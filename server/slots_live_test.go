package server

import (
	"testing"

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
