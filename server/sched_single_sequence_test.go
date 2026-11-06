package server

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/model"
)

// TestEveryEngineServesTheOperatorsSequenceCount holds upstream v0.40.15
// (#19545): the ollama/ollama#4165 architecture deny-list is gone, so a family
// it named keeps the operator's count on stock llama.cpp too --
// XOLLAMA_PARALLEL on opencoti, OLLAMA_NUM_PARALLEL on stock.
func TestEveryEngineServesTheOperatorsSequenceCount(t *testing.T) {
	for _, engine := range []string{"opencoti", "llamacpp"} {
		for _, family := range []string{"qwen35", "llama"} {
			t.Run(engine+"/"+family, func(t *testing.T) {
				t.Setenv("XOLLAMA_ENGINE", engine)
				t.Setenv("OLLAMA_NUM_PARALLEL", "4")
				t.Setenv("XOLLAMA_PARALLEL", "4")

				if got := schedLoadParallel(t, completionModel(t, family)); got != 4 {
					t.Errorf("numParallel = %d, want 4", got)
				}
			})
		}
	}
}

// TestOnlyAnEmbeddingModelIsHeldToOneSequence: with the deny-list gone, the
// launch configuration's single-sequence flag means an embedding model.
func TestOnlyAnEmbeddingModelIsHeldToOneSequence(t *testing.T) {
	if cfg := llamaServerConfigForModel(completionModel(t, "qwen35")); cfg.SingleSequenceOnly {
		t.Error("qwen35: SingleSequenceOnly = true, want false")
	}
}

// completionModel is a one-layer GGUF a scheduler can load, of the given family.
func completionModel(t *testing.T, family string) *Model {
	t.Helper()
	m := &Model{Config: model.ConfigV2{ModelFamily: family}}
	m.ModelPath, _ = createBinFile(t, gguftest.KV{
		"general.architecture":          "llama",
		"llama.context_length":          uint32(32),
		"llama.embedding_length":        uint32(4096),
		"llama.block_count":             uint32(1),
		"llama.attention.head_count":    uint32(32),
		"llama.attention.head_count_kv": uint32(32),
		"tokenizer.ggml.tokens":         []string{" "},
		"tokenizer.ggml.scores":         []float32{0},
		"tokenizer.ggml.token_type":     []int32{0},
	}, []*gguftest.Tensor{
		{Name: "blk.0.attn.weight", Type: gguf.TensorTypeF32, Offset: uint64(0), Shape: []uint64{1, 1, 1, 1}, WriterTo: bytes.NewReader(make([]byte, 4))},
		{Name: "output.weight", Type: gguf.TensorTypeF32, Offset: uint64(0), Shape: []uint64{1, 1, 1, 1}, WriterTo: bytes.NewReader(make([]byte, 4))},
	})
	return m
}

func schedLoadParallel(t *testing.T, m *Model) int {
	t.Helper()
	ctx, done := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer done()
	s := InitScheduler(ctx)

	req := &LlmRequest{
		ctx:             ctx,
		model:           m,
		opts:            api.DefaultOptions(),
		successCh:       make(chan *runnerRef, 1),
		errCh:           make(chan error, 1),
		sessionDuration: &api.Duration{Duration: 2 * time.Second},
	}
	got := -1
	s.newServerFn = func(_ ml.SystemInfo, _ []ml.DeviceInfo, _ string, _ *gguf.Model, _ []string, _ []string, _ api.Options, numParallel int, _ llm.LlamaServerConfig) (llm.LlamaServer, error) {
		got = numParallel
		return &mockLlm{vramSize: 10, vramByGPU: map[ml.DeviceID]uint64{}}, nil
	}
	s.load(req, ml.SystemInfo{}, []ml.DeviceInfo{}, false)
	return got
}
