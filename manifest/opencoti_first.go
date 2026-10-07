package manifest

import (
	"strings"

	"github.com/ollama/ollama/envconfig"
)

// opencotiFirst is the `opencoti-first` hook of runnerPreferences: the order
// in which a manifest list's children are chosen, for a pull and for a model
// already on disk, when no runner was asked for.
//
// The owner's rule (2026-10-07): a model opencoti can serve never runs on MLX,
// on any platform. opencoti serves GGUF, so the GGUF children (llamacpp, then
// ggml) come first everywhere and MLX only takes a model that has no GGUF
// child. Upstream v0.40.0 puts MLX first on Apple silicon; without this hook
// `pull qwen3.5` on a Mac took the safetensors child and ran it on MLX
// (measured on v0.40.0-rc.1.xollama).
//
// Off means off: with XOLLAMA_ENGINE=llamacpp the fork's engine is not in
// play, and the order is upstream's, byte for byte. An explicit runner
// (`--runner mlx`) never reaches here: runnerPreferencesFor honours it.
func opencotiFirst() ([]string, bool) {
	if strings.EqualFold(strings.TrimSpace(envconfig.Engine()), "llamacpp") {
		return nil, false
	}
	return []string{RunnerLlamaCPP, RunnerGGML, RunnerMLX}, true
}
