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
		{"unstated", "opencoti", &Model{}, true, 2},
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
