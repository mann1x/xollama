package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// audio.cpp reads a path without ".gguf" as a safetensors package, so its
// GGUF speech model must reach it under a .gguf name that resolves to the blob.
func TestAnAudioCppSpeechModelIsPassedUnderAGGUFName(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	model := mediaDigest(t, []byte("GGUF\x03\x00\x00\x00kokoro"))
	vocoder := mediaDigest(t, []byte("GGUF\x03\x00\x00\x00vocoder"))
	m := &xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: model, Vocoder: vocoder}}
	path := mediaComponentPath(m)

	got := path(model)
	if !strings.HasSuffix(got, ".gguf") || filepath.Dir(got) == filepath.Dir(blobPath(model)) {
		t.Fatalf("speech model passed as %q, want a .gguf name outside the blobs", got)
	}
	// The name must be the file's own: audio.cpp resolves a symlink and
	// judges the blob's name.
	li, err := os.Lstat(got)
	if err != nil || li.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is a symlink or missing (%v), want the blob's own name", got, err)
	}
	bi, _ := os.Stat(blobPath(model))
	if !os.SameFile(li, bi) {
		t.Fatalf("%s is not the blob", got)
	}
	// Asked again (every engine start), the same link.
	if again := path(model); again != got {
		t.Fatalf("second start got %q, first %q", again, got)
	}
	// Only the speech model: other components keep their blob path.
	if p := path(vocoder); p != blobPath(vocoder) {
		t.Fatalf("vocoder passed as %q, want its blob", p)
	}
}

func TestOnlyAudioCppAndOnlyGGUFAreRenamed(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	gguf := mediaDigest(t, []byte("GGUF\x03\x00\x00\x00outetts"))
	other := mediaDigest(t, []byte("PK\x03\x04not a gguf"))

	// OuteTTS (engine "" or "outetts") reads the magic: blob path as before.
	oute := mediaComponentPath(&xollama.Media{TTS: &xollama.TTSMedia{Model: gguf}})
	if p := oute(gguf); p != blobPath(gguf) {
		t.Fatalf("outetts model passed as %q, want its blob", p)
	}
	// An audio.cpp model that is not GGUF is left alone.
	acpp := mediaComponentPath(&xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: other}})
	if p := acpp(other); p != blobPath(other) {
		t.Fatalf("non-GGUF audio.cpp model passed as %q, want its blob", p)
	}
}

// A removed model's blob is pruned; its name must not keep the space.
func TestANameGoesWithItsBlob(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	gone := mediaDigest(t, []byte("GGUF\x03\x00\x00\x00removed"))
	kept := mediaDigest(t, []byte("GGUF\x03\x00\x00\x00kept"))
	acpp := func(d string) func(string) string {
		return mediaComponentPath(&xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: d}})
	}
	goneName := acpp(gone)(gone)
	if err := os.Remove(blobPath(gone)); err != nil {
		t.Fatal(err)
	}
	acpp(kept)(kept)
	if _, err := os.Lstat(goneName); !os.IsNotExist(err) {
		t.Fatalf("%s outlived its blob (%v)", goneName, err)
	}
}
