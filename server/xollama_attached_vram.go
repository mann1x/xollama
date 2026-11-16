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

// An assistant head's draft context is priced from the engine's own last
// report for that drafter (llm/engine_draft_head.go), scaled to a longer
// context (linear, the safe side). Since c14 r16 (0851, bug-4363, our #2311)
// the engine builds it with max(32, drafting sequences) rows instead of the
// target's batch, so it does not grow with num_batch: 6 MiB at num_ctx 32768
// and 20 at 256000, for num_batch 512 and 2048 alike (solidPC RTX 3090,
// 26B-A4B head, 2816 wide, 2026-11-16). Under r15 it was 76 to 430 MiB on
// solidPC and 2338 / 4424 on eleven2go, linear in the batch. Before any
// report, that line stands: 5 MiB plus 2 per 32768 of context.
const (
	draftHeadMeasuredWidth = 2816
	draftHeadBase          = 5 * format.MebiByte
	draftHeadPerCurveCtx   = 2 * format.MebiByte
	draftHeadCurveCtx      = 32768
)

type drafterInfo struct {
	path      string
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
	d := drafterInfo{path: path}
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
// num_batch. An assistant head's does not depend on the batch (c14 r16). A
// drafter that is not an assistant head (a model of its own) is priced as one
// more compute reserve plus the batch's, as the target's compute is.
func drafterContextVRAM(d drafterInfo, numCtx, numBatch int) uint64 {
	if numCtx <= 0 {
		return 0
	}
	batch := max(numBatch, llamaServerGenerationBatchDefault)
	if !d.assistant {
		return format.GibiByte + llm.LlamaServerBatchMemory(batch)
	}
	if m, ok := llm.DraftHeadContext(d.path); ok && m.Context > 0 {
		c := m.Context
		if m.NumCtx > 0 && numCtx > m.NumCtx {
			c = c * uint64(numCtx) / uint64(m.NumCtx)
		}
		return c
	}
	width := d.width
	if width == 0 {
		width = draftHeadMeasuredWidth
	}
	c := draftHeadBase + draftHeadPerCurveCtx*uint64(numCtx)/draftHeadCurveCtx
	return c * width / draftHeadMeasuredWidth
}

// attachedVRAM is what the opencoti prediction leaves out and the engine
// allocates at load: an attached drafter's file and its draft context.
func attachedVRAM(m *Model, numCtx, numBatch int) uint64 {
	if m == nil || m.DraftPath == "" || llamaServerConfigForModel(m).DraftOff {
		return 0
	}
	d := drafterOf(m.DraftPath)
	return d.size + drafterContextVRAM(d, numCtx, numBatch)
}

// projectorVRAM is the projectors' files. opencoti loads a projector at the
// first image, not at load, so it is not part of the load's prediction
// (placement and eviction); it is headroom the automatic num_batch keeps, so
// that image finds room on the card instead of going to the CPU (mail #2308).
func projectorVRAM(m *Model) uint64 {
	if m == nil {
		return 0
	}
	var total uint64
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
// with what is attached counted too: the drafter's share of the batch and
// the projector's headroom (mail #2308). It runs after upstream's choice,
// only for an automatic batch on an opencoti load with a drafter or a
// projector.
func (req *LlmRequest) drafterAwareBatch(numCtx int, predicted, available uint64, gpus []ml.DeviceInfo) {
	if !req.numBatchAuto || req.model == nil || (req.model.DraftPath == "" && len(req.model.ProjectorPaths) == 0) ||
		!llm.EveryGroupOnOpencoti(llamaServerConfigForModel(req.model), gpus) {
		return
	}
	at := llamaServerGenerationBatchDefault
	headroom := projectorVRAM(req.model)
	for req.opts.NumBatch > at &&
		!generationBatchFits(req.opts.NumBatch, predicted+headroom+drafterBatchVRAM(req.model, numCtx, at, req.opts.NumBatch, gpus), available) {
		req.opts.NumBatch = nextLowerGenerationBatch(req.opts.NumBatch)
	}
}
