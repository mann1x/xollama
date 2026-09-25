package server

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// selectModelDevices narrows the discovered devices to the ones this model's
// config pins it to, or refuses the load.
//
// A refusal, never a fallback. A pin exists to keep a model off hardware —
// an agent's 2B model off the discrete card another model needs — so quietly
// serving it somewhere else when the pinned device is missing breaks the one
// promise the pin makes, and the user finds out from somebody else's OOM.
//
// With no pin, an integrated Vulkan GPU is set aside whenever a discrete GPU
// is present. It reports host RAM as its memory, so to the placement code it
// looks like the largest device on the machine: a model too big for the
// discrete card would move onto the iGPU wholesale rather than partially
// offload on the card, which is the slower answer in every case measured. An
// iGPU is something a model asks for.
func selectModelDevices(cfg *xollama.Config, gpus []ml.DeviceInfo) ([]ml.DeviceInfo, error) {
	if cfg == nil || cfg.Devices.IsZero() {
		return setAsideIntegratedVulkan(gpus), nil
	}
	pin := cfg.Devices
	if pin.Backend == "CPU" {
		return []ml.DeviceInfo{}, nil
	}

	var candidates []ml.DeviceInfo
	for _, g := range gpus {
		if g.Library == pin.Backend {
			candidates = append(candidates, g)
		}
	}
	if len(pin.IDs) == 0 {
		if len(candidates) == 0 {
			return nil, deviceRefusal(pin, nil, gpus)
		}
		return candidates, nil
	}

	selected := make([]bool, len(candidates))
	var missing []string
	for _, id := range pin.IDs {
		matched := false
		for i, c := range candidates {
			if deviceMatches(c, id) {
				selected[i], matched = true, true
			}
		}
		if !matched {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, deviceRefusal(pin, missing, gpus)
	}
	var out []ml.DeviceInfo
	for i, c := range candidates {
		if selected[i] {
			out = append(out, c)
		}
	}
	return out, nil
}

// deviceMatches reports whether one pin entry names this device.
func deviceMatches(d ml.DeviceInfo, id string) bool {
	switch id {
	case xollama.DeviceIntegrated:
		return d.Integrated
	case xollama.DeviceDiscrete:
		return !d.Integrated
	}
	if pci, ok := xollama.CanonicalPCIID(id); ok {
		dpci, ok := xollama.CanonicalPCIID(d.PCIID)
		return ok && dpci == pci
	}
	return d.ID == id
}

func setAsideIntegratedVulkan(gpus []ml.DeviceInfo) []ml.DeviceInfo {
	if !slices.ContainsFunc(gpus, func(g ml.DeviceInfo) bool { return !g.Integrated }) {
		return gpus
	}
	if !slices.ContainsFunc(gpus, isIntegratedVulkan) {
		return gpus
	}
	return slices.DeleteFunc(slices.Clone(gpus), isIntegratedVulkan)
}

func isIntegratedVulkan(g ml.DeviceInfo) bool { return g.Integrated && g.Library == "Vulkan" }

// deviceRefusal names what was asked for, what this installation can drive at
// all, and what is actually present — the three things needed to tell a typo
// from a missing payload from a hidden device.
func deviceRefusal(pin *xollama.Devices, missing []string, gpus []ml.DeviceInfo) error {
	asked := pin.Backend
	if len(missing) > 0 {
		asked = fmt.Sprintf("%s device %s", pin.Backend, strings.Join(missing, ", "))
	} else {
		asked += " (any device)"
	}

	present := make([]string, 0, len(gpus))
	for _, g := range gpus {
		present = append(present, describeDevice(g))
	}
	presentText := "none"
	if len(present) > 0 {
		presentText = strings.Join(present, "; ")
	}

	msg := fmt.Sprintf("this model is pinned to %s, which is not available here. Backends this installation carries: %s. Devices present: %s",
		asked, strings.Join(builtBackends(), ", "), presentText)
	if pin.Backend == "Vulkan" || slices.Contains(pin.IDs, xollama.DeviceIntegrated) {
		if !envconfig.EnableIntegratedGPU(true) {
			msg += ". Integrated GPUs are hidden by OLLAMA_IGPU_ENABLE"
		} else if strings.EqualFold(strings.TrimSpace(envconfig.Var(engine.EnvSelector)), string(engine.KindLlamaCpp)) {
			msg += ". Under XOLLAMA_ENGINE=llamacpp an integrated Vulkan GPU is hidden as upstream hides it; set OLLAMA_IGPU_ENABLE=1"
		}
	}
	return fmt.Errorf("%s. Change it with `xollama tweak model`", msg)
}

func describeDevice(g ml.DeviceInfo) string {
	s := fmt.Sprintf("%s %s %s", g.Library, g.ID, g.Description)
	if g.PCIID != "" {
		s += " [" + g.PCIID + "]"
	}
	if g.Integrated {
		s += " integrated"
	}
	return s
}

// builtBackends lists the GPU backends the installed payload can drive,
// whichever engine carries them.
func builtBackends() []string {
	found := map[string]bool{"CPU": true}
	entries, _ := os.ReadDir(ml.LibOllamaPath)
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		switch {
		case e.IsDir() && strings.HasPrefix(name, "cuda"):
			found["CUDA"] = true
		case e.IsDir() && strings.HasPrefix(name, "rocm"):
			found["ROCm"] = true
		case e.IsDir() && strings.HasPrefix(name, "vulkan"):
			found["Vulkan"] = true
		case strings.HasPrefix(name, "ggml-cuda"):
			found["CUDA"] = true
		case strings.HasPrefix(name, "ggml-vulkan"):
			found["Vulkan"] = true
		}
	}
	var out []string
	for _, b := range xollama.ValidBackends() {
		if found[b] {
			out = append(out, b)
		}
	}
	return out
}
