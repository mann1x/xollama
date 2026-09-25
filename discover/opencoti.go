package discover

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

// opencotiEnumerateTimeout bounds one backend's enumeration. A Vulkan run is
// not fast: every installed ICD is initialised, and on solidPC the NVIDIA ICD
// alone costs 8.2 s to report no devices (RADV answers in 43 ms). That is paid
// once, at bootstrap — never on a free-memory refresh.
const opencotiEnumerateTimeout = 45 * time.Second

// opencotiBackends are the backends opencoti can be asked to list, in the
// order they are run.
var opencotiBackends = []engine.Backend{engine.BackendCUDA, engine.BackendVulkan}

// opencotiListDevices runs one enumeration and returns its combined output.
// It is a variable so tests can stand in for the artifact.
var opencotiListDevices = func(ctx context.Context, artifact string, b engine.Backend) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, opencotiEnumerateTimeout)
	defer cancel()
	name, args := engine.EnumerateCommand(artifact, b, runtime.GOOS)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = llamaServerDiscoveryWaitDelay
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// opencotiArtifact locates the engine the way a launch does, so discovery
// never enumerates through a different artifact than the one that will run.
var opencotiArtifact = func() (string, error) {
	home, _ := os.UserHomeDir()
	return engine.Find(envconfig.Var(engine.EnvPath), engine.DefaultDirs(ml.LibOllamaPath, home))
}

// opencotiDevice is one device as opencoti listed it.
type opencotiDevice struct {
	index        int // position within its backend: what *_VISIBLE_DEVICES selects
	name         string
	description  string
	total, free  uint64
	integrated   bool
	computeMajor int
	computeMinor int
}

// parseOpencotiDevices reads one backend's `--list-devices --verbose` output.
// The device lines are llama-server's format; the verbose lines add the CUDA
// compute capability and the Vulkan uma flag.
func parseOpencotiDevices(output string, library string) []opencotiDevice {
	uma := parseVulkanUMA(output)
	cc := map[int][2]int{}
	var devices []opencotiDevice
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if m := cudaCCRegex.FindStringSubmatch(line); m != nil {
			idx, _ := strconv.Atoi(m[1])
			major, _ := strconv.Atoi(m[2])
			minor, _ := strconv.Atoi(m[3])
			cc[idx] = [2]int{major, minor}
			continue
		}
		m := deviceLineRegex.FindStringSubmatch(line)
		if m == nil || inferLibrary(m[1], m[2]) != library {
			continue
		}
		totalMiB, _ := strconv.ParseUint(m[3], 10, 64)
		freeMiB, _ := strconv.ParseUint(m[4], 10, 64)
		devices = append(devices, opencotiDevice{
			index:       len(devices),
			name:        m[1],
			description: m[2],
			total:       totalMiB * 1024 * 1024,
			free:        freeMiB * 1024 * 1024,
		})
	}
	for i := range devices {
		devices[i].integrated = library == "Vulkan" && uma[devices[i].index]
		if c, ok := cc[devices[i].index]; ok {
			devices[i].computeMajor, devices[i].computeMinor = c[0], c[1]
		}
	}
	return devices
}

// overlayOpencotiDevices makes opencoti the source of truth for every backend
// it will serve: which devices exist, their indexes and their memory.
//
// Identity — PCI ID, integrated, driver — is not something the engine
// reports, so it is joined from what llama.cpp and the native probe found, by
// description and then by position. A device opencoti lists that joins
// nothing is kept, unless it is plainly a second listing of one already kept:
// an older ggml-vulkan enumerates a GPU once per installed ICD (solidPC's
// Renoir iGPU appears as "AMD RADV RENOIR" and again, through amdvlk, as
// "Unknown AMD GPU"). A llama.cpp device opencoti does not list is dropped:
// the engine that would run on it cannot see it.
//
// Under XOLLAMA_ENGINE=llamacpp this returns devices unchanged without
// running anything.
func overlayOpencotiDevices(ctx context.Context, devices []ml.DeviceInfo) []ml.DeviceInfo {
	selector := envconfig.Var(engine.EnvSelector)
	host := engine.Host()
	var backends []engine.Backend
	for _, b := range opencotiBackends {
		if engine.Enumerates(host, b, selector) {
			backends = append(backends, b)
		}
	}
	if len(backends) == 0 {
		return devices
	}
	artifact, err := opencotiArtifact()
	if err != nil {
		slog.Debug("opencoti device enumeration skipped: no engine artifact", "error", err)
		return devices
	}

	for _, b := range backends {
		start := time.Now()
		output, err := opencotiListDevices(ctx, artifact, b)
		listed := parseOpencotiDevices(output, string(b))
		slog.Debug("opencoti device enumeration", "backend", b, "devices", len(listed), "duration", time.Since(start), "error", err)
		if err != nil && len(listed) == 0 {
			// Nothing to be authoritative with; the engine may simply have no
			// payload for this backend on this host. llama.cpp's view stands.
			continue
		}
		devices = mergeOpencotiBackend(devices, string(b), listed, selector)
	}
	return devices
}

// mergeOpencotiBackend replaces one backend's devices with opencoti's list,
// joined to the identity discovery already has.
func mergeOpencotiBackend(devices []ml.DeviceInfo, library string, listed []opencotiDevice, selector string) []ml.DeviceInfo {
	var others, existing []ml.DeviceInfo
	for _, d := range devices {
		if d.Library == library {
			existing = append(existing, d)
		} else {
			others = append(others, d)
		}
	}

	// Join by name first, and only then by position, so a listing whose name
	// does match can never lose its identity to an earlier one that merely
	// sits at the same index.
	used := make([]bool, len(existing))
	joinedTo := make([]int, len(listed))
	for j, oc := range listed {
		joinedTo[j] = -1
		for i, d := range existing {
			if !used[i] && ml.SimilarDeviceDescription(d.Description, oc.description) {
				used[i], joinedTo[j] = true, i
				break
			}
		}
	}
	// Same backend, same driver, same order: when two ggml versions spell a
	// name differently, position is the remaining evidence. Only accepted when
	// the memory agrees, so it cannot pair an iGPU with a discrete card.
	for j, oc := range listed {
		if joinedTo[j] < 0 && oc.index < len(existing) && !used[oc.index] &&
			ml.SimilarDeviceMemory(existing[oc.index].TotalMemory, oc.total) {
			used[oc.index], joinedTo[j] = true, oc.index
		}
	}

	// Joined listings are kept first, so a duplicate is always measured
	// against the listing that carries an identity. Among the rest, a driver
	// that cannot name the chip ("Unknown AMD GPU") is the one set aside.
	order := make([]int, 0, len(listed))
	for j := range listed {
		if joinedTo[j] >= 0 {
			order = append(order, j)
		}
	}
	var unjoined []int
	for j := range listed {
		if joinedTo[j] < 0 {
			unjoined = append(unjoined, j)
		}
	}
	slices.SortStableFunc(unjoined, func(a, b int) int {
		return cmpBool(isUnnamed(listed[a].description), isUnnamed(listed[b].description))
	})
	order = append(order, unjoined...)

	var merged []ml.DeviceInfo
	for _, j := range order {
		oc := listed[j]
		if oc.total == 0 {
			continue // BLAS-style pseudo-device, as llama-server discovery skips
		}
		joined := joinedTo[j] >= 0
		var d ml.DeviceInfo
		if joined {
			d = existing[joinedTo[j]]
		} else {
			if dup, ok := secondListing(oc, merged, others); ok {
				slog.Info("opencoti lists a device twice; keeping one listing",
					"library", library, "kept", dup.Description, "dropped", oc.description, "index", oc.index)
				continue
			}
			d = ml.DeviceInfo{
				DeviceID:    ml.DeviceID{Library: library},
				Name:        oc.name,
				Description: oc.description,
				LibraryPath: []string{ml.LibOllamaPath},
			}
		}
		if oc.computeMajor > 0 {
			d.ComputeMajor, d.ComputeMinor = oc.computeMajor, oc.computeMinor
		}
		// A device below the engine's capability floor is llama.cpp's to serve
		// under auto, so llama.cpp's view of it is the one that stays.
		if library == "CUDA" && !strings.EqualFold(strings.TrimSpace(selector), "opencoti") &&
			!engine.SupportsDevice(engine.Host(), engine.Device{Backend: engine.BackendCUDA, ComputeMajor: d.ComputeMajor, ComputeMinor: d.ComputeMinor}) {
			if joined {
				merged = append(merged, d)
			}
			continue
		}
		d.ID = strconv.Itoa(oc.index)
		d.FilterID = ""
		remapFilterIDForUserVisibleDevices(&d)
		d.TotalMemory, d.FreeMemory = oc.total, oc.free
		d.Integrated = d.Integrated || oc.integrated
		merged = append(merged, d)
	}
	// Back into the engine's own order, which is the order its indexes mean.
	slices.SortStableFunc(merged, func(a, b ml.DeviceInfo) int {
		ai, aerr := strconv.Atoi(a.ID)
		bi, berr := strconv.Atoi(b.ID)
		if aerr != nil || berr != nil {
			return 0
		}
		return ai - bi
	})

	for i, d := range existing {
		if !used[i] {
			slog.Info("dropping a device the engine that serves it does not list",
				"library", library, "id", d.ID, "description", d.Description, "pci_id", d.PCIID)
		}
	}
	return append(others, merged...)
}

// secondListing reports whether an unjoined opencoti device is another
// listing of a device already kept: one iGPU seen through a second ICD, or a
// discrete GPU another backend already serves (a Vulkan listing of a CUDA
// card).
func secondListing(oc opencotiDevice, kept, others []ml.DeviceInfo) (ml.DeviceInfo, bool) {
	for _, k := range kept {
		if oc.integrated && k.Integrated && ml.SimilarDeviceMemory(k.TotalMemory, oc.total) {
			return k, true
		}
	}
	for _, o := range others {
		if ml.SimilarDeviceDescription(o.Description, oc.description) && ml.SimilarDeviceMemory(o.TotalMemory, oc.total) {
			return o, true
		}
	}
	return ml.DeviceInfo{}, false
}

// isUnnamed reports a driver that could not identify the chip it drives.
func isUnnamed(description string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(description)), "unknown")
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}
