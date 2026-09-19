package llm

import (
	"fmt"
	"slices"
	"strconv"
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

// slotPlan is how many requests this load may serve at once, and how the engine
// is allowed to get there.
//
// Upstream reserves OLLAMA_NUM_PARALLEL slots' worth of KV cells when the model
// loads, used or not, and queues everything past that number. So the number has
// to be guessed in advance: too low leaves the card idle, too high spends on
// slots nobody opens the memory the model itself needed.
//
// Dynamic slots remove the guess. The cache becomes one shared pool rather than
// a fixed per-slot split (--kv-unified), Max slots are allocated but parked, and
// the engine admits another only while the decode rate and the free VRAM still
// allow it (--max-parallel and friends). Nothing is reserved for a slot nobody
// is using.
//
// The total cell count is deliberately NOT changed: ollama sized -c as
// num_ctx x num_parallel and planned its memory estimate against that, so the
// same cells are allocated either way. What changes is whether one long
// conversation may use all of them.
type slotPlan struct {
	// Dynamic is false when this is upstream's fixed split, in which case
	// nothing below applies and no argument is added.
	Dynamic bool
	// Live is where the engine starts, which stays ollama's own num_parallel.
	Live int
	// Max is the ceiling the engine may grow to.
	Max int
	// TPSFloor and VRAMReserveMiB are the two brakes on admitting another
	// slot. Zero leaves each to the engine's own default.
	TPSFloor       float64
	VRAMReserveMiB int
}

// defaultMaxParallel is the ceiling when nobody named one.
//
// Four, because the point of a shared pool is that an unused slot costs
// nothing, so the ceiling should be high enough to be useful on a busy server
// and low enough that the engine's own admission brakes are what actually
// decide -- not a number picked here.
const defaultMaxParallel = 4

// resolveSlotPlan applies the precedence the documentation promises: the
// model's xollama.json, then the environment, then the default.
//
// numParallel is what ollama decided, including the cases where it forced 1 --
// an embedding model, or an architecture that is unsafe above one sequence. A
// forced 1 is a correctness decision and the ceiling must not climb back over
// it, so it is taken as both the floor and, when forced, the ceiling.
func resolveSlotPlan(cfg LlamaServerConfig, numParallel int, forcedSingle bool) slotPlan {
	plan := slotPlan{
		Dynamic:        envconfig.DynamicSlots(),
		Live:           max(numParallel, 1),
		Max:            int(envconfig.MaxParallel()),
		VRAMReserveMiB: int(envconfig.SlotsVRAMReserve()),
	}
	if v := strings.TrimSpace(envconfig.SlotsTPSFloor()); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			plan.TPSFloor = f
		}
	}

	if cfg.Xollama != nil && cfg.Xollama.Slots != nil {
		s := cfg.Xollama.Slots
		if s.Dynamic != nil {
			plan.Dynamic = *s.Dynamic
		}
		if s.Max > 0 {
			plan.Max = s.Max
		}
		if s.TPSFloor > 0 {
			plan.TPSFloor = s.TPSFloor
		}
		if s.VRAMReserveMiB > 0 {
			plan.VRAMReserveMiB = s.VRAMReserveMiB
		}
	}

	// An architecture ollama refuses to run above one sequence must not be
	// grown past one by this. That deny-list exists because those models give
	// wrong answers with several sequences in flight, which is not a trade
	// anyone gets to make here.
	if forcedSingle {
		plan.Dynamic = false
	}
	if !plan.Dynamic {
		return slotPlan{Live: plan.Live}
	}

	if plan.Max <= 0 {
		plan.Max = defaultMaxParallel
	}
	// A ceiling below where the engine starts is not a ceiling.
	plan.Max = max(plan.Max, plan.Live)
	return plan
}

// appendSlotArgs adds the dynamic-slot arguments, once the engine is known.
//
// Both flags are gated on opencoti, for different reasons. --max-parallel is
// its own; --kv-unified exists upstream too, but it changes what -c means, and
// switching that on for a stock load would move the off path away from
// upstream's for no gain, since nothing there can park or admit a slot.
func appendSlotArgs(args []string, plan slotPlan, usedOpencoti bool) []string {
	if !plan.Dynamic || !usedOpencoti || plan.Max <= plan.Live {
		return args
	}
	// --max-parallel requires the shared pool: parked slots have nothing to be
	// admitted into when every slot owns a fixed share of the cells.
	args = append(args, "--kv-unified", "--max-parallel", strconv.Itoa(plan.Max))
	if plan.TPSFloor > 0 {
		args = append(args, "--max-parallel-tps-floor", strconv.FormatFloat(plan.TPSFloor, 'g', -1, 64))
	}
	if plan.VRAMReserveMiB > 0 {
		args = append(args, "--max-parallel-vram-reserve", strconv.Itoa(plan.VRAMReserveMiB))
	}
	return args
}

// concurrency is how many requests may be in flight at once.
//
// This is the half that makes dynamic slots visible. ollama gates concurrency
// on its own semaphore, sized to num_parallel, so an engine that had grown to
// four live slots would still be fed one request at a time and the feature
// would do nothing at all. When the plan is dynamic the semaphore is sized to
// the ceiling instead, and the engine's admission control -- not a number
// guessed at load time -- decides what actually runs.
func (p slotPlan) concurrency() int {
	if p.Dynamic && p.Max > p.Live {
		return p.Max
	}
	return max(p.Live, 1)
}
