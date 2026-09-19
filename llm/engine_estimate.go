package llm

import (
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// xollama-hook: launch-config
//
// What this file adds to ollama's memory estimate, and why it has to.
//
// ollama predicts a load's VRAM once, before the process exists:
// PredictServerVRAM sizes the cache as num_ctx x num_parallel cells and adds
// the weights. That is a complete answer as long as num_parallel is fixed,
// because then the runner allocates everything it will ever hold at startup,
// and the request that spawned it has already paid for the last one.
//
// Dynamic slots break that assumption. The runner starts at one live slot and
// the engine may admit more later, so a prediction made for the first request
// stops describing the process the moment a second slot opens. That is the gap
// this file closes: the surcharge is computed against the ceiling, not the live
// count, so the estimate is true for the runner's whole life.
//
// Most of the cache does not move. Under --kv-unified the cells are one shared
// pool, and llama.cpp sizes that pool the same either way --
//
//	n_ctx_seq = unified ? n_ctx : n_ctx / n_seq_max      // llama-context.cpp
//	KV tensor = (n_embd_k_gqa, n_ctx_seq, unified ? 1 : n_seq_max)
//
// -- so the products are equal and four slots sharing a pool cost what one slot
// cost. That is why the allocation at startup is so nearly the single-slot
// figure. One part is not equal, and it is the part below.
//
// A sliding-window model keeps a second, short cache beside the global one, and
// llama.cpp sizes that one per sequence even when the global cache is shared:
//
//	size_swa = GGML_PAD(min(n_ctx_seq, n_swa*(unified ? n_seq_max : 1) + n_ubatch), 256)
//	                                        -- src/llama-kv-cache-iswa.cpp:73
//
// A ceiling of four multiplies the window term by four. On Gemma-3 and its
// relatives that is the whole difference between "almost the same as one slot"
// and the number the card actually needs, and a prediction that misses it will
// place a second model into memory this one is going to grow into.

// kvCellPad is llama.cpp's cell-count padding for the sliding-window cache,
// the GGML_PAD(..., 256) in the line quoted above.
const kvCellPad = 256

// slidingWindowCells mirrors that line exactly, in cells, for one cache.
//
// The order matters and is not the intuitive one: llama.cpp clamps to the
// context first and pads afterwards, so the result can sit up to 255 cells
// above n_ctx_seq. Padding first would round the clamp away and quietly
// under-count by most of a page.
func slidingWindowCells(window, numBatch, numCtxSeq, seqs int) int {
	if window <= 0 || numCtxSeq <= 0 {
		return 0
	}
	cells := min(numCtxSeq, window*max(seqs, 1)+max(numBatch, 0))
	return (cells + kvCellPad - 1) / kvCellPad * kvCellPad
}

// kvCellBytes is what one cache cell costs across every layer, in the same
// shape PredictServerVRAM uses for the global cache: K and V, f16, all layers.
//
// Every layer is counted even though only the sliding-window layers hold the
// short cache, because which layers those are is decided by the architecture in
// llama.cpp's own tables rather than by anything in the GGUF. Counting them all
// overestimates, which is the direction this prediction is documented to err
// in, and the result stays bounded: the window term is clamped to n_ctx_seq, so
// the surcharge can never exceed one extra context's worth of cells.
func kvCellBytes(f *gguf.Model) uint64 {
	layers := f.KV().BlockCount()
	kvHeads := f.KV().HeadCountKVMin()
	if kvHeads == 0 {
		kvHeads = 1
	}
	var headDim uint64
	if f.KV().HeadCountMax() > 0 {
		headDim = f.KV().EmbeddingLength() / f.KV().HeadCountMax()
	}
	return 2 * layers * kvHeads * headDim * 2
}

// PredictServerSlotVRAM returns the VRAM this load allocates over and above
// what its live slot count alone would need, because its ceiling is higher.
//
// It is zero unless all of these hold, and each one is load-bearing:
//
//   - the engine that will serve the load is opencoti. --kv-unified and
//     --max-parallel are only ever passed there (see appendSlotArgs), so a
//     stock llama.cpp load has to predict exactly what upstream predicts.
//   - the resolved plan really does raise the ceiling above the live count.
//   - the model declares a sliding window. Without one there is only the
//     shared pool, whose size does not depend on how many sequences draw on it.
//
// numCtxSeq is the per-sequence context ollama settled on, numBatch the ubatch
// it will pass, and numParallel the live slot count it decided to start with.
func PredictServerSlotVRAM(f *gguf.Model, cfg LlamaServerConfig, gpus []ml.DeviceInfo, numCtxSeq, numBatch, numParallel int) uint64 {
	if f == nil || numCtxSeq <= 0 {
		return 0
	}

	plan := resolveSlotPlan(cfg, numParallel, cfg.SingleSequenceOnly)
	seqs := plan.concurrency()
	if seqs <= plan.Live || !wouldUseOpencoti(cfg, gpus) {
		return 0
	}

	window := int(f.KV().Uint("attention.sliding_window"))

	// The two sides are the two layouts, each priced the way llama.cpp prices
	// it. Without the ceiling ollama gets a per-stream short cache in each of
	// its Live streams; with it, one short cache covering the whole pool, whose
	// context is the pool's -- num_ctx x num_parallel, which is what -c says.
	split := slidingWindowCells(window, numBatch, numCtxSeq, 1) * plan.Live
	unified := slidingWindowCells(window, numBatch, numCtxSeq*plan.Live, seqs)

	extra := unified - split
	if extra <= 0 {
		return 0
	}
	return uint64(extra) * kvCellBytes(f)
}

// wouldUseOpencoti reports whether this load is going to be served by opencoti,
// without touching the filesystem.
//
// It is engine.Resolve's decision and nothing more. On a build where the
// artifact turns out to be missing, the launch falls back to stock and this
// will have predicted a little high -- the safe direction, and the only one
// available to a prediction made before the process exists. What it must never
// do is answer yes for a load pinned to llama.cpp: with XOLLAMA_ENGINE=llamacpp
// every number here has to be upstream's, to the byte.
func wouldUseOpencoti(cfg LlamaServerConfig, gpus []ml.DeviceInfo) bool {
	if cfg.enginePin() == xollama.EngineLlamaCpp {
		return false
	}
	return engine.Resolve(engine.Host(), engineDevices(gpus), envconfig.Var(engine.EnvSelector)).Kind == engine.KindOpencoti
}
