package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ollama/ollama/manifest"
	modeltypes "github.com/ollama/ollama/types/model"
)

func TestRootQuantization(t *testing.T) {
	for _, tt := range []struct {
		name      string
		root      *Root
		groupSize int
		bits      int
		mode      string
	}{
		// Unquantized models leave the linear defaults zero, as constructors
		// that only set them for a non-empty quant type did.
		{name: "unquantized", root: &Root{}},
		{name: "default group size", root: &Root{quantType: "NVFP4", groupSize: 16}, groupSize: 16, bits: 4, mode: "nvfp4"},
		{name: "blob group size", root: &Root{quantType: "INT4", groupSize: 32}, groupSize: 32, bits: 4, mode: "affine"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			groupSize, bits, mode, tensorQuant := tt.root.Quantization()
			if groupSize != tt.groupSize || bits != tt.bits || mode != tt.mode {
				t.Fatalf("Quantization() = %d, %d, %q, want %d, %d, %q", groupSize, bits, mode, tt.groupSize, tt.bits, tt.mode)
			}
			if tensorQuant == nil || len(tensorQuant) != 0 {
				t.Fatalf("tensorQuant = %v, want an empty map", tensorQuant)
			}
		})
	}
}

func TestRootQuantizationCopiesTensorQuant(t *testing.T) {
	root := &Root{
		quantType:   "INT8",
		groupSize:   64,
		tensorQuant: map[string]*TensorQuantInfo{"w": {QuantType: "INT4", GroupSize: 32}},
	}
	_, _, _, tensorQuant := root.Quantization()
	if got := tensorQuant["w"]; got == nil || got.QuantType != "INT4" || got.GroupSize != 32 {
		t.Fatalf("tensorQuant[w] = %+v, want INT4 group 32", got)
	}
	tensorQuant["w"].GroupSize = 128
	if root.tensorQuant["w"].GroupSize != 32 {
		t.Fatal("Quantization returned the root's own tensor metadata instead of a copy")
	}
}

func manifestForRunnerTest(fill, runner, format string) manifest.Manifest {
	return manifest.Manifest{
		SchemaVersion: 2,
		MediaType:     manifest.MediaTypeManifest,
		Runner:        runner,
		Format:        format,
		Config: manifest.Layer{
			MediaType: "application/vnd.docker.container.image.v1+json",
			Digest:    "sha256:" + strings.Repeat(fill, 64),
			Size:      12,
		},
	}
}

func writeManifestForRunnerTest(t *testing.T, name string, m manifest.Manifest) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.WriteManifestData(modeltypes.ParseName(name), data); err != nil {
		t.Fatal(err)
	}
}

// The MLX runner serves only the MLX child of a manifest list. Resolved by the
// platform's default order, which puts llamacpp first off Apple silicon, a
// model pulled for both runners opened its GGUF child here.
func TestOpenSelectsTheMLXChildOfAManifestList(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	writeManifestForRunnerTest(t, "both", manifest.Manifest{
		SchemaVersion: 2,
		MediaType:     manifest.MediaTypeManifestList,
		Manifests: []manifest.Manifest{
			manifestForRunnerTest("a", manifest.RunnerLlamaCPP, manifest.FormatGGUF),
			manifestForRunnerTest("b", manifest.RunnerMLX, manifest.FormatSafetensors),
		},
	})

	root, err := Open("both")
	if err != nil {
		t.Fatal(err)
	}
	if got := root.Manifest.Runner; got != manifest.RunnerMLX {
		t.Fatalf("runner = %q, want %q", got, manifest.RunnerMLX)
	}
	if got, want := root.Manifest.Config.Digest, "sha256:"+strings.Repeat("b", 64); got != want {
		t.Fatalf("config digest = %q, want the MLX child's %q", got, want)
	}
}

// A model that is not a list opens as before, including one written before
// manifests carried a runner.
func TestOpenKeepsASingleManifest(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	for _, runner := range []string{manifest.RunnerMLX, ""} {
		writeManifestForRunnerTest(t, "single", manifestForRunnerTest("c", runner, manifest.FormatSafetensors))

		root, err := Open("single")
		if err != nil {
			t.Fatalf("runner %q: %v", runner, err)
		}
		if got, want := root.Manifest.Config.Digest, "sha256:"+strings.Repeat("c", 64); got != want {
			t.Fatalf("runner %q: config digest = %q, want %q", runner, got, want)
		}
	}
}
