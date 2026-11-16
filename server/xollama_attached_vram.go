package server

import (
	"os"
	"sync"

	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

// The opencoti prediction (llm.PredictOpencotiVRAM) prices the model file,
// its caches and one compute reserve. A load also carries what is attached to
// the model: an assistant drafter and a vision projector. Left out, a model
// with both was predicted at 14.9 GiB and took 20.5 GiB, and the automatic
// num_batch, deciding on the low number, doubled the batch of the lighter
// quant: an IQ2 (9.0 GB of weights) took as much of the card as the Q4_K_M
// (12.6 GB), because 2048 "fitted" (eleven2go, RTX 3090, num_ctx 256000,
// mail #2308, 2026-11-16).

// An assistant head's draft context, measured there (26B-A4B head, 2816
// wide, num_ctx 256000): 2911 MiB at num_batch 1024 and 5010 MiB at 2048, so
// about 862 MiB plus 2.05 MiB per batch row. It is sized like the target's
// compute buffer (2358 and 4479 MiB on the same loads): by the context and by
// the batch. On c13 at num_ctx 32768 the same head's context was 212 MiB
// (c41/run4), which this scaling over-states slightly: the safe side.
const (
	draftHeadMeasuredCtx   = 262144
	draftHeadMeasuredWidth = 2816
	draftHeadBase          = 862 * format.MebiByte
	draftHeadPerRow        = 2150 * format.KibiByte // 2.05 MiB
)

type drafterInfo struct {
	size      uint64
	assistant bool   // a gemma4-assistant head, built against the target's context
	width     uint64 // its embedding_length_out
}

var drafterInfos sync.Map // path -> drafterInfo

func drafterOf(path string) drafterInfo {
	if path == "" {
		return drafterInfo{}
	}
	if v, ok := drafterInfos.Load(path); ok {
		return v.(drafterInfo)
	}
	var d drafterInfo
	if st, err := os.Stat(path); err == nil {
		d.size = uint64(st.Size())
	}
	if f, err := llm.LoadModel(path, 0); err == nil {
		d.assistant = f.KV().Architecture() == "gemma4-assistant"
		d.width = f.KV().Uint("embedding_length_out")
	}
	drafterInfos.Store(path, d)
	return d
}

// drafterContextVRAM is an attached drafter's context at this num_ctx and
// num_batch. A head of another width is scaled by its width; a drafter that
// is not an assistant head (a model of its own) is priced as one more compute
// reserve plus the batch's, as the target's compute is.
func drafterContextVRAM(d drafterInfo, numCtx, numBatch int) uint64 {
	if numCtx <= 0 {
		return 0
	}
	batch := max(numBatch, llamaServerGenerationBatchDefault)
	if !d.assistant {
		return format.GibiByte + llm.LlamaServerBatchMemory(batch)
	}
	width := d.width
	if width == 0 {
		width = draftHeadMeasuredWidth
	}
	at := draftHeadBase + draftHeadPerRow*uint64(batch)
	return at * uint64(numCtx) / draftHeadMeasuredCtx * width / draftHeadMeasuredWidth
}

// attachedVRAM is what the opencoti prediction leaves out at this num_batch:
// the drafter's file and context, and the projectors' files. opencoti loads a
// projector at the first image; it is counted at load so that image has room
// on the card instead of going to the CPU.
func attachedVRAM(m *Model, numCtx, numBatch int) uint64 {
	if m == nil {
		return 0
	}
	var total uint64
	if m.DraftPath != "" && !llamaServerConfigForModel(m).DraftOff {
		d := drafterOf(m.DraftPath)
		total += d.size + drafterContextVRAM(d, numCtx, numBatch)
	}
	for _, p := range m.ProjectorPaths {
		if st, err := os.Stat(p); err == nil {
			total += uint64(st.Size())
		}
	}
	return total
}

// drafterBatchVRAM is what a drafter's context grows by between the batch
// the prediction was made at and the batch the load gets: the drafter's
// share of the batch cost, which upstream's batch choice counts for the
// target alone.
func drafterBatchVRAM(m *Model, numCtx, predictedAt, numBatch int, gpus []ml.DeviceInfo) uint64 {
	if m == nil || m.DraftPath == "" || numBatch <= predictedAt {
		return 0
	}
	if cfg := llamaServerConfigForModel(m); cfg.DraftOff || !llm.EveryGroupOnOpencoti(cfg, gpus) {
		return 0
	}
	d := drafterOf(m.DraftPath)
	return drafterContextVRAM(d, numCtx, numBatch) - drafterContextVRAM(d, numCtx, predictedAt)
}

// attachedBatch is the num_batch predictVRAM prices the attachments at: an
// automatic batch is decided after the prediction, from the smallest one,
// and drafterAwareBatch adds the drafter's growth from there.
func attachedBatch(req *LlmRequest, numBatch int) int {
	if req.numBatchAuto {
		return llamaServerGenerationBatchDefault
	}
	return numBatch
}

// drafterAwareBatch steps an automatic num_batch down until the batch fits
// with the drafter's share counted too (mail #2308). It runs after upstream's
// choice, only for an automatic batch on an opencoti load with a drafter.
func (req *LlmRequest) drafterAwareBatch(numCtx int, predicted, available uint64, gpus []ml.DeviceInfo) {
	if !req.numBatchAuto || req.model == nil || req.model.DraftPath == "" ||
		!llm.EveryGroupOnOpencoti(llamaServerConfigForModel(req.model), gpus) {
		return
	}
	at := llamaServerGenerationBatchDefault
	for req.opts.NumBatch > at &&
		!generationBatchFits(req.opts.NumBatch, predicted+drafterBatchVRAM(req.model, numCtx, at, req.opts.NumBatch, gpus), available) {
		req.opts.NumBatch = nextLowerGenerationBatch(req.opts.NumBatch)
	}
}
