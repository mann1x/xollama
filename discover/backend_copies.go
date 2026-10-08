package discover

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// xollama-hook: backend-copies — one GPU reachable through two backends.
//
// A 3090 is both a CUDA and a Vulkan device. Upstream's discovery keeps one
// entry per physical GPU and drops the Vulkan one in favour of CUDA or ROCm
// (DeviceInfo.PreferredLibrary), so nothing after it could ever choose
// Vulkan for that card: the server's GPU policy (`gpu.devices[].backend`,
// server/xollama_gpu.go) and a model's device pin (`--device-backend`) never
// saw the entry they asked for, and one model could not be split across a
// 3090 and an RX 9070 XT, which only Vulkan reaches together.
//
// So while opencoti may serve, discovery keeps that Vulkan copy, but only in
// its own list. GPUDevices still returns one entry per GPU, upstream's
// choice, because its callers count GPUs and add up their memory (the default
// context is sized from it at startup; a copy would double the 3090).
// GPUDevicesAllBackends returns the copies too: the device menus and the link
// probe show them, and the scheduler adds them to a load only when its pin or
// the policy names a backend (withBackendCopies, server/xollama_gpu.go).
//
// A copy is only kept when both entries carry the same PCI ID: that is the
// one identity that later says, without guessing, which entries are one GPU.
// Under XOLLAMA_ENGINE=llamacpp nothing here runs.

// keepBackendCopy reports whether a and b, which upstream's dedup is about to
// collapse into one, are kept as two backend copies of one GPU.
func keepBackendCopy(a, b ml.DeviceInfo) bool {
	if !backendCopiesEnabled() {
		return false
	}
	return isBackendCopy(a, b)
}

func backendCopiesEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(envconfig.Var(engine.EnvSelector)), string(engine.KindLlamaCpp))
}

// isBackendCopy: a discrete GPU seen through Vulkan and through CUDA or ROCm,
// with the same PCI ID.
func isBackendCopy(a, b ml.DeviceInfo) bool {
	if a.Integrated || b.Integrated || a.Library == b.Library {
		return false
	}
	vk, other := a, b
	if b.Library == "Vulkan" {
		vk, other = b, a
	}
	if vk.Library != "Vulkan" || (other.Library != "CUDA" && other.Library != "ROCm") {
		return false
	}
	return samePCIID(vk.PCIID, other.PCIID)
}

func samePCIID(a, b string) bool {
	x, ok1 := xollama.CanonicalPCIID(a)
	y, ok2 := xollama.CanonicalPCIID(b)
	return ok1 && ok2 && x == y
}

// onePerGPU drops the Vulkan copy of every GPU that CUDA or ROCm also serves:
// upstream's choice, applied to what discovery kept.
func onePerGPU(devices []ml.DeviceInfo) []ml.DeviceInfo {
	return slices.DeleteFunc(devices, isDroppedCopy(devices))
}

func isDroppedCopy(all []ml.DeviceInfo) func(ml.DeviceInfo) bool {
	return func(d ml.DeviceInfo) bool {
		if d.Library != "Vulkan" {
			return false
		}
		return slices.ContainsFunc(all, func(o ml.DeviceInfo) bool { return isBackendCopy(d, o) })
	}
}

// GPUDevicesAllBackends is GPUDevices with every backend copy of a GPU kept:
// a 3090 appears once under CUDA and once under Vulkan. Only for callers that
// choose a backend per GPU; anything that counts GPUs or adds up their memory
// must use GPUDevices.
func GPUDevicesAllBackends(ctx context.Context, runners []ml.FilteredRunnerDiscovery) []ml.DeviceInfo {
	return append(GPUDevices(ctx, runners), BackendCopies()...)
}

// BackendCopies returns the copies GPUDevices leaves out, as of the last
// discovery or refresh: it refreshes nothing itself, so a caller that has
// just called GPUDevices does not pay for a second refresh.
func BackendCopies() []ml.DeviceInfo {
	deviceMu.Lock()
	defer deviceMu.Unlock()
	var copies []ml.DeviceInfo
	dropped := isDroppedCopy(devices)
	for _, d := range devices {
		if dropped(d) {
			copies = append(copies, d)
		}
	}
	return copies
}

// keepListedCopy: an opencoti listing that another backend already serves
// (secondListing) is kept when it is a Vulkan copy of that GPU by PCI ID.
func keepListedCopy(library string, oc opencotiDevice, served ml.DeviceInfo) bool {
	if !backendCopiesEnabled() || oc.integrated || oc.pciID == "" {
		return false
	}
	return isBackendCopy(ml.DeviceInfo{DeviceID: ml.DeviceID{Library: library}, PCIID: oc.pciID}, served)
}

// listedIDRegex reads the identity opencoti appends to a device line when
// OPENCOTI_LIST_DEVICE_IDS=1 (list_device_ids_v1):
//
//	Vulkan1: NVIDIA GeForce RTX 3090 (24322 MiB, 23554 MiB free) id=0000:11:00.0
//	Vulkan2: AMD Radeon RX 9070 XT (16304 MiB, 15419 MiB free) id=uuid:0000…0000 pci=0000:03:00.0
//
// pci= is the PCI address where the id is something else (a uuid on AMD under
// Windows); otherwise the id is the PCI address itself.
var listedIDRegex = regexp.MustCompile(`\sid=(\S+)(?:\s+pci=(\S+))?\s*$`)

// listedPCIID returns the PCI ID a device line carries, or "".
func listedPCIID(line string) string {
	m := listedIDRegex.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	for _, s := range []string{m[2], m[1]} {
		if id, ok := xollama.CanonicalPCIID(s); ok {
			return id
		}
	}
	return ""
}

// listDeviceIDsEnv asks the engine to print each device's identity.
const listDeviceIDsEnv = "OPENCOTI_LIST_DEVICE_IDS=1"
