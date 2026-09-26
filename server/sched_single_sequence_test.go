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

// TestTheSingleSequenceRuleBindsOnlyStockLlamaCpp holds the ollama/ollama#4165
// exemption: the architecture deny-list is what stock llama.cpp gets wrong, so
// on opencoti the operator's sequence count stands. With the engine pinned to
// llama.cpp the scheduler must still start upstream's one sequence.
func TestTheSingleSequenceRuleBindsOnlyStockLlamaCpp(t *testing.T) {
	for _, tc := range []struct {
		engine string
		family string
		want   int
	}{
		{"opencoti", "qwen35", 4},
		{"llamacpp", "qwen35", 1},
		{"opencoti", "llama", 4},
		{"llamacpp", "llama", 4},
	} {
		t.Run(tc.engine+"/"+tc.family, func(t *testing.T) {
			t.Setenv("XOLLAMA_ENGINE", tc.engine)
			t.Setenv("OLLAMA_NUM_PARALLEL", "4")

			got := schedLoadParallel(t, completionModel(t, tc.family))
			if got != tc.want {
				t.Errorf("numParallel = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTheDenyListIsTheOnlyReasonOpencotiLifts(t *testing.T) {
	m := completionModel(t, "qwen35")
	cfg := llamaServerConfigForModel(m)
	if !cfg.SingleSequenceOnly || !cfg.SingleSequenceStockOnly {
		t.Errorf("qwen35: SingleSequenceOnly=%v SingleSequenceStockOnly=%v, want both", cfg.SingleSequenceOnly, cfg.SingleSequenceStockOnly)
	}

	cfg = llamaServerConfigForModel(completionModel(t, "llama"))
	if cfg.SingleSequenceOnly || cfg.SingleSequenceStockOnly {
		t.Errorf("llama: SingleSequenceOnly=%v SingleSequenceStockOnly=%v, want neither", cfg.SingleSequenceOnly, cfg.SingleSequenceStockOnly)
	}
}

// completionModel is a one-layer GGUF a scheduler can load, whose family is
// what the deny-list reads.
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
