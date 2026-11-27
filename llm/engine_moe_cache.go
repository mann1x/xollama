package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/types/xollama"
)

// The MoE expert cache on opencoti (c14 r19; types/xollama/moe_cache.go).
// Nothing stated adds nothing: the engine's own default (the automatic cache
// under --fit on and --moe-strategy auto, one GPU) applies.

// appendMoECacheArgs adds the flags of a stated cache: a size is
// --moe-cache-mib (the total; it turns the automatic sizing off), a share is
// --moe-cache-cpu-share. Called from appendEnginePolicyArgs, which runs only
// for opencoti.
func appendMoECacheArgs(args []string, f *xollama.Fit) []string {
	if f == nil {
		return args
	}
	if n := xollama.MoECacheMiB(f.MoECache); n > 0 {
		args = append(args, "--moe-cache-mib", strconv.Itoa(n))
	}
	if s := f.MoECacheCPUShare; s != nil && f.MoECache != xollama.MoECacheOff {
		args = append(args, "--moe-cache-cpu-share", strconv.FormatFloat(*s, 'f', -1, 64))
	}
	return args
}

// Environment variables the engine reads for the cache (opencoti
// docs/features/moe_cache.md); neither has a flag.
const (
	envMoECacheAuto    = "OPENCOTI_MOE_CACHE_AUTO"
	envMoECacheProfile = "OPENCOTI_MOE_CACHE_PROFILE"
)

// withMoECacheEnvs adds what a stated cache needs in the engine's
// environment: off is OPENCOTI_MOE_CACHE_AUTO=0, a stated auto is =1 (the
// model's setting beats an operator's 0, as every model setting beats the
// environment), and a warm start turned off is OPENCOTI_MOE_CACHE_PROFILE=0.
// A warm start turned on removes nothing an operator set. envs may be shared,
// so it is copied before a change.
func withMoECacheEnvs(envs map[string]string, x *xollama.Config) map[string]string {
	if !x.StatesMoECache() {
		return envs
	}
	set := map[string]string{}
	switch x.Fit.MoECache {
	case xollama.MoECacheOff:
		set[envMoECacheAuto] = "0"
	case xollama.MoECacheAuto:
		set[envMoECacheAuto] = "1"
	}
	if w := x.Fit.MoECacheWarmStart; w != nil && !*w {
		set[envMoECacheProfile] = "0"
	}
	if len(set) == 0 {
		return envs
	}
	out := cloneStringMap(envs)
	if out == nil {
		out = map[string]string{}
	}
	for k, v := range set {
		out[k] = v
	}
	return out
}

// MoECacheState is the engine's record of the MoE cache on each device of
// this load, or nil where it keeps none: stock llama.cpp, an opencoti without
// moe_cache_state_v1, an engine that does not answer, or a load whose devices
// carry no record.
func (s *llamaServerRunner) MoECacheState(ctx context.Context) []api.MoECacheDevice {
	if !s.usedOpencoti || s.port == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, drafterStateTimeout)
	defer cancel()
	status, out, err := s.engineRequest(ctx, http.MethodGet, "/props", nil)
	if err != nil || status != http.StatusOK {
		return nil
	}
	return parseMoECacheState(out)
}

// parseMoECacheState reads opencoti.devices[].moe_cache from a /props body.
func parseMoECacheState(props []byte) []api.MoECacheDevice {
	var p struct {
		Features []string `json:"features"`
		Opencoti struct {
			Devices []struct {
				Name     string              `json:"name"`
				PCI      string              `json:"pci"`
				MoECache *api.MoECacheDevice `json:"moe_cache"`
			} `json:"devices"`
		} `json:"opencoti"`
	}
	if json.Unmarshal(props, &p) != nil || !slices.Contains(p.Features, engine.FeatureMoECacheState) {
		return nil
	}
	var res []api.MoECacheDevice
	for _, d := range p.Opencoti.Devices {
		if d.MoECache == nil {
			continue
		}
		m := *d.MoECache
		m.Device, m.PCI = d.Name, d.PCI
		res = append(res, m)
	}
	return res
}
