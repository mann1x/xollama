package manifest

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

func dualList() []Manifest {
	return []Manifest{
		createManifestForTest("sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), RunnerMLX),
		createManifestForTest("sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64), RunnerLlamaCPP),
	}
}

// A model with a GGUF child never goes to MLX while opencoti is the engine,
// on any OS. On Apple silicon this fails without the opencoti-first hook
// (upstream v0.40.0 puts MLX first there); the macOS legs of test.yaml run it.
func TestOpencotiFirstPicksTheGGUFChildOnEveryPlatform(t *testing.T) {
	for _, engine := range []string{"", "auto", "opencoti"} {
		t.Setenv("XOLLAMA_ENGINE", engine)
		if p := runnerPreferences(); p[0] != RunnerLlamaCPP {
			t.Fatalf("XOLLAMA_ENGINE=%q on %s/%s: preferences %v, want llamacpp first", engine, runtime.GOOS, runtime.GOARCH, p)
		}
		child, err := selectManifestWithPreferences(dualList(), runnerPreferences())
		if err != nil {
			t.Fatal(err)
		}
		if child.Runner != RunnerLlamaCPP {
			t.Fatalf("XOLLAMA_ENGINE=%q: picked %q from a GGUF+MLX list, want llamacpp", engine, child.Runner)
		}
	}
}

// MLX still serves what has no GGUF child, and an explicit runner wins.
func TestOpencotiFirstLeavesMLXOnlyModelsAndExplicitRunners(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	mlxOnly := dualList()[:1]
	child, err := selectManifestWithPreferences(mlxOnly, runnerPreferences())
	if err != nil || child.Runner != RunnerMLX {
		t.Fatalf("MLX-only list: child %v err %v, want mlx", child, err)
	}
	if p := runnerPreferencesFor("mlx"); !slices.Equal(p, []string{RunnerMLX}) {
		t.Fatalf("explicit mlx: preferences %v", p)
	}
}

// Off means off: with the stock engine the order is upstream's.
func TestOpencotiFirstIsOffWithTheStockEngine(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	want := []string{RunnerLlamaCPP, RunnerGGML, RunnerMLX}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		want = []string{RunnerMLX, RunnerLlamaCPP, RunnerGGML}
	}
	if p := runnerPreferences(); !slices.Equal(p, want) {
		t.Fatalf("XOLLAMA_ENGINE=llamacpp: preferences %v, want upstream's %v", p, want)
	}
}
