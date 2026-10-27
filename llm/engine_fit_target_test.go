package llm

import (
	"os"
	"runtime"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm/engine"
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

// Both CUDA libraries sit beside one engine and one process loads one. On a
// host that also has a newer card the engine's own pick is CUDA 13, so a load
// on a card only the CUDA 12 library serves has to say so; any other load
// leaves the pick to the engine and the launch's environment untouched.
func TestALoadOnACUDA12CardTellsTheEngine(t *testing.T) {
	t.Setenv(engine.EnvPath, "")
	pin, err := engine.DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	// LegacyCUDA asks about the host it runs on, so the pin must carry a CUDA
	// 12 library for THIS platform: a macOS runner has none, and found the
	// Linux one only while the lookup ignored the platform (xo-42).
	label, err := engine.PackageArch(runtime.GOOS, runtime.GOARCH)
	if _, ok := pin.CUDA12DSO(label); err != nil || !ok || !pin.CoversCUDA12(7, 0) {
		t.Skipf("the committed pin carries no CUDA 12 library for a 7.0 card on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	card := func(major, minor int) []ml.DeviceInfo {
		return []ml.DeviceInfo{{DeviceID: ml.DeviceID{Library: "CUDA"}, ComputeMajor: major, ComputeMinor: minor}}
	}
	shared := map[string]string{"KEEP": "1"}
	v100 := llamaServerLaunchConfig{opts: api.DefaultOptions(), gpus: card(7, 0), extraEnvs: shared}
	if got := v100.opencotiEnvsForStart(); got[engine.EnvCUDALegacy] != "1" || got["KEEP"] != "1" {
		t.Errorf("V100 launch envs = %v, want %s=1 beside the launch's own", got, engine.EnvCUDALegacy)
	}
	if _, leaked := shared[engine.EnvCUDALegacy]; leaked {
		t.Error("the switch was written into the launch's own map, where the next load would inherit it")
	}
	rtx := llamaServerLaunchConfig{opts: api.DefaultOptions(), gpus: card(8, 6), extraEnvs: shared}
	if got := rtx.opencotiEnvsForStart(); len(got) != 1 {
		t.Errorf("RTX 3090 launch envs = %v, want the launch's own and nothing else", got)
	}
}
