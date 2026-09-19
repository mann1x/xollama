package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/xollama"
)

// slidingWindowModel writes a small sliding-window GGUF and returns it with a
// Model that is servable for completion, which is what lets the slot ceiling
// rise above one.
func slidingWindowModel(t *testing.T) (*gguf.Model, *Model) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sliding.gguf")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gguftest.Write(file, gguftest.KV{
		"general.architecture":            "gemma3",
		"gemma3.block_count":              uint32(4),
		"gemma3.embedding_length":         uint32(256),
		"gemma3.context_length":           uint32(8192),
		"gemma3.attention.head_count":     uint32(4),
		"gemma3.attention.head_count_kv":  uint32(2),
		"gemma3.attention.sliding_window": uint32(1024),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := llm.LoadModel(path, 0)
	if err != nil {
		t.Fatal(err)
	}

	m := &Model{ModelPath: path}
	m.Config.Capabilities = []string{"completion"}
	return f, m
}

func slidingWindowRequest(t *testing.T) (*gguf.Model, *LlmRequest) {
	t.Helper()

	f, m := slidingWindowModel(t)
	opts := api.DefaultOptions()
	opts.NumCtx = 4096
	opts.NumBatch = 512
	return f, &LlmRequest{model: m, opts: opts}
}

// TestSlotCeilingVRAMRaisesThePrediction is the wiring test for the surcharge:
// the scheduler's own prediction has to grow when the load's ceiling does,
// because that prediction is what decides placement and eviction. Without it,
// the number describes only the request that spawned the runner.
func TestSlotCeilingVRAMRaisesThePrediction(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f, req := slidingWindowRequest(t)
	if got := slotCeilingVRAM(req, f, nil, 1); got == 0 {
		t.Fatal("slot ceiling surcharge = 0, want a charge: dynamic slots are on by default")
	}

	base := llm.PredictServerVRAM(req.model.ModelPath, f, effectiveLlamaServerContext(req.opts.NumCtx, f, 1))
	if base+slotCeilingVRAM(req, f, nil, 1) <= base {
		t.Error("prediction did not grow with the ceiling")
	}
}

// TestSlotCeilingVRAMIsUpstreamsOnStockLlamaCpp is the off-means-off case at the
// scheduler level: with the engine pinned to llama.cpp the prediction has to be
// the number upstream would have produced, because no slot flag is passed there.
func TestSlotCeilingVRAMIsUpstreamsOnStockLlamaCpp(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")

	f, req := slidingWindowRequest(t)
	if got := slotCeilingVRAM(req, f, nil, 1); got != 0 {
		t.Errorf("slot ceiling surcharge = %d bytes, want 0", got)
	}
}

// TestSlotCeilingVRAMSurvivesAnUnchosenBatch covers the ordering trap in the
// scheduler: the automatic batch size is chosen from the prediction, so
// NumBatch is still zero at the moment the prediction is made.
//
// The batch term cancels between the two layouts while neither clamps, so this
// only bites once the ceiling's window runs past the context -- which is
// exactly when the surcharge is largest. A zero left in place there shrinks the
// single-slot side alone and overstates the difference.
func TestSlotCeilingVRAMSurvivesAnUnchosenBatch(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f, req := slidingWindowRequest(t)
	chosen := slotCeilingVRAM(req, f, nil, 1)

	req.opts.NumBatch = 0
	unchosen := slotCeilingVRAM(req, f, nil, 1)

	if unchosen != chosen {
		t.Errorf("surcharge with an unchosen batch = %d bytes, want the default-batch figure %d", unchosen, chosen)
	}
}

// TestSlotCeilingVRAMFollowsTheModelConfig checks the per-model plane reaches
// the prediction: a model that switches dynamic slots off must be predicted the
// way upstream predicts it.
func TestSlotCeilingVRAMFollowsTheModelConfig(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f, req := slidingWindowRequest(t)
	off := false
	req.model.Xollama = &xollama.Config{Version: 1, Slots: &xollama.Slots{Dynamic: &off}}

	if got := slotCeilingVRAM(req, f, nil, 1); got != 0 {
		t.Errorf("slot ceiling surcharge = %d bytes, want 0 for a model with dynamic slots off", got)
	}
}
