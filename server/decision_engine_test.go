package server

import (
	"testing"

	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/types/xollama"
)

func decisionModel(t *testing.T, decisionType string) *Model {
	t.Helper()
	kv := gguftest.KV{"general.architecture": "qwen35"}
	if decisionType != "" {
		kv["qwen35.decision.type"] = decisionType
	}
	path, _ := createBinFile(t, kv, []*gguftest.Tensor{})
	m := &Model{ModelPath: path, ShortName: "m"}
	loadTestMetadata(t, m)
	return m
}

func withClefHead(t *testing.T, has bool) {
	t.Helper()
	old := engineHasClefHead
	engineHasClefHead = func() bool { return has }
	t.Cleanup(func() { engineHasClefHead = old })
}

func TestAClefModelGoesToStockWhileTheEngineHasNoClefHead(t *testing.T) {
	withClefHead(t, false)
	m := decisionModel(t, "clef")

	if got := clefEngine(m, nil); got == nil || got.Engine != xollama.EngineLlamaCpp {
		t.Fatalf("a Clef model with no settings: engine = %+v, want %q", got, xollama.EngineLlamaCpp)
	}

	own := &xollama.Config{FlashAttention: "on"}
	got := clefEngine(m, own)
	if got.Engine != xollama.EngineLlamaCpp || got.FlashAttention != "on" {
		t.Fatalf("a Clef model with settings: %+v, want its settings kept and engine %q", got, xollama.EngineLlamaCpp)
	}
	if own.Engine != "" {
		t.Fatalf("the model's own config was changed: engine = %q", own.Engine)
	}
}

func TestOnlyAClefModelWithoutItsOwnEngineIsMoved(t *testing.T) {
	withClefHead(t, false)

	stated := &xollama.Config{Engine: xollama.EngineOpencoti}
	if got := clefEngine(decisionModel(t, "clef"), stated); got != stated {
		t.Fatalf("an engine the model states was replaced: %+v", got)
	}
	// Scored from token probabilities, which opencoti serves (nimble, measured).
	if got := clefEngine(decisionModel(t, "systemone"), nil); got != nil {
		t.Fatalf("a decision model without the Clef head was moved: %+v", got)
	}
	if got := clefEngine(decisionModel(t, ""), nil); got != nil {
		t.Fatalf("a chat model was moved: %+v", got)
	}
}

func TestAClefModelStaysOnAnEngineThatDeclaresTheHead(t *testing.T) {
	withClefHead(t, true)
	if got := clefEngine(decisionModel(t, "clef"), nil); got != nil {
		t.Fatalf("the engine declares clef_score_v1, yet the model was moved: %+v", got)
	}
}

// The launch reads the engine from launchXollama; a clefEngine nobody calls
// would pass every test above.
func TestTheLaunchConfigOfAClefModelNamesStock(t *testing.T) {
	withClefHead(t, false)
	if got := launchXollama(decisionModel(t, "clef")); got == nil || got.Engine != xollama.EngineLlamaCpp {
		t.Fatalf("launch config of a Clef model: %+v, want engine %q", got, xollama.EngineLlamaCpp)
	}
}
