package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

func gemma4Base(t *testing.T, width uint32) string {
	t.Helper()
	_, digest := createBinFile(t, gguftest.KV{
		"general.architecture":    "gemma4",
		"general.file_type":       uint32(gguf.FileTypeF32),
		"gemma4.embedding_length": width,
	}, []*gguftest.Tensor{{Name: "blk.0.attn_q.weight", Type: gguf.TensorTypeF32, Shape: []uint64{1, 1}, WriterTo: bytes.NewReader(make([]byte, 4))}})
	return digest
}

// assistantHead is a Gemma 4 assistant head for a target of the given width.
// salt makes two heads of one width different files.
func assistantHead(t *testing.T, width uint32, salt byte) string {
	t.Helper()
	_, digest := createBinFile(t, gguftest.KV{
		"general.architecture":                  "gemma4-assistant",
		"general.file_type":                     uint32(gguf.FileTypeF32),
		"gemma4-assistant.embedding_length_out": width,
		"gemma4-assistant.requires_target_arch": "gemma4",
	}, []*gguftest.Tensor{{Name: "blk.0.attn_q.weight", Type: gguf.TensorTypeF32, Shape: []uint64{1, 1}, WriterTo: bytes.NewReader([]byte{salt, 0, 0, 0})}})
	return digest
}

func draftLayersOf(t *testing.T, name string) []manifest.Layer {
	t.Helper()
	mf, err := manifest.ParseNamedManifest(model.ParseName(name))
	if err != nil {
		t.Fatal(err)
	}
	var out []manifest.Layer
	for _, l := range mf.Layers {
		if l.MediaType == manifest.MediaTypeImageDraft {
			out = append(out, l)
		}
	}
	return out
}

// The whole life of a drafter set through the config: attached, replaced by
// another, detached. This is what `xollama tweak model --drafter` sends.
func TestDraftHeadAttachesReplacesAndDetaches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var s Server

	base := gemma4Base(t, 2816)
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "g4", Files: map[string]string{"g4.gguf": base}, Stream: &stream}); w.Code != http.StatusOK {
		t.Fatalf("create base: %d %s", w.Code, w.Body)
	}
	if n := len(draftLayersOf(t, "g4")); n != 0 {
		t.Fatalf("a new model has %d draft layers", n)
	}

	set := func(head string) *xollama.Config {
		t.Helper()
		w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "g4", From: "g4", Xollama: &xollama.Config{Draft: &xollama.Draft{Head: head}}, Stream: &stream})
		if w.Code != http.StatusOK {
			t.Fatalf("draft.head %s: %d %s", head, w.Code, w.Body)
		}
		m, err := GetModel("g4")
		if err != nil {
			t.Fatal(err)
		}
		return m.Xollama
	}

	first, second := assistantHead(t, 2816, 1), assistantHead(t, 2816, 2)

	cfg := set(first)
	if got := draftLayersOf(t, "g4"); len(got) != 1 || got[0].Digest != first {
		t.Fatalf("after attach: %+v, want the one head %s", got, first)
	}
	if cfg == nil || cfg.Draft == nil || cfg.Draft.Head != first {
		t.Fatalf("the stored config does not name the head: %+v", cfg)
	}

	set(second)
	if got := draftLayersOf(t, "g4"); len(got) != 1 || got[0].Digest != second {
		t.Fatalf("after a second attach: %+v, want only %s", got, second)
	}

	cfg = set(xollama.DraftHeadNone)
	if got := draftLayersOf(t, "g4"); len(got) != 0 {
		t.Fatalf("after detach: %+v, want none", got)
	}
	if cfg != nil && cfg.Draft != nil {
		t.Fatalf("a detach left %+v in the config", cfg.Draft)
	}
	m, _ := GetModel("g4")
	if m.DraftPath != "" {
		t.Fatalf("the model still launches with a drafter: %s", m.DraftPath)
	}
}

// A config that says nothing about the head leaves the drafter alone: a
// tweak of an unrelated setting must not cost the model its drafter.
func TestAConfigWithoutAHeadLeavesTheDrafterAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var s Server

	base, head := gemma4Base(t, 2816), assistantHead(t, 2816, 1)
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "g4", Files: map[string]string{"g4.gguf": base}, DraftFiles: map[string]string{"head.gguf": head}, Stream: &stream}); w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "g4", From: "g4", Xollama: &xollama.Config{FlashAttention: "on"}, Stream: &stream}); w.Code != http.StatusOK {
		t.Fatalf("tweak: %d %s", w.Code, w.Body)
	}
	if got := draftLayersOf(t, "g4"); len(got) != 1 || got[0].Digest != head {
		t.Fatalf("draft layers = %+v, want the Modelfile's %s", got, head)
	}
}

// A head for another size fails at load, deep in the engine. It is refused
// where it is attached, and the model keeps what it had.
func TestADraftHeadForAnotherSizeIsRefused(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var s Server

	base := gemma4Base(t, 2816)
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "g4", Files: map[string]string{"g4.gguf": base}, Stream: &stream}); w.Code != http.StatusOK {
		t.Fatalf("create base: %d %s", w.Code, w.Body)
	}
	wrong := assistantHead(t, 5376, 1)
	w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "g4", From: "g4", Xollama: &xollama.Config{Draft: &xollama.Draft{Head: wrong}}, Stream: &stream})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "5376 wide") || !strings.Contains(w.Body.String(), "2816 wide") {
		t.Fatalf("got %d %s, want a 400 naming both widths", w.Code, w.Body)
	}
	if n := len(draftLayersOf(t, "g4")); n != 0 {
		t.Fatalf("the refused head was attached (%d draft layers)", n)
	}
}

// show names the drafter published for a model that has none, and says so
// once it is attached; the size comes from the width, not from the name.
func TestShowNamesThePublishedDrafter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var s Server

	base := gemma4Base(t, 2816)
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "v9-agentic", Files: map[string]string{"g4.gguf": base}, Stream: &stream}); w.Code != http.StatusOK {
		t.Fatalf("create base: %d %s", w.Code, w.Body)
	}
	show := func() *api.DrafterInfo {
		t.Helper()
		w := createRequest(t, s.ShowHandler, api.ShowRequest{Model: "v9-agentic"})
		if w.Code != http.StatusOK {
			t.Fatalf("show: %d %s", w.Code, w.Body)
		}
		var resp api.ShowResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		return resp.Drafter
	}

	d := show()
	if d == nil || d.Source != api.DrafterSourceNone || d.Recommended == nil {
		t.Fatalf("drafter = %+v, want source none with a recommendation", d)
	}
	if want := "hf.co/ManniX-ITA/gemma-4-26B-A4B-it-assistant-GGUF/gemma-4-26B-A4B-it-assistant.Q8_0.gguf"; d.Recommended.Source != want || d.Recommended.Attached {
		t.Fatalf("recommended = %+v, want %s, not attached", d.Recommended, want)
	}

	head := assistantHead(t, 2816, 1)
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "v9-agentic", From: "v9-agentic", Xollama: &xollama.Config{Draft: &xollama.Draft{Head: head}}, Stream: &stream}); w.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", w.Code, w.Body)
	}
	d = show()
	if d == nil || d.Source != "attached" || d.Digest != head || d.SpecType != "draft-assistant" || d.Mismatch != "" {
		t.Fatalf("drafter = %+v, want the attached head under draft-assistant", d)
	}
	if d.Recommended == nil || !d.Recommended.Attached {
		t.Fatalf("recommended = %+v, want it marked attached", d.Recommended)
	}
	if d.Tokens != api.DefaultOptions().DraftNumPredict || d.TokensFrom != "default" {
		t.Fatalf("tokens = %d from %q, want the default", d.Tokens, d.TokensFrom)
	}
}

// The draft length, most specific first: the request or a PARAMETER, then
// the model's draft.tokens, then nothing.
func TestApplyDraftTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := func(v int) *int { return &v }
	with := func(tokens *int) *Model {
		return &Model{Xollama: &xollama.Config{Draft: &xollama.Draft{Tokens: tokens}}}
	}

	for _, tc := range []struct {
		name    string
		m       *Model
		in      int
		set     bool
		want    int
		wantSet bool
	}{
		{"nobody said", &Model{}, 4, false, 4, false},
		{"the model sets a length", with(n(2)), 4, false, 2, true},
		{"the model turns it off", with(n(0)), 4, false, 0, true},
		{"a parameter wins over the model", with(n(0)), 3, true, 3, true},
		{"a request turns it off", with(n(2)), 0, true, 0, true},
		{"no model", nil, 4, false, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := api.DefaultOptions()
			opts.DraftNumPredict = tc.in
			got := applyDraftTokens(tc.m, &opts, tc.set)
			if opts.DraftNumPredict != tc.want || got != tc.wantSet {
				t.Fatalf("draft_num_predict = %d, set %v; want %d, %v", opts.DraftNumPredict, got, tc.want, tc.wantSet)
			}
		})
	}
}

// Off is the model's own word: its PARAMETER, or with none its config. A
// model that says nothing is not off, which is what keeps a built-in head
// drafting on opencoti as it did.
func TestDraftTurnedOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := func(v int) *int { return &v }
	cfg := func(tokens *int) *xollama.Config { return &xollama.Config{Draft: &xollama.Draft{Tokens: tokens}} }

	for _, tc := range []struct {
		name string
		m    *Model
		want bool
	}{
		{"nothing said", &Model{}, false},
		{"draft.tokens 0", &Model{Xollama: cfg(n(0))}, true},
		{"draft.tokens 3", &Model{Xollama: cfg(n(3))}, false},
		{"PARAMETER draft_num_predict 0", &Model{Options: map[string]any{"draft_num_predict": float64(0)}}, true},
		{"a PARAMETER length over draft.tokens 0", &Model{Options: map[string]any{"draft_num_predict": float64(3)}, Xollama: cfg(n(0))}, false},
		{"no model", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := draftTurnedOff(tc.m); got != tc.want {
				t.Fatalf("off = %v, want %v", got, tc.want)
			}
			if tc.m != nil && llamaServerConfigForModel(tc.m).DraftOff != tc.want {
				t.Fatalf("the launch config does not carry it")
			}
		})
	}
}
