package llm

import (
	"os"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/ml"
)

// On opencoti a vision model must not reach the engine with a fit target:
// one switches the engine's automatic margin off (V100, 2026-09-27: a 1909 MiB
// margin and 4 GB of KV on the host with 4.9 GB of VRAM free). Stock llama.cpp
// keeps upstream's pad (TestMMProjFitTargetExtraEnvs).
func TestAVisionModelOnOpencotiGetsNoFitTarget(t *testing.T) {
	t.Setenv(llamaArgFitTargetEnv, "")
	_ = os.Unsetenv(llamaArgFitTargetEnv)
	launch := llamaServerLaunchConfig{
		projectors:   []string{"model.gguf"},
		mmprojMemory: 885 * bytesPerMiB,
		opts:         api.DefaultOptions(),
		gpus:         []ml.DeviceInfo{{DeviceID: ml.DeviceID{Library: "CUDA"}, FreeMemory: 32 << 30, TotalMemory: 32 << 30}},
		modelLayers:  66,
		extraEnvs:    map[string]string{"KEEP": "1"},
	}
	if _, ok := launch.extraEnvsForStart()[llamaArgFitTargetEnv]; !ok {
		t.Fatal("precondition: stock llama.cpp gets upstream's projector pad")
	}
	got := launch.opencotiEnvsForStart()
	if v, ok := got[llamaArgFitTargetEnv]; ok {
		t.Fatalf("opencoti got a fit target %q; the pad switches its automatic margin off", v)
	}
	if got["KEEP"] != "1" {
		t.Fatal("the launch's own environment was dropped")
	}

	launch.extraEnvs = map[string]string{llamaArgFitTargetEnv: "512"}
	if v := launch.opencotiEnvsForStart()[llamaArgFitTargetEnv]; v != "512" {
		t.Fatalf("a stated fit target = %q, want it passed on unchanged (512)", v)
	}
}
