package llm

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// ab-3 ran a council's two researchers back to back: -np 1, and the engine
// deferred the second worker for a slot (opencoti #501). A council starts
// with a slot per parallel member, and its pool is not widened by them.
func TestACouncilStartsWithASlotPerParallelMember(t *testing.T) {
	args := []string{"--model", "m", "-c", "16384", "-np", "1"}
	cfg := LlamaServerConfig{CouncilSlots: 2}

	live := councilLive(cfg, 1, true)
	if live != 2 {
		t.Fatalf("live %d, want 2", live)
	}
	got := councilSlotArgs(slices.Clone(args), live, 1)
	if i := slices.Index(got, "-np"); got[i+1] != "2" {
		t.Errorf("-np %s, want 2: %v", got[i+1], got)
	}
	if i := slices.Index(got, "-c"); got[i+1] != "16384" {
		t.Errorf("-c %s: the workers are charged to the owner, the pool must not grow", got[i+1])
	}
	if !slices.Contains(got, "--kv-unified") {
		t.Errorf("two slots over one -c need one shared pool: %v", got)
	}
}

func TestCouncilSlotsLeaveEveryOtherLaunchAlone(t *testing.T) {
	no := false
	for name, tc := range map[string]struct {
		cfg      LlamaServerConfig
		parallel int
		opencoti bool
	}{
		"not a council":        {LlamaServerConfig{}, 1, true},
		"stock llama.cpp":      {LlamaServerConfig{CouncilSlots: 2}, 1, false},
		"already wide enough":  {LlamaServerConfig{CouncilSlots: 2}, 4, true},
		"held to one sequence": {LlamaServerConfig{CouncilSlots: 2, SingleSequenceOnly: true}, 1, true},
		"cells split per slot": {LlamaServerConfig{CouncilSlots: 2, Xollama: &xollama.Config{KV: &xollama.KV{Unified: &no}}}, 1, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := councilLive(tc.cfg, tc.parallel, tc.opencoti); got != tc.parallel {
				t.Errorf("live %d, want the scheduler's %d", got, tc.parallel)
			}
			args := []string{"-c", "8192", "-np", "1"}
			if got := councilSlotArgs(slices.Clone(args), tc.parallel, tc.parallel); !slices.Equal(got, args) {
				t.Errorf("argv changed: %v", got)
			}
		})
	}
}
