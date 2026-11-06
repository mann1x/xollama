package llm

import (
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/ml"
)

// What an opencoti load takes, predicted from the GGUF before the engine
// starts.
//
// Upstream's PredictServerVRAM prices the cache as f16 keys and values on
// every block at the whole context. That is neither the type the load runs
// with nor the layers that hold a cache, and on eleven2go (2026-10-11) it said
// 26.6 GiB of cache for a load whose engine line reads "KV demand 4352 MiB":
// the scheduler evicted a model from one card while the other was empty.
//
// The terms below are the engine's own, checked against its log on two loads:
//
//	qwen35 27B, q8_0, 128000 cells:  KV 4250 MiB, drafter KV 500 MiB
//	gemma4 A4B, q8_0, 96000 cells:   KV 996 MiB, sliding-window KV 531 MiB
//
// and a hybrid model's recurrent state against three loads on the Mac mini
// (2026-10-13, "llama_memory_recurrent: size ="): qwen3.5 2B 38.53 MiB, 9B
// 100.50 MiB, and 1197.00 MiB for the 27B with its drafter at depth 3
//
// and each is read from the GGUF: which blocks keep a cache
// (full_attention_interval, shared_kv_layers, nextn_predict_layers), its
// heads per block (head_count_kv), the key and value widths (key_length,
// value_length and their _swa forms) and which blocks hold only the sliding
// window (sliding_window_pattern).

// opencotiComputeReserve stands for what the GGUF does not give: the compute
// buffers and the engine's runtime margin. The two loads above took 0.9 and
// 0.7 GiB beyond weights and caches.
const opencotiComputeReserve = format.GibiByte

// kvElemMilliBits is the size of one cache value in thousandths of a bit.
// An empty type is llama.cpp's default, f16; a name not listed here (the
// frozen turbo tiers) is priced as f16, the high side.
func kvElemMilliBits(t string) uint64 {
	switch t = strings.ToLower(strings.TrimSpace(t)); t {
	case "f32":
		return 32000
	case "q8_0":
		return 8500
	case "q6_0":
		return 6500
	case "q5_1":
		return 6000
	case "q5_0":
		return 5500
	case "q4_1":
		return 5000
	case "q4_0", "iq4_nl":
		return 4500
	case "kvarn2", "kvarn3", "kvarn4", "kvarn5", "kvarn6", "kvarn8":
		// b + 0.375 bits per value (opencoti docs/features/kvarn.md).
		return uint64(t[len(t)-1]-'0')*1000 + 375
	}
	return 16000
}

// kvLayout is the cache values one cell holds, summed over the blocks of
// each kind: the blocks that cache the whole context, the blocks that cache
// only the sliding window, and the NextN drafter's own.
type kvLayout struct {
	globalK, globalV uint64
	swaK, swaV       uint64
	draftK, draftV   uint64
	window           int
	// A hybrid model's recurrent blocks, and the values one row of their
	// state holds (llama_hparams::n_embd_r and n_embd_s).
	recurrent      int
	stateR, stateS uint64
}

func kvLayoutOf(f *gguf.Model) kvLayout {
	kv := f.KV()
	blocks := int(kv.BlockCount())
	nextn := int(kv.Uint("nextn_predict_layers"))
	layers := max(blocks-nextn, 0)
	cached := max(layers-int(kv.Uint("attention.shared_kv_layers")), 0)

	headsKV := kv.KeyValue("attention.head_count_kv").Uints()
	if len(headsKV) == 0 {
		for _, n := range kv.KeyValue("attention.head_count_kv").Ints() {
			headsKV = append(headsKV, uint64(max(n, 0)))
		}
	}
	scalarHeads := kv.Uint("attention.head_count_kv", kv.HeadCountMax())
	heads := func(i int) uint64 {
		if i < len(headsKV) {
			return headsKV[i]
		}
		return max(scalarHeads, 1)
	}

	var headDim uint64
	if kv.HeadCountMax() > 0 {
		headDim = kv.EmbeddingLength() / kv.HeadCountMax()
	}
	klen := kv.Uint("attention.key_length", headDim)
	vlen := kv.Uint("attention.value_length", headDim)
	klenSWA := kv.Uint("attention.key_length_swa", klen)
	vlenSWA := kv.Uint("attention.value_length_swa", vlen)

	interval := int(kv.Uint("full_attention_interval"))
	out := kvLayout{window: int(kv.Uint("attention.sliding_window"))}
	// Which blocks hold only the window is the GGUF's array. An architecture
	// that keeps the pattern in llama.cpp's own tables has every block priced
	// at the whole context: high, never low.
	pattern := kv.KeyValue("attention.sliding_window_pattern").Bools()
	for i := range cached {
		// A hybrid model's recurrent blocks keep state, not a cache.
		if interval > 0 && (i+1)%interval != 0 {
			continue
		}
		if out.window > 0 && i < len(pattern) && pattern[i] {
			out.swaK += heads(i) * klenSWA
			out.swaV += heads(i) * vlenSWA
			continue
		}
		out.globalK += heads(i) * klen
		out.globalV += heads(i) * vlen
	}
	if dState, dInner := kv.Uint("ssm.state_size"), kv.Uint("ssm.inner_size"); interval > 0 && dState > 0 && dInner > 0 {
		for i := range layers {
			if (i+1)%interval != 0 {
				out.recurrent++
			}
		}
		if conv := kv.Uint("ssm.conv_kernel"); conv > 1 {
			out.stateR = (conv - 1) * (dInner + 2*kv.Uint("ssm.group_count")*dState)
		}
		out.stateS = dState * dInner
	}
	out.draftK = uint64(nextn) * max(scalarHeads, 1) * klen
	out.draftV = uint64(nextn) * max(scalarHeads, 1) * vlen
	return out
}

// OpencotiVRAM is a load's predicted memory by part.
type OpencotiVRAM struct {
	Weights uint64 // the model file
	KV      uint64 // the cache at the whole context, in the load's types
	SWA     uint64 // the sliding-window cache, at the slot ceiling
	Drafter uint64 // the NextN drafter's cache, f16
	State   uint64 // a hybrid model's recurrent state, bf16
	Reserve uint64 // compute buffers and runtime margin
}

func (v OpencotiVRAM) Total() uint64 {
	return v.Weights + v.KV + v.SWA + v.Drafter + v.State + v.Reserve
}

// PredictOpencotiVRAM predicts what opencoti takes for this load. numCtx is
// the whole pool (num_ctx x num_parallel), numCtxSeq one sequence's context,
// numBatch the ubatch and numParallel the live slot count, as the scheduler
// settled them; draftDepth is the draft length the launch passes
// (--spec-draft-n-max), 0 for none.
func PredictOpencotiVRAM(modelPath string, f *gguf.Model, cfg LlamaServerConfig, numCtx, numCtxSeq, numBatch, numParallel, draftDepth int) OpencotiVRAM {
	out := OpencotiVRAM{Weights: modelFileSize(modelPath, f), Reserve: opencotiComputeReserve}
	if f == nil || numCtx <= 0 {
		return out
	}
	types := resolveKVCacheTypesOn(cfg, strings.ToLower(envconfig.KvCacheType()), false)
	kBits, vBits := kvElemMilliBits(types.K), kvElemMilliBits(types.V)
	kBitsSWA, vBitsSWA := kBits, vBits
	if types.KSWA != "" {
		kBitsSWA = kvElemMilliBits(types.KSWA)
	}
	if types.VSWA != "" {
		vBitsSWA = kvElemMilliBits(types.VSWA)
	}
	layout := kvLayoutOf(f)
	bytes := func(values, milliBits uint64, cells int) uint64 {
		return values * milliBits * uint64(max(cells, 0)) / 8000
	}

	out.KV = bytes(layout.globalK, kBits, numCtx) + bytes(layout.globalV, vBits, numCtx)

	if layout.window > 0 && layout.swaK+layout.swaV > 0 {
		// The window cache is sized per sequence, for every sequence the
		// engine may admit: the same count PredictServerSlotVRAM plans for.
		plan := resolveSlotPlan(cfg, numParallel, cfg.SingleSequenceOnly)
		seqs := max(plan.concurrency(), councilLive(cfg, numParallel, true)) + resolvePoolCount(cfg) + max(cfg.CouncilPools, 0) + clientPoolSeats(cfg)
		windows := seqs
		if plan.SWASeqBudget > 0 {
			windows = min(plan.SWASeqBudget, seqs)
		}
		cells := slidingWindowCells(layout.window, numBatch, numCtxSeq, 1) * plan.Live
		if seqs > plan.Live || windows < seqs {
			cells = slidingWindowCells(layout.window, numBatch, numCtxSeq*plan.Live, windows)
		}
		out.SWA = bytes(layout.swaK, kBitsSWA, cells) + bytes(layout.swaV, vBitsSWA, cells)
	}

	if !cfg.DraftOff {
		out.Drafter = bytes(layout.draftK+layout.draftV, 16000, numCtx)
	}

	if layout.recurrent > 0 {
		// One row per sequence the context holds, and with a built-in
		// drafter one more per draft step: the rollback ring. Priced in
		// full: Metal allocates it so, a card commits it as it is used.
		plan := resolveSlotPlan(cfg, numParallel, cfg.SingleSequenceOnly)
		rows := max(plan.concurrency(), councilLive(cfg, numParallel, true)) + resolvePoolCount(cfg) + max(cfg.CouncilPools, 0) + clientPoolSeats(cfg)
		if layout.draftK > 0 && !cfg.DraftOff {
			rows *= 1 + max(draftDepth, 0)
		}
		out.State = bytes(uint64(layout.recurrent)*(layout.stateR+layout.stateS), 16000, rows)
	}
	return out
}

// EveryGroupOnOpencoti reports whether opencoti serves the load whichever
// backend the placement picks: the prediction is made before that choice, and
// it may only leave upstream's when no stock llama.cpp load can come of it.
func EveryGroupOnOpencoti(cfg LlamaServerConfig, gpus []ml.DeviceInfo) bool {
	groups := ml.ByLibrary(gpus)
	if len(groups) == 0 {
		return false
	}
	for _, group := range groups {
		if !wouldUseOpencoti(cfg, group) {
			return false
		}
	}
	return true
}
