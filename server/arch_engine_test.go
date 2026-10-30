package server

import (
	"testing"

	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/types/xollama"
)

func archModel(t *testing.T, arch string) *Model {
	t.Helper()
	path, _ := createBinFile(t, gguftest.KV{"general.architecture": arch}, []*gguftest.Tensor{})
	m := &Model{ModelPath: path, ShortName: "m"}
	loadTestMetadata(t, m)
	return m
}

// withArchServed stubs the pin's answer: lfm3 unserved unless served is true,
// every other architecture served, as the real table has it.
func withArchServed(t *testing.T, served bool, goos string) {
	t.Helper()
	oldServes, oldGOOS := engineServesArch, archGOOS
	engineServesArch = func(arch string) bool { return arch != "lfm3" || served }
	archGOOS = goos
	t.Cleanup(func() { engineServesArch, archGOOS = oldServes, oldGOOS })
}

func TestAModelOfAnArchitectureTheEngineLacksGoesToStock(t *testing.T) {
	withArchServed(t, false, "linux")
	m := archModel(t, "lfm3")

	if got := archEngine(m, nil); got == nil || got.Engine != xollama.EngineLlamaCpp {
		t.Fatalf("no settings: engine = %+v, want %q", got, xollama.EngineLlamaCpp)
	}

	own := &xollama.Config{FlashAttention: "on"}
	got := archEngine(m, own)
	if got.Engine != xollama.EngineLlamaCpp || got.FlashAttention != "on" {
		t.Fatalf("with settings: %+v, want its settings kept and engine %q", got, xollama.EngineLlamaCpp)
	}
	if own.Engine != "" {
		t.Fatalf("the model's own config was changed: engine = %q", own.Engine)
	}
}

func TestOnlyAnUnservedArchitectureWithoutItsOwnEngineIsMoved(t *testing.T) {
	withArchServed(t, false, "linux")

	stated := &xollama.Config{Engine: xollama.EngineOpencoti}
	if got := archEngine(archModel(t, "lfm3"), stated); got != stated {
		t.Fatalf("an engine the model states was replaced: %+v", got)
	}
	if got := archEngine(archModel(t, "lfm2"), nil); got != nil {
		t.Fatalf("an architecture the engine serves was moved: %+v", got)
	}
	if got := archEngine(&Model{ShortName: "safetensors"}, nil); got != nil {
		t.Fatalf("a model with no GGUF architecture was moved: %+v", got)
	}
}

func TestAnArchitectureStaysOnAnEngineThatDeclaresIt(t *testing.T) {
	withArchServed(t, true, "linux")
	if got := archEngine(archModel(t, "lfm3"), nil); got != nil {
		t.Fatalf("the engine declares the architecture, yet the model was moved: %+v", got)
	}
}

// macOS ships no stock llama-server: moving the load there would replace the
// engine's "unknown model architecture" with the generic stockless refusal.
func TestAnUnservedArchitectureStaysOnOpencotiWhereNoStockShips(t *testing.T) {
	withArchServed(t, false, "darwin")
	if got := archEngine(archModel(t, "lfm3"), nil); got != nil {
		t.Fatalf("macOS: the model was moved to an engine this build does not ship: %+v", got)
	}
}

// The launch reads the engine from launchXollama; an archEngine nobody calls
// would pass every test above.
func TestTheLaunchConfigOfAnUnservedArchitectureNamesStock(t *testing.T) {
	withArchServed(t, false, "windows")
	if got := launchXollama(archModel(t, "lfm3")); got == nil || got.Engine != xollama.EngineLlamaCpp {
		t.Fatalf("launch config: %+v, want engine %q", got, xollama.EngineLlamaCpp)
	}
}
