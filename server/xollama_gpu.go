package server

import (
	"bytes"
	"cmp"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// The server's GPU policy (plans/system-settings.md), set with
// `xollama tweak server gpu` and kept in the settings file's gpu section.
// With no policy every function here returns what it was given, so the
// scheduler behaves exactly as upstream's.

const settingsGPU = "gpu"

func init() {
	llm.DeviceEnvs = gpuPolicyEnvs
}

// serverGPU is the policy, nil when there is none or it no longer validates.
func serverGPU() *xollama.GPUSettings {
	raw := envconfig.SettingsSection(settingsGPU)
	if len(raw) == 0 {
		return nil
	}
	var g xollama.GPUSettings
	err := jsonUnmarshalStrict(raw, &g)
	if err == nil {
		err = g.Validate()
	}
	if err != nil {
		slog.Warn("the server's GPU settings are ignored", "error", err)
		return nil
	}
	return &g
}

// applyGPUPolicy narrows and orders the GPUs a load may use.
//
// A model with its own device pin keeps exactly the devices it pinned: the
// pin is the model's, and it wins. Otherwise disabled GPUs are left out, and
// a GPU reachable through several backends keeps only its chosen one. Either
// way the GPUs come back highest priority first, which is the order the
// engine's visible-devices list fills.
func applyGPUPolicy(cfg *xollama.Config, gpus []ml.DeviceInfo) []ml.DeviceInfo {
	g := serverGPU()
	if g.IsZero() || len(gpus) == 0 {
		return gpus
	}
	pinned := cfg != nil && !cfg.Devices.IsZero()
	out := make([]ml.DeviceInfo, 0, len(gpus))
	for _, d := range gpus {
		if !pinned {
			if s, ok := policyFor(g, d, gpus); ok && s.Disabled {
				continue
			}
			if !preferredBackend(g, d, gpus) {
				continue
			}
		}
		out = append(out, d)
	}
	slices.SortStableFunc(out, func(a, b ml.DeviceInfo) int {
		return cmp.Compare(gpuPriority(g, b, gpus), gpuPriority(g, a, gpus))
	})
	return out
}

// preferredBackend says whether d is the entry to keep for its GPU: true
// when the GPU appears under one backend only, or under its chosen one.
func preferredBackend(g *xollama.GPUSettings, d ml.DeviceInfo, all []ml.DeviceInfo) bool {
	s, ok := policyFor(g, d, all)
	if !ok || s.Backend == "" || d.PCIID == "" {
		// No backend chosen for this GPU: upstream's (backend-copies).
		return upstreamPreferred(d, all)
	}
	want := xollama.CanonicalBackend(s.Backend)
	if d.Library == want {
		return true
	}
	// Keep a GPU on another backend only when its chosen one is not there.
	return !slices.ContainsFunc(all, func(o ml.DeviceInfo) bool {
		return o.Library == want && samePCI(o.PCIID, d.PCIID)
	})
}

func samePCI(a, b string) bool {
	x, ok1 := xollama.CanonicalPCIID(a)
	y, ok2 := xollama.CanonicalPCIID(b)
	return ok1 && ok2 && x == y
}

// policyFor is the policy's entry for d: by PCI ID, else by name and d's
// place among the GPUs of that name in all. With no all that place is not
// known and d is taken for the first of its name.
func policyFor(g *xollama.GPUSettings, d ml.DeviceInfo, all []ml.DeviceInfo) (xollama.GPUDevice, bool) {
	return g.Lookup(d.PCIID, deviceName(d), nameOrdinal(d, all))
}

func gpuPriority(g *xollama.GPUSettings, d ml.DeviceInfo, all []ml.DeviceInfo) int {
	s, _ := policyFor(g, d, all)
	return s.Priority
}

// schedSpread is OLLAMA_SCHED_SPREAD, unless the GPU policy states a split.
func schedSpread() bool {
	switch serverGPU().Split() {
	case xollama.SplitSpread:
		return true
	case xollama.SplitSingle, xollama.SplitAuto:
		return false
	}
	return envconfig.SchedSpread()
}

// neverSplit is the "single" split policy.
func neverSplit() bool { return serverGPU().Split() == xollama.SplitSingle }

// priorityOrder compares two GPUs by priority: positive when a comes first.
func priorityOrder(a, b ml.DeviceInfo) int {
	g := serverGPU()
	if g.IsZero() {
		return 0
	}
	// The scheduler compares two GPUs here without the list they came from:
	// two cards of one name and no PCI ID are both read as the first.
	return cmp.Compare(gpuPriority(g, a, nil), gpuPriority(g, b, nil))
}

// gpuPolicyEnvs are the engine variables the policy sets for a launch on
// gpus: the split mode, and the forced link speeds.
//
// OPENCOTI_LINK_GBPS carries one entry per GPU of the load that has a forced
// speed, keyed by PCI ID ("0000:01:00.0=25.6,0000:02:00.0=12.8", opencoti
// patch 0512). A GPU without one is left out, so the engine probes it. The
// engine plans each KV cache with the slowest of the GPUs holding its
// layers, so a forced speed never makes it plan faster than a slower GPU it
// uses.
func gpuPolicyEnvs(gpus []ml.DeviceInfo) map[string]string {
	g := serverGPU()
	if g.IsZero() {
		return nil
	}
	env := map[string]string{}
	if g.SplitMode != "" && len(gpus) > 1 {
		env["LLAMA_ARG_SPLIT_MODE"] = g.SplitMode
	}
	var links []string
	for _, d := range gpus {
		pci, ok := xollama.CanonicalPCIID(d.PCIID)
		if !ok {
			continue
		}
		s, ok := g.Device(pci)
		if !ok || s.LinkGBps <= 0 {
			continue
		}
		entry := pci + "=" + strconv.FormatFloat(s.LinkGBps, 'f', -1, 64)
		if !slices.Contains(links, entry) {
			links = append(links, entry)
		}
	}
	if len(links) > 0 {
		env["OPENCOTI_LINK_GBPS"] = strings.Join(links, ",")
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// jsonUnmarshalStrict refuses unknown fields: a GPU setting misspelled by
// hand must not be silently ignored.
func jsonUnmarshalStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
