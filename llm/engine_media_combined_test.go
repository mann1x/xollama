package llm

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func TestMediaFlagsGoOnlyToAnOpencotiLaunch(t *testing.T) {
	cfg := LlamaServerConfig{MediaArgs: []string{"--tts-model", "/blob"}}
	if got := mediaArgsFor([]string{"-c", "4096"}, cfg, false); slices.Contains(got, "--tts-model") {
		t.Errorf("stock llama.cpp was given media flags: %v", got)
	}
	if got := mediaArgsFor([]string{"-c", "4096"}, cfg, true); !slices.Contains(got, "--tts-model") {
		t.Errorf("opencoti lost its media flags: %v", got)
	}
}

func TestMediaOnTheCPUBooksNoGPUMemory(t *testing.T) {
	size := func(string) int64 { return 1 << 30 }
	path := func(d string) string { return "/blobs/" + d }
	cpu := &xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: "sha256:a"}}
	if args, est := CombinedMedia(cpu, path, size); len(args) == 0 || est != 0 {
		t.Errorf("audio.cpp: args %v, estimate %d", args, est)
	}
	gpu := &xollama.Media{Image: &xollama.ImageMedia{Model: "sha256:a"}}
	if _, est := CombinedMedia(gpu, path, size); est < 1<<30 {
		t.Errorf("an image model priced at %d", est)
	}
}

func TestAMissingEngineFeatureIsRefusedByName(t *testing.T) {
	m := &xollama.Media{Image: &xollama.ImageMedia{Model: "sha256:a"}}
	if err := CheckMediaFeatures(m, nil); err == nil {
		t.Error("an engine with no image feature was accepted")
	}
	if err := CheckMediaFeatures(m, MediaFeatures(m)); err != nil {
		t.Error(err)
	}
}
