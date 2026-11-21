package server

import (
	"testing"

	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// slots.live replaces the server's count only where opencoti serves the load.
func TestLiveSlotsBindOnlyOnOpencoti(t *testing.T) {
	live := &Model{Xollama: &xollama.Config{Version: 1, Slots: &xollama.Slots{Live: 3}}}
	for _, tc := range []struct {
		name       string
		engine     string
		m          *Model
		completion bool
		want       int
	}{
		{"opencoti", "opencoti", live, true, 3},
		{"stock llama.cpp", "llamacpp", live, true, 2},
		{"embedding", "opencoti", live, false, 2},
		{"unstated: OLLAMA_NUM_PARALLEL is not opencoti's", "opencoti", &Model{}, true, 1},
		{"pinned to llama.cpp", "opencoti", &Model{Xollama: &xollama.Config{Version: 1, Engine: xollama.EngineLlamaCpp, Slots: &xollama.Slots{Live: 3}}}, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XOLLAMA_ENGINE", tc.engine)
			if got := liveSlots(tc.m, nil, tc.completion, 2); got != tc.want {
				t.Errorf("liveSlots = %d, want %d", got, tc.want)
			}
		})
	}
}

// On opencoti the floor comes from XOLLAMA_PARALLEL, never OLLAMA_NUM_PARALLEL
// (which a stock ollama on the same machine shares); a model's slots.live
// still wins, and stock llama.cpp keeps ollama's count.
func TestXollamaParallelReplacesNumParallelOnOpencoti(t *testing.T) {
	live := &Model{Xollama: &xollama.Config{Version: 1, Slots: &xollama.Slots{Live: 3}}}
	for _, tc := range []struct {
		name     string
		engine   string
		parallel string
		m        *Model
		want     int
	}{
		{"opencoti takes XOLLAMA_PARALLEL", "opencoti", "4", &Model{}, 4},
		{"the model's slots.live wins", "opencoti", "4", live, 3},
		{"stock keeps ollama's count", "llamacpp", "4", &Model{}, 2},
		{"unset is one", "opencoti", "", &Model{}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XOLLAMA_ENGINE", tc.engine)
			t.Setenv("XOLLAMA_PARALLEL", tc.parallel)
			t.Setenv("OLLAMA_NUM_PARALLEL", "2")
			if got := liveSlots(tc.m, nil, true, 2); got != tc.want {
				t.Errorf("liveSlots = %d, want %d", got, tc.want)
			}
		})
	}
}

// An MLX model never runs on opencoti, so it keeps ollama's count: since
// upstream v0.40.17 (#19197) the MLX runner generates concurrently on it, and
// the opencoti floor (XOLLAMA_PARALLEL, default one) made every MLX load serial.
func TestAnMLXModelKeepsOllamasParallel(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	t.Setenv("XOLLAMA_PARALLEL", "")
	mlx := &Model{Config: model.ConfigV2{ModelFormat: "safetensors"}}
	if got := liveSlots(mlx, nil, true, 2); got != 2 {
		t.Errorf("liveSlots(MLX) = %d, want ollama's 2", got)
	}
	gguf := &Model{Config: model.ConfigV2{ModelFormat: "gguf"}}
	if got := liveSlots(gguf, nil, true, 2); got != 1 {
		t.Errorf("liveSlots(GGUF on opencoti) = %d, want XOLLAMA_PARALLEL's floor 1", got)
	}
}
