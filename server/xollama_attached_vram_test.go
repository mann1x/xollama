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

// The head's context as measured on eleven2go (mail #2308): 2911 MiB at
// num_batch 1024 and 5010 MiB at 2048, num_ctx 256000. The model may be high,
// never low, and not by more than a tenth.
func TestDraftHeadContextMatchesTheMeasurement(t *testing.T) {
	head := drafterInfo{assistant: true, width: 2816}
	for _, c := range []struct{ batch, measuredMiB int }{{1024, 2911}, {2048, 5010}} {
		got := drafterContextVRAM(head, 262144, c.batch) / format.MebiByte
		if got < uint64(c.measuredMiB) || got > uint64(c.measuredMiB)*11/10 {
			t.Errorf("num_batch %d: draft context %d MiB, measured %d", c.batch, got, c.measuredMiB)
		}
	}
	// Twice the width, twice the context; a tenth of the context, a tenth.
	if a, b := drafterContextVRAM(head, 262144, 1024), drafterContextVRAM(drafterInfo{assistant: true, width: 5632}, 262144, 1024); b != 2*a {
		t.Errorf("width scaling: %d then %d", a, b)
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
// projector, the IQ2 got num_batch 2048 and the heavier Q4_K_M 1024, so the
// lighter file took as much of the card. Counting what is attached, the
// lighter quant never gets the bigger batch.
func TestTheLighterQuantDoesNotGetTheBiggerBatch(t *testing.T) {
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

	batchFor := func(weightsAndKV uint64, m *Model) int {
		req := &LlmRequest{model: m, opts: api.DefaultOptions(), numBatchAuto: true}
		predicted := weightsAndKV + format.GibiByte + attachedVRAM(m, ctx, attachedBatch(req, req.opts.NumBatch))
		req.applyAutomaticGenerationBatch(true, ctx, predicted, avail, ml.FlashAttentionEnabled, gpus)
		return req.opts.NumBatch
	}
	iq2 := uint64(9057+3294) << 20
	q4 := uint64(12611+3187) << 20

	// What happened: nothing attached was counted.
	if got := batchFor(iq2, &Model{Name: "bare"}); got != 2048 {
		t.Fatalf("without the attachments the IQ2 gets %d; the case no longer reproduces", got)
	}
	gotIQ2, gotQ4 := batchFor(iq2, m), batchFor(q4, m)
	if gotIQ2 > gotQ4 {
		t.Fatalf("IQ2 num_batch %d > Q4_K_M num_batch %d", gotIQ2, gotQ4)
	}
	if gotIQ2 == 2048 {
		t.Fatal("the IQ2 still gets 2048 with a drafter and a projector counted")
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
