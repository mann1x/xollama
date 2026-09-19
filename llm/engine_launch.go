package llm

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ollama/ollama/envconfig"
)

// xollama-hook: launch-config
//
// One place that answers "what launch settings does this load actually get".
//
// Upstream has a single server-wide OLLAMA_KV_CACHE_TYPE, written into both
// halves of the cache. That is two limitations in one string: it cannot differ
// per model, and it cannot differ between keys and values even though they are
// not equally sensitive to quantisation. This resolver adds both without moving
// the upstream setting -- it stays the default that everything else overrides.
//
// Precedence, most specific first:
//
//  1. the model's own xollama.json
//  2. the XOLLAMA_* environment variables, per half
//  3. OLLAMA_KV_CACHE_TYPE, upstream's server-wide setting
//  4. unset, which leaves the engine's own default (f16)
//
// See docs/xollama/kv-cache.mdx for the user-facing description.

// kvCacheTypes is the resolved cache type for each half of the cache, plus the
// short-window ring a sliding-window model keeps alongside it.
type kvCacheTypes struct {
	K    string
	V    string
	KSWA string
	VSWA string
}

// stockCacheTypes is what upstream llama.cpp's own parser accepts
// (common/arg.cpp, kv_cache_types). Anything outside this list is an engine
// extension -- opencoti's kvarnN widths and the frozen turbo tiers, q6_0 -- and
// asking for one on stock llama.cpp has to be refused rather than passed on to
// fail as an unrecognised argument deep in the engine's own parser.
var stockCacheTypes = []string{
	"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1",
}

// resolveKVCacheTypes applies the precedence above. base is what upstream
// resolved from OLLAMA_KV_CACHE_TYPE, so an unmodified server keeps behaving
// exactly as it did.
func resolveKVCacheTypes(cfg LlamaServerConfig, base string) kvCacheTypes {
	out := kvCacheTypes{K: base, V: base}

	if v := strings.ToLower(strings.TrimSpace(envconfig.KCacheType())); v != "" {
		out.K = v
	}
	if v := strings.ToLower(strings.TrimSpace(envconfig.VCacheType())); v != "" {
		out.V = v
	}
	if v := strings.ToLower(strings.TrimSpace(envconfig.KCacheTypeSWA())); v != "" {
		out.KSWA = v
	}
	if v := strings.ToLower(strings.TrimSpace(envconfig.VCacheTypeSWA())); v != "" {
		out.VSWA = v
	}

	if cfg.Xollama != nil && cfg.Xollama.KV != nil {
		kv := cfg.Xollama.KV
		if kv.K != "" {
			out.K = strings.ToLower(kv.K)
		}
		if kv.V != "" {
			out.V = strings.ToLower(kv.V)
		}
		if kv.KSWA != "" {
			out.KSWA = strings.ToLower(kv.KSWA)
		}
		if kv.VSWA != "" {
			out.VSWA = strings.ToLower(kv.VSWA)
		}
	}

	return out
}

// isStockCacheType reports whether upstream llama.cpp's own parser knows this
// type name.
func isStockCacheType(t string) bool {
	return t == "" || slices.Contains(stockCacheTypes, t)
}

// requiresEngineExtension reports whether these types need an engine beyond
// stock llama.cpp, and names the first reason, so a refusal can say which
// setting caused it rather than "something here is unsupported".
func (kv kvCacheTypes) requiresEngineExtension() string {
	for _, f := range []struct{ what, val string }{
		{"kv.k / XOLLAMA_K_CACHE_TYPE", kv.K},
		{"kv.v / XOLLAMA_V_CACHE_TYPE", kv.V},
	} {
		if !isStockCacheType(f.val) {
			return fmt.Sprintf("%s = %q is not a cache type stock llama.cpp accepts", f.what, f.val)
		}
	}
	// The ring is not a type stock llama.cpp quantises separately -- it has no
	// flag for it at all -- so any value here needs the extension regardless of
	// whether the type itself is a stock one.
	if kv.KSWA != "" || kv.VSWA != "" {
		return "kv.k_swa / kv.v_swa need an engine with a separate sliding-window cache; stock llama.cpp has no such flag"
	}
	return ""
}

// appendKVCacheArgs writes the global cache types, which both engines accept.
// The ring is not written here: it is engine-specific and is added after the
// engine has actually been chosen, the same way --spec-type is retargeted.
func appendKVCacheArgs(params []string, kv kvCacheTypes) []string {
	if kv.K != "" {
		params = append(params, "--cache-type-k", kv.K)
	}
	if kv.V != "" {
		params = append(params, "--cache-type-v", kv.V)
	}
	return params
}

// appendKVCacheRingArgs adds the sliding-window ring types once the engine is
// known. On anything but opencoti it adds nothing -- and cannot be reached with
// a ring set, because such a load is refused before it starts.
func appendKVCacheRingArgs(args []string, kv kvCacheTypes, usedOpencoti bool) []string {
	if !usedOpencoti || kv.KSWA == "" || kv.VSWA == "" {
		return args
	}
	return append(args, "--cache-type-k-swa", kv.KSWA, "--cache-type-v-swa", kv.VSWA)
}
