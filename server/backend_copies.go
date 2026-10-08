package server

import (
	"slices"

	"github.com/ollama/ollama/discover"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// xollama-hook: backend-copies — see discover/backend_copies.go.
//
// The scheduler's GPU list has one entry per GPU, upstream's. A load whose
// device pin or the server's GPU policy names a backend gets the other
// backend copies added before its devices are chosen, so a 3090 can be used
// through Vulkan: alone (`gpu.devices[].backend = Vulkan`, or a model pinned to
// Vulkan) or beside an RX 9070 XT in one split. applyGPUPolicy then leaves one
// entry per GPU again. Every other load sees exactly the list it saw before.

// discoverBackendCopies is the copies discovery holds, behind a seam for tests.
var discoverBackendCopies = discover.BackendCopies

// withBackendCopies adds to gpus the backend copies discovery holds, when the
// load asks for a backend. A copy is the same silicon as the entry already in
// gpus, so it shares that entry's free memory as the scheduler accounted it
// (updateFreeSpace); the lower of the two readings is kept for both.
func withBackendCopies(cfg *xollama.Config, gpus []ml.DeviceInfo) []ml.DeviceInfo {
	if len(gpus) == 0 || !wantsBackend(cfg, serverGPU()) {
		return gpus
	}
	out := slices.Clone(gpus)
	for _, c := range discoverBackendCopies() {
		if slices.ContainsFunc(out, func(g ml.DeviceInfo) bool { return g.Library == c.Library && g.ID == c.ID }) {
			continue
		}
		i := slices.IndexFunc(out, func(g ml.DeviceInfo) bool {
			return g.Library != c.Library && !g.Integrated && samePCI(g.PCIID, c.PCIID)
		})
		if i < 0 {
			continue // not a copy of a GPU this load may use
		}
		free := min(c.FreeMemory, out[i].FreeMemory)
		c.FreeMemory, out[i].FreeMemory = free, free
		out = append(out, c)
	}
	return out
}

// wantsBackend: the model is pinned to a backend, or the policy chooses one
// for some GPU.
func wantsBackend(cfg *xollama.Config, g *xollama.GPUSettings) bool {
	if cfg != nil && !cfg.Devices.IsZero() && cfg.Devices.Backend != "" && cfg.Devices.Backend != "CPU" {
		return true
	}
	if g == nil {
		return false
	}
	return slices.ContainsFunc(g.Devices, func(d xollama.GPUDevice) bool { return d.Backend != "" })
}

// upstreamPreferred is the entry upstream's discovery keeps for a GPU
// reachable through several backends: CUDA or ROCm over Vulkan.
func upstreamPreferred(d ml.DeviceInfo, all []ml.DeviceInfo) bool {
	if d.Library != "Vulkan" || d.PCIID == "" {
		return true
	}
	return !slices.ContainsFunc(all, func(o ml.DeviceInfo) bool {
		return (o.Library == "CUDA" || o.Library == "ROCm") && samePCI(o.PCIID, d.PCIID)
	})
}
