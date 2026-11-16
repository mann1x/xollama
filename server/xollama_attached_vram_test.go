package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

// Before the engine has reported a head, the solidPC curve prices it
// (RTX 3090, c14 r15, 26B-A4B head, 2026-11-16): never below what it took
// at num_ctx 32768, and the long contexts on the safe side.
func TestDraftHeadContextBeforeAReport(t *testing.T) {
	head := drafterInfo{path: "/blobs/sha256-unreported", assistant: true, width: 2816}
	for _, c := range []struct{ ctx, batch, measuredMiB int }{
		{32768, 512, 76},
		{32768, 1024, 152},
		{32768, 2048, 302},
		{131072, 512, 172},
		{256000, 512, 294},
		{256000, 1024, 338},
		{256000, 2048, 424},
	} {
		got := drafterContextVRAM(head, c.ctx, c.batch) / format.MebiByte
		if got+2 < uint64(c.measuredMiB) {
			t.Errorf("num_ctx %d num_batch %d: priced %d MiB, took %d", c.ctx, c.batch, got, c.measuredMiB)
		}
	}
}

// After a report, the engine's own number is the price, scaled to the batch
// and to a longer context.
func TestDraftHeadContextFromTheEnginesReport(t *testing.T) {
	head := drafterInfo{path: "/blobs/sha256-eleven2go-head", assistant: true, width: 2816}
	llm.StoreDraftHeadContext(head.path, llm.DraftHeadMeasure{NumCtx: 256000, NumBatch: 2048, Context: 4424 << 20})
	if got := drafterContextVRAM(head, 256000, 2048) >> 20; got != 4424 {
		t.Errorf("same load: %d MiB, reported 4424", got)
	}
	if got := drafterContextVRAM(head, 256000, 1024) >> 20; got != 2212 {
		t.Errorf("half the batch: %d MiB, want 2212 (eleven2go measured 2338)", got)
	}
	if got := drafterContextVRAM(head, 32768, 2048) >> 20; got != 4424 {
		t.Errorf("a shorter context keeps the reported price, got %d MiB", got)
	}
}

// sparse makes a file of that size without writing it.
func sparse(t *testing.T, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "projector.gguf")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return p
}

// mail #2308: on a 24 GB card at num_ctx 256000 with a drafter and a
// projector, the IQ2 got num_batch 2048 and the heavier Q4_K_M 1024, and the
// head's draft context grows with the batch (eleven2go: 2338 MiB at 1024,
// 4424 at 2048), so the lighter file took as much of the card. With what is
// attached counted, the lighter quant stays lighter at the batch it gets.
func TestTheLighterQuantStaysLighter(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	cuda := ml.DeviceInfo{
		DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, PCIID: "0000:11:00.0", Description: "NVIDIA GeForce RTX 3090",
		ComputeMajor: 8, ComputeMinor: 6, TotalMemory: 24 << 30, FreeMemory: 22800 << 20,
	}
	gpus := []ml.DeviceInfo{cuda}
	if !llm.EveryGroupOnOpencoti(llm.LlamaServerConfig{}, gpus) {
		t.Skip("this build's pin does not serve CUDA here; the case does not arise")
	}
	drafter, _ := createBinFile(t, map[string]any{
		"general.architecture":                  "gemma4-assistant",
		"gemma4-assistant.embedding_length_out": uint32(2816),
	}, nil)
	m := &Model{Name: "gemma4-26b-agentic:mtp-vision", DraftPath: drafter, ProjectorPaths: []string{sparse(t, 1140<<20)}}
	const ctx = 262144
	avail := cuda.FreeMemory

	// load returns the batch a load gets and what it takes at that batch:
	// the prediction, the target's batch cost and the drafter's.
	load := func(weightsAndKV uint64, m *Model) (int, uint64) {
		req := &LlmRequest{model: m, opts: api.DefaultOptions(), numBatchAuto: true}
		predicted := weightsAndKV + format.GibiByte + attachedVRAM(m, ctx, attachedBatch(req, req.opts.NumBatch))
		req.applyAutomaticGenerationBatch(true, ctx, predicted, avail, ml.FlashAttentionEnabled, gpus)
		b := req.opts.NumBatch
		return b, predicted + llm.LlamaServerBatchMemory(b) + drafterBatchVRAM(m, ctx, llamaServerGenerationBatchDefault, b, gpus)
	}
	iq2 := uint64(9057+3294) << 20
	q4 := uint64(12611+3187) << 20

	// What happened: nothing attached was counted, and the IQ2 got 2048.
	if b, _ := load(iq2, &Model{Name: "bare"}); b != 2048 {
		t.Fatalf("without the attachments the IQ2 gets %d; the case no longer reproduces", b)
	}

	for _, report := range []bool{false, true} {
		if report {
			llm.StoreDraftHeadContext(drafter, llm.DraftHeadMeasure{NumCtx: 256000, NumBatch: 2048, Context: 4424 << 20})
		}
		bIQ2, tIQ2 := load(iq2, m)
		bQ4, tQ4 := load(q4, m)
		t.Logf("head report %v: IQ2 num_batch %d takes %.1f GiB, Q4_K_M num_batch %d takes %.1f GiB",
			report, bIQ2, float64(tIQ2)/(1<<30), bQ4, float64(tQ4)/(1<<30))
		if tIQ2+format.GibiByte > tQ4 {
			t.Errorf("head report %v: the IQ2 takes %.1f GiB at %d, the Q4_K_M %.1f GiB at %d: the lighter file is not lighter",
				report, float64(tIQ2)/(1<<30), bIQ2, float64(tQ4)/(1<<30), bQ4)
		}
		if report && bIQ2 == 2048 {
			t.Error("with eleven2go's head report the IQ2 still gets 2048")
		}
	}
}

// Off means off: on stock llama.cpp nothing attached is added and the batch
// is upstream's.
func TestStockEngineBatchIsUpstreams(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	gpus := []ml.DeviceInfo{{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, TotalMemory: 24 << 30, FreeMemory: 22800 << 20}}
	drafter, _ := createBinFile(t, map[string]any{"general.architecture": "gemma4-assistant"}, nil)
	m := &Model{DraftPath: drafter}
	req := &LlmRequest{model: m, opts: api.DefaultOptions(), numBatchAuto: true}
	predicted := uint64(13) << 30
	want := automaticGenerationBatch(262144, predicted, gpus[0].FreeMemory, ml.FlashAttentionEnabled, gpus)
	req.applyAutomaticGenerationBatch(true, 262144, predicted, gpus[0].FreeMemory, ml.FlashAttentionEnabled, gpus)
	if req.opts.NumBatch != want {
		t.Fatalf("num_batch %d, upstream's %d", req.opts.NumBatch, want)
	}
	if got := drafterBatchVRAM(m, 262144, 512, 2048, gpus); got != 0 {
		t.Fatalf("drafter batch charge %d on stock llama.cpp", got)
	}
}
