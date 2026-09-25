package create

import (
	"os"
	"testing"

	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/types/xollama"
)

func xollamaLayers(t *testing.T, layers []manifest.Layer) []manifest.Layer {
	t.Helper()
	var out []manifest.Layer
	for _, l := range layers {
		if l.MediaType == xollama.MediaTypeImageJSON && l.Name == xollama.ConfigPath {
			out = append(out, l)
		}
	}
	return out
}

func TestApplyModelfileLayersWritesXollamaConfig(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	layers, err := ApplyModelfileLayers(nil, ModelfileLayerOptions{
		Xollama: &xollama.Config{Engine: xollama.EngineOpencoti},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := xollamaLayers(t, layers)
	if len(got) != 1 {
		t.Fatalf("got %d xollama config layers, want 1 (all layers: %+v)", len(got), layers)
	}

	blob, err := manifest.BlobsPath(got[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := xollama.Parse(data)
	if err != nil {
		t.Fatalf("stored blob does not parse: %v (%s)", err, data)
	}
	if cfg.Engine != xollama.EngineOpencoti {
		t.Fatalf("stored engine = %q, want %q", cfg.Engine, xollama.EngineOpencoti)
	}
	// The LOWEST schema that is true of it, not the newest this build knows:
	// an engine pin is a v1 config, so an older xollama must still be able to
	// read this model. See xollama.requiredVersion.
	if cfg.Version != xollama.SchemaVersionBase {
		t.Fatalf("stored version = %d, want %d", cfg.Version, xollama.SchemaVersionBase)
	}
}

// The other half of the version rule, at the storage boundary rather than in
// the marshaller: a config that actually uses a v2 field must say v2, or an
// older build would read it as v1 and serve the model differently from how its
// publisher meant.
func TestTheStoredVersionFollowsTheFieldsUsed(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	unified := true
	layers, err := ApplyModelfileLayers(nil, ModelfileLayerOptions{
		Xollama: &xollama.Config{KV: &xollama.KV{Unified: &unified}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := storedXollamaConfig(t, layers)
	if cfg.Version != 2 {
		t.Fatalf("stored version = %d, want 2", cfg.Version)
	}
}

// nil and empty are different requests. An ordinary create says nothing about
// the fork config and must inherit the parent's; `tweak model --clear` says
// the fork config is nothing, and has no other way to say it.
func TestAnEmptyXollamaConfigRemovesTheInheritedLayer(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	parent, err := ApplyModelfileLayers(nil, ModelfileLayerOptions{
		Xollama: &xollama.Config{Engine: xollama.EngineOpencoti},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := xollamaLayers(t, parent); len(got) != 1 {
		t.Fatalf("parent has %d xollama config layers, want 1", len(got))
	}

	kept, err := ApplyModelfileLayers(parent, ModelfileLayerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := xollamaLayers(t, kept); len(got) != 1 {
		t.Fatalf("a create that says nothing left %d xollama config layers, want 1 inherited", len(got))
	}

	cleared, err := ApplyModelfileLayers(parent, ModelfileLayerOptions{Xollama: &xollama.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := xollamaLayers(t, cleared); len(got) != 0 {
		t.Fatalf("an empty config left %d xollama config layers, want 0", len(got))
	}
}

func storedXollamaConfig(t *testing.T, layers []manifest.Layer) *xollama.Config {
	t.Helper()

	got := xollamaLayers(t, layers)
	if len(got) != 1 {
		t.Fatalf("got %d xollama config layers, want 1", len(got))
	}
	blob, err := manifest.BlobsPath(got[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := xollama.Parse(data)
	if err != nil {
		t.Fatalf("stored blob does not parse: %v (%s)", err, data)
	}
	return cfg
}

// A model has exactly one xollama config. Creating FROM a parent that already
// carries one must override it, not stack a second layer the reader would pick
// arbitrarily between.
func TestApplyModelfileLayersReplacesInheritedXollamaConfig(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	parent, err := ApplyModelfileLayers(nil, ModelfileLayerOptions{
		Xollama: &xollama.Config{Engine: xollama.EngineLlamaCpp},
	})
	if err != nil {
		t.Fatal(err)
	}

	child, err := ApplyModelfileLayers(parent, ModelfileLayerOptions{
		Xollama: &xollama.Config{Engine: xollama.EngineOpencoti},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := xollamaLayers(t, child)
	if len(got) != 1 {
		t.Fatalf("got %d xollama config layers after override, want 1", len(got))
	}
	blob, err := manifest.BlobsPath(got[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := xollama.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine != xollama.EngineOpencoti {
		t.Fatalf("child engine = %q, want the override %q", cfg.Engine, xollama.EngineOpencoti)
	}
}

// Upstream stores its own safetensors config under the SAME media type, named
// config.json. Removing by media type alone would delete it and break the
// model, so the remove must match on the name too.
func TestApplyModelfileLayersLeavesUpstreamConfigLayerAlone(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	upstream := manifest.Layer{
		MediaType: xollama.MediaTypeImageJSON,
		Name:      "config.json",
		Digest:    "sha256-notarealdigest",
	}

	layers, err := ApplyModelfileLayers([]manifest.Layer{upstream}, ModelfileLayerOptions{
		Xollama: &xollama.Config{Engine: xollama.EngineOpencoti},
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawUpstream bool
	for _, l := range layers {
		if l.MediaType == xollama.MediaTypeImageJSON && l.Name == "config.json" {
			sawUpstream = true
		}
	}
	if !sawUpstream {
		t.Fatalf("upstream config.json layer was removed: %+v", layers)
	}
	if n := len(xollamaLayers(t, layers)); n != 1 {
		t.Fatalf("got %d xollama config layers, want 1", n)
	}
}

// No directive, no layer — a plain model must be byte-identical to what
// upstream would produce.
func TestApplyModelfileLayersWritesNothingWithoutConfig(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	for _, tt := range []struct {
		name string
		cfg  *xollama.Config
	}{
		{name: "nil", cfg: nil},
		{name: "empty", cfg: &xollama.Config{Version: 1}},
		{name: "empty draft", cfg: &xollama.Config{Version: 1, Draft: &xollama.Draft{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			layers, err := ApplyModelfileLayers(nil, ModelfileLayerOptions{Xollama: tt.cfg})
			if err != nil {
				t.Fatal(err)
			}
			if n := len(xollamaLayers(t, layers)); n != 0 {
				t.Fatalf("got %d xollama config layers, want 0", n)
			}
		})
	}
}
