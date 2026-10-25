package systel

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
)

// drmReader reads AMD and Intel GPUs from /sys/class/drm, and NVIDIA ones
// only when NVML is missing, for their link. sysfs is the root, for tests.
type drmReader struct {
	sysfs string
	// energy remembers each Intel card's energy counter, for its power.
	energy map[string]energySample
}

type energySample struct {
	uj uint64
	at time.Time
}

var cardDir = regexp.MustCompile(`^card[0-9]+$`)

func (r *drmReader) reset() { r.energy = nil }

func (r *drmReader) read(s *Snapshot) {
	cards, _ := filepath.Glob(filepath.Join(r.sysfs, "class", "drm", "card*"))
	seen := map[string]bool{}
	next := map[string]energySample{}
	for _, card := range cards {
		if !cardDir.MatchString(filepath.Base(card)) {
			continue // a connector, card0-DP-1
		}
		dev := filepath.Join(card, "device")
		real, err := filepath.EvalSymlinks(dev)
		if err != nil {
			continue
		}
		pci := normalizePCI(filepath.Base(real))
		if seen[pci] {
			continue
		}
		seen[pci] = true
		var d api.SystemDevice
		switch strings.TrimSpace(readString(filepath.Join(dev, "vendor"))) {
		case "0x1002":
			d = api.SystemDevice{Vendor: "amd", Name: readString(filepath.Join(dev, "product_name"))}
		case "0x8086":
			d = api.SystemDevice{Vendor: "intel"}
		case "0x10de":
			if nvmlOpen() != nil {
				continue // NVML lists it, with everything
			}
			d = api.SystemDevice{Vendor: "nvidia"}
		default:
			continue
		}
		d.Source, d.PCIID = "sysfs", pci
		d.Link = pcieLink(dev)
		// A discrete GPU the kernel has powered down is woken by most of
		// these files: leave it asleep, with only what needs no wake.
		if strings.TrimSpace(readString(filepath.Join(dev, "power", "runtime_status"))) == "suspended" {
			s.Devices = append(s.Devices, d)
			continue
		}
		switch d.Vendor {
		case "amd":
			amdReadings(dev, &d)
		case "intel":
			r.intelReadings(card, dev, pci, s.At, &d, next)
		}
		s.Devices = append(s.Devices, d)
	}
	r.energy = next
}

func amdReadings(dev string, d *api.SystemDevice) {
	if v := readUint(filepath.Join(dev, "gpu_busy_percent")); v != nil {
		d.UtilPct = ptr(float64(*v))
	}
	total := readUint(filepath.Join(dev, "mem_info_vram_total"))
	used := readUint(filepath.Join(dev, "mem_info_vram_used"))
	// An APU's dedicated VRAM (mem_info_vram_total) is only a small UMA
	// carve-out; the memory it can actually use is the shared GTT pool, which is
	// what /api/xollama/devices reports. Report dedicated + GTT for an iGPU, with
	// the same thresholds as discover/amd.go, so the two endpoints agree
	// (bug-320: a Cezanne iGPU showed 512 MB here and its full pool on /devices).
	if gtt := readUint(filepath.Join(dev, "mem_info_gtt_total")); total != nil && gtt != nil && isIntegratedUMA(*total, *gtt) {
		d.Integrated = true
		sum := *total + *gtt
		total = &sum
	}
	if total != nil {
		d.TotalMemory = total
		if used != nil && *used <= *total {
			d.UsedMemory = used
			d.FreeMemory = ptr(*total - *used)
		}
	}
	d.ClockMHz, d.ClockMaxMHz = dpmClock(filepath.Join(dev, "pp_dpm_sclk"))
	hw := hwmonDir(dev)
	if hw == "" {
		return
	}
	if v := readUint(filepath.Join(hw, "temp1_input")); v != nil {
		d.TempC = ptr(float64(*v) / 1000)
	}
	power := readUint(filepath.Join(hw, "power1_average"))
	if power == nil {
		power = readUint(filepath.Join(hw, "power1_input"))
	}
	if power != nil {
		d.PowerW = ptr(float64(*power) / 1e6)
	}
	if v := readUint(filepath.Join(hw, "power1_cap")); v != nil && *v > 0 {
		d.PowerLimitW = ptr(float64(*v) / 1e6)
	}
	if v := readUint(filepath.Join(hw, "pwm1")); v != nil {
		d.FanPct = ptr(float64(*v) * 100 / 255)
	}
}

func (r *drmReader) intelReadings(card, dev, pci string, at time.Time, d *api.SystemDevice, next map[string]energySample) {
	d.ClockMHz = readUint(filepath.Join(card, "gt_cur_freq_mhz"))
	d.ClockMaxMHz = readUint(filepath.Join(card, "gt_max_freq_mhz"))
	hw := hwmonDir(dev)
	if hw == "" {
		return
	}
	if v := readUint(filepath.Join(hw, "power1_max")); v != nil && *v > 0 {
		d.PowerLimitW = ptr(float64(*v) / 1e6)
	}
	// Discrete Arc reports energy, not power: power is its rate.
	if e := readUint(filepath.Join(hw, "energy1_input")); e != nil {
		next[pci] = energySample{uj: *e, at: at}
		if prev, ok := r.energy[pci]; ok && *e >= prev.uj {
			if dt := at.Sub(prev.at).Seconds(); dt > 0 {
				d.PowerW = ptr(float64(*e-prev.uj) / 1e6 / dt)
			}
		}
	}
}

// pcieLink reads the link as the bus reports it.
func pcieLink(dev string) *api.SystemLink {
	l := &api.SystemLink{
		Gen:      pcieGen(readString(filepath.Join(dev, "current_link_speed"))),
		MaxGen:   pcieGen(readString(filepath.Join(dev, "max_link_speed"))),
		Width:    readInt(filepath.Join(dev, "current_link_width")),
		MaxWidth: readInt(filepath.Join(dev, "max_link_width")),
	}
	if l.Gen == nil && l.MaxGen == nil && l.Width == nil && l.MaxWidth == nil {
		return nil
	}
	return l
}

// pcieGen maps "16.0 GT/s PCIe" to 4.
func pcieGen(s string) *int {
	f := strings.Fields(s)
	if len(f) == 0 {
		return nil
	}
	gts, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil
	}
	for gen, rate := range []float64{2.5, 5, 8, 16, 32, 64} {
		if gts == rate {
			return ptr(gen + 1)
		}
	}
	return nil
}

// dpmClock reads amdgpu's clock table: the line marked '*' is the current
// level, and the highest is the most it runs at.
func dpmClock(path string) (cur, maxClock *uint64) {
	for _, line := range strings.Split(readString(path), "\n") {
		_, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.ToLower(f[0]), "mhz"), 10, 64)
		if err != nil {
			continue
		}
		if maxClock == nil || n > *maxClock {
			maxClock = ptr(n)
		}
		if strings.HasSuffix(strings.TrimSpace(v), "*") {
			cur = ptr(n)
		}
	}
	return cur, maxClock
}

func hwmonDir(dev string) string {
	m, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func readString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readUint(path string) *uint64 {
	n, err := strconv.ParseUint(readString(path), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func readInt(path string) *int {
	n, err := strconv.Atoi(readString(path))
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}
