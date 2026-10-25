package systel

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
)

// A metrics sidecar fills the readings a platform's own interface does not
// expose cheaply in process. On Windows the kernel graphics interface (D3DKMT)
// gives an AMD or Intel GPU only its memory and utilization — no temperature,
// power, clocks, fan or PCIe link — so a small external process reads those
// from the vendor's own source (AMD ADLX/ADL for a discrete board; the Ryzen
// SMU power-management table, via a PawnIO/ZenStates-style helper, for an APU's
// integrated graphics) and writes them to a file this reader merges in.
//
// The sidecar never adds a device and never overrides a reading the platform
// already made: it only fills fields left nil. A missing file is the ordinary
// state of a host without the sidecar installed and is silent; a file that is
// present but stale or malformed is reported once, because then something that
// should be feeding readings has stopped.
//
// The file is the contract. Its shape, keyed so each entry finds its device:
//
//	{
//	  "version": 1,
//	  "updated_unix": 1730000000,
//	  "gpus": [
//	    {"pci_id":"0000:03:00.0","name":"Radeon RX 7900 XTX","temp_c":55,"power_w":120,
//	     "power_limit_w":327,"clock_mhz":2400,"clock_max_mhz":2860,"fan_pct":38,
//	     "link":{"gen":4,"width":16,"max_gen":4,"max_width":16}},
//	    {"integrated":true,"name":"AMD Radeon(TM) Graphics","temp_c":48,
//	     "clock_mhz":2000,"clock_max_mhz":2200,"power_w":15}
//	  ]
//	}
const (
	sidecarVersion = 1
	// sidecarMaxAge is how old the file may be before its numbers are treated
	// as stale: a few refresh intervals, so a sidecar that writes every second
	// or two is never mistaken for a dead one.
	sidecarMaxAge = 10 * time.Second
)

type sidecarReader struct {
	path   string           // empty disables the reader
	now    func() time.Time // time source, for tests
	warned bool             // a present-but-unusable file is reported once
	// proc runs the sidecar the installer ships while the snapshot is polled;
	// nil where the server starts none (a sidecar run by hand still works).
	proc sidecarProcess
}

// sidecarProcess is the sidecar the server runs. ensure starts it when it is
// not running (fresh: at once; else after a back-off, so a sidecar that keeps
// dying is not restarted every interval) and never waits for it; stop ends it
// and removes its file, so a stopped sidecar's numbers are never read as
// stale ones.
type sidecarProcess interface {
	ensure(fresh bool)
	stop()
}

func (r *sidecarReader) reset() {
	r.warned = false
	if r.proc != nil {
		r.proc.ensure(true)
	}
}

func (r *sidecarReader) stop() {
	if r.proc != nil {
		r.proc.stop()
	}
}

type sidecarFile struct {
	Version     int          `json:"version"`
	UpdatedUnix int64        `json:"updated_unix"`
	GPUs        []sidecarGPU `json:"gpus"`
}

type sidecarGPU struct {
	PCIID       string          `json:"pci_id,omitempty"`
	Name        string          `json:"name,omitempty"`
	Integrated  bool            `json:"integrated,omitempty"`
	TempC       *float64        `json:"temp_c,omitempty"`
	PowerW      *float64        `json:"power_w,omitempty"`
	PowerLimitW *float64        `json:"power_limit_w,omitempty"`
	ClockMHz    *uint64         `json:"clock_mhz,omitempty"`
	ClockMaxMHz *uint64         `json:"clock_max_mhz,omitempty"`
	FanPct      *float64        `json:"fan_pct,omitempty"`
	UtilPct     *float64        `json:"util_pct,omitempty"`
	Link        *api.SystemLink `json:"link,omitempty"`
}

func (r *sidecarReader) read(s *Snapshot) {
	if r.path == "" {
		return
	}
	if r.proc != nil {
		r.proc.ensure(false)
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		if !os.IsNotExist(err) {
			r.warnOnce("GPU metrics sidecar file cannot be read", err)
		}
		return // no file: no sidecar installed, nothing to say
	}
	var f sidecarFile
	if err := json.Unmarshal(b, &f); err != nil {
		r.warnOnce("GPU metrics sidecar file is not valid JSON", err)
		return
	}
	if f.Version != sidecarVersion {
		r.warnOnce("GPU metrics sidecar file has an unsupported version", fmt.Errorf("got %d, want %d", f.Version, sidecarVersion))
		return
	}
	if f.UpdatedUnix == 0 {
		r.warnOnce("GPU metrics sidecar file has no timestamp", fmt.Errorf("version %d", f.Version))
		return
	}
	if age := r.clock().Sub(time.Unix(f.UpdatedUnix, 0)); age > sidecarMaxAge {
		r.warnOnce("GPU metrics sidecar file is stale", fmt.Errorf("last written %s ago", age.Round(time.Second)))
		return
	}
	r.warned = false
	for _, g := range f.GPUs {
		if i := matchSidecarDevice(s.Devices, g); i >= 0 {
			enrichFromSidecar(&s.Devices[i], g)
		}
	}
}

func (r *sidecarReader) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *sidecarReader) warnOnce(msg string, err error) {
	if r.warned {
		return
	}
	r.warned = true
	slog.Warn(msg, "path", r.path, "error", err)
}

// matchSidecarDevice finds the device a sidecar entry describes: by PCI id when
// both carry one, else the integrated device for an entry with no bus address
// (an APU's graphics, read from the SMU), else by name.
func matchSidecarDevice(devs []api.SystemDevice, g sidecarGPU) int {
	if g.PCIID != "" {
		for i := range devs {
			if devs[i].PCIID != "" && strings.EqualFold(devs[i].PCIID, g.PCIID) {
				return i
			}
		}
		return -1
	}
	if g.Integrated {
		for i := range devs {
			if devs[i].Integrated {
				return i
			}
		}
	}
	if g.Name != "" {
		for i := range devs {
			if strings.EqualFold(devs[i].Name, g.Name) {
				return i
			}
		}
	}
	return -1
}

// enrichFromSidecar fills only the fields the platform left nil; a reading the
// vendor's own driver already made is authoritative and kept.
func enrichFromSidecar(d *api.SystemDevice, g sidecarGPU) {
	if d.TempC == nil {
		d.TempC = g.TempC
	}
	if d.PowerW == nil {
		d.PowerW = g.PowerW
	}
	if d.PowerLimitW == nil {
		d.PowerLimitW = g.PowerLimitW
	}
	if d.ClockMHz == nil {
		d.ClockMHz = g.ClockMHz
	}
	if d.ClockMaxMHz == nil {
		d.ClockMaxMHz = g.ClockMaxMHz
	}
	if d.FanPct == nil {
		d.FanPct = g.FanPct
	}
	if d.UtilPct == nil {
		d.UtilPct = g.UtilPct
	}
	if d.Link == nil {
		d.Link = g.Link
	}
}
