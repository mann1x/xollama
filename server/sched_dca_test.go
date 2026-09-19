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

// dcaModel writes a GGUF for an architecture that has the chunked attention
// route, with a small trained context so a request can exceed it.
func dcaModel(t *testing.T, arch string, trainCtx uint32) (*gguf.Model, *Model) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "dca.gguf")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gguftest.Write(file, gguftest.KV{
		"general.architecture":            arch,
		arch + ".block_count":             uint32(4),
		arch + ".embedding_length":        uint32(256),
		arch + ".context_length":          trainCtx,
		arch + ".attention.head_count":    uint32(4),
		arch + ".attention.head_count_kv": uint32(2),
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

func dcaRequest(t *testing.T, arch string, trainCtx uint32, numCtx int, on bool) (*gguf.Model, *LlmRequest) {
	t.Helper()

	f, m := dcaModel(t, arch, trainCtx)
	if on {
		enabled := true
		m.Xollama = &xollama.Config{Version: 1, DCA: &xollama.DCA{Enabled: &enabled}}
	}
	opts := api.DefaultOptions()
	opts.NumCtx = numCtx
	return f, &LlmRequest{model: m, opts: opts}
}

// TestContextIsClampedWithoutDCA is upstream's behaviour, which has to survive
// untouched: a request for more context than the model was trained in is cut
// back to the trained figure.
func TestContextIsClampedWithoutDCA(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f, req := dcaRequest(t, "qwen3", 32768, 131072, false)
	if got := effectiveModelContext(req.opts.NumCtx, f, req.contextUnlocked(f, nil)); got != 32768 {
		t.Errorf("effective context = %d, want the trained 32768", got)
	}
}

// TestDCAUnlocksTheContextForTheEstimate is the point of the whole feature, and
// the half that is easy to get wrong: the scheduler has to size the cache for
// the context actually being served, not the one the file declares. A clamp
// here with no clamp at launch would under-count the cache by the difference.
func TestDCAUnlocksTheContextForTheEstimate(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f, req := dcaRequest(t, "qwen3", 32768, 131072, true)
	if got := effectiveModelContext(req.opts.NumCtx, f, req.contextUnlocked(f, nil)); got != 131072 {
		t.Errorf("effective context = %d, want the requested 131072", got)
	}

	clamped := llm.PredictServerVRAM(req.model.ModelPath, f, effectiveLlamaServerContext(req.opts.NumCtx, f, 1, false))
	unlocked := llm.PredictServerVRAM(req.model.ModelPath, f, effectiveLlamaServerContext(req.opts.NumCtx, f, 1, true))
	if unlocked <= clamped {
		t.Errorf("prediction did not grow with the unlocked context: %d vs %d", unlocked, clamped)
	}
}

// TestDCADoesNotUnlockOnStockLlamaCpp is off-means-off: the engine that carries
// the chunked route is opencoti, so a pinned stock load has to be clamped
// exactly as upstream clamps it.
func TestDCADoesNotUnlockOnStockLlamaCpp(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")

	f, req := dcaRequest(t, "qwen3", 32768, 131072, true)
	if req.contextUnlocked(f, nil) {
		t.Error("stock llama.cpp has no chunked route and must not unlock the context")
	}
}

// TestDCADoesNotUnlockAnArchitectureWithoutTheRoute guards the silent failure:
// on an architecture whose graph never builds the chunked input the flag does
// nothing, so unlocking the context there would serve the model unprotected.
func TestDCADoesNotUnlockAnArchitectureWithoutTheRoute(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f, req := dcaRequest(t, "llama", 32768, 131072, true)
	if req.contextUnlocked(f, nil) {
		t.Error("an architecture with no chunked route must not unlock the context")
	}
}

// TestRunnerServesPastTrainedContext covers the runner-reuse half. A DCA runner
// holds more context than its model declares; clamping an incoming request back
// to the declared figure would make a request for more context compare equal to
// it and reuse a runner that cannot serve it.
func TestRunnerServesPastTrainedContext(t *testing.T) {
	for _, tc := range []struct {
		name       string
		numCtx     int
		trainCtx   int
		nilOptions bool
		want       bool
	}{
		{name: "a DCA runner", numCtx: 131072, trainCtx: 32768, want: true},
		{name: "an ordinary runner", numCtx: 32768, trainCtx: 32768},
		{name: "a model that declares no trained context", numCtx: 131072, trainCtx: 0},
		{name: "a runner with no options yet", nilOptions: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &runnerRef{trainContext: tc.trainCtx}
			if !tc.nilOptions {
				opts := api.DefaultOptions()
				opts.NumCtx = tc.numCtx
				runner.Options = &opts
			}
			if got := runnerServesPastTrainedContext(runner); got != tc.want {
				t.Errorf("runnerServesPastTrainedContext() = %v, want %v", got, tc.want)
			}
		})
	}
	if runnerServesPastTrainedContext(nil) {
		t.Error("a nil runner serves nothing")
	}
}
