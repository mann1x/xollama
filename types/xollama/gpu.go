package xollama

import (
	"fmt"
	"slices"
)

// GPUSettings are the server's GPU policy (plans/system-settings.md), set
// with `xollama tweak server gpu`. A model's own device pin (Devices) still
// decides which devices that model may use; the policy decides the rest.
type GPUSettings struct {
	// Devices holds the settings of individual GPUs, keyed by PCI ID. A GPU
	// not listed is allowed, at priority 0, on its first backend.
	Devices []GPUDevice `json:"devices,omitempty"`

	// SplitPolicy is when a model is spread over several GPUs: "auto" (or empty)
	// splits only a model that does not fit one GPU, "spread" always splits
	// (OLLAMA_SCHED_SPREAD), "single" never splits, so a model too large for
	// one GPU leaves the rest of its layers on the CPU.
	SplitPolicy string `json:"split,omitempty"`

	// SplitMode is the engine's --split-mode for a split model: "layer"
	// (llama.cpp's default, the one opencoti validates on every path) or
	// "row" (CUDA only and unmeasured with opencoti's KV paths; Vulkan turns
	// it into layer). Empty leaves the engine's.
	SplitMode string `json:"split_mode,omitempty"`
}

// GPUDevice is one GPU's settings.
type GPUDevice struct {
	// ID is the GPU's PCI ID ("0000:01:00.0").
	ID string `json:"id"`
	// Disabled keeps every model off this GPU, unless a model's own pin
	// names it.
	Disabled bool `json:"disabled,omitempty"`
	// Priority orders the GPUs: a model that fits on one goes to the
	// highest-priority GPU with room, and a split fills them in this order.
	Priority int `json:"priority,omitempty"`
	// Backend is the one to use where this GPU is reachable through more
	// than one (a 3090 is both a CUDA and a Vulkan device).
	Backend string `json:"backend,omitempty"`
	// LinkGBps forces the host link speed the engine plans with
	// (OPENCOTI_LINK_GBPS), instead of its boot probe. Zero probes.
	LinkGBps float64 `json:"link_gbps,omitempty"`
}

// Split policies and split modes.
const (
	SplitAuto   = "auto"
	SplitSpread = "spread"
	SplitSingle = "single"
)

var (
	validSplits = []string{SplitAuto, SplitSpread, SplitSingle}
	// tensor is accepted by the engine's parser but unsupported on every
	// opencoti path (opencoti #609), so it is not offered.
	validSplitModes = []string{"layer", "row"}
)

// ValidSplits returns the split policies.
func ValidSplits() []string { return slices.Clone(validSplits) }

// ValidSplitModes returns the engine split modes.
func ValidSplitModes() []string { return slices.Clone(validSplitModes) }

// IsZero reports whether the policy states nothing.
func (g *GPUSettings) IsZero() bool {
	return g == nil || (len(g.Devices) == 0 && g.SplitPolicy == "" && g.SplitMode == "")
}

// Device returns the settings for the GPU with this PCI ID, if any.
func (g *GPUSettings) Device(pciID string) (GPUDevice, bool) {
	if g == nil {
		return GPUDevice{}, false
	}
	want, ok := CanonicalPCIID(pciID)
	if !ok {
		return GPUDevice{}, false
	}
	for _, d := range g.Devices {
		if id, _ := CanonicalPCIID(d.ID); id == want {
			return d, true
		}
	}
	return GPUDevice{}, false
}

// Validate checks the policy.
func (g *GPUSettings) Validate() error {
	if g == nil {
		return nil
	}
	if g.SplitPolicy != "" && !slices.Contains(validSplits, g.SplitPolicy) {
		return fmt.Errorf("gpu: unknown split %q (want one of %v)", g.SplitPolicy, validSplits)
	}
	if g.SplitMode != "" && !slices.Contains(validSplitModes, g.SplitMode) {
		return fmt.Errorf("gpu: unknown split_mode %q (want one of %v)", g.SplitMode, validSplitModes)
	}
	if g.SplitMode != "" && g.SplitPolicy == SplitSingle {
		return fmt.Errorf("gpu: split_mode %q says how to split, and split %q never splits", g.SplitMode, g.SplitPolicy)
	}
	seen := map[string]bool{}
	for _, d := range g.Devices {
		id, ok := CanonicalPCIID(d.ID)
		if !ok {
			return fmt.Errorf("gpu: %q is not a PCI ID", d.ID)
		}
		if seen[id] {
			return fmt.Errorf("gpu: %s is listed twice", id)
		}
		seen[id] = true
		if d.Backend != "" && (CanonicalBackend(d.Backend) == "" || CanonicalBackend(d.Backend) == "CPU") {
			return fmt.Errorf("gpu: %s: unknown backend %q", id, d.Backend)
		}
		if d.LinkGBps < 0 || d.LinkGBps > 1024 {
			return fmt.Errorf("gpu: %s: link_gbps %v is not a link speed", id, d.LinkGBps)
		}
	}
	return nil
}

// Split returns the split policy, "" when the policy states none.
func (g *GPUSettings) Split() string {
	if g == nil {
		return ""
	}
	return g.SplitPolicy
}
