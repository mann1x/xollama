package llm

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// ab-3 ran a council's two researchers back to back: -np 1, and the engine
// deferred the second worker for a slot (opencoti #501). A council starts
// with every slot the engine allows live -- its parallel ceiling, never fewer
// than its widest step -- and its pool is not widened by them.
func TestACouncilStartsWithTheEnginesParallelSlots(t *testing.T) {
	t.Setenv("XOLLAMA_MAX_PARALLEL", "")
	t.Setenv("XOLLAMA_DYNAMIC_SLOTS", "")
	args := []string{"--model", "m", "-c", "16384", "-np", "1"}
	cfg := LlamaServerConfig{CouncilSlots: 2}

	live := councilLive(cfg, 1, true)
	if live != defaultMaxParallel {
		t.Fatalf("live %d, want the engine's ceiling %d", live, defaultMaxParallel)
	}
	got := councilSlotArgs(slices.Clone(args), live, 1)
	if i := slices.Index(got, "-np"); got[i+1] != "4" {
		t.Errorf("-np %s, want 4: %v", got[i+1], got)
	}
	if i := slices.Index(got, "-c"); got[i+1] != "16384" {
		t.Errorf("-c %s: the workers are charged to the owner, the pool must not grow", got[i+1])
	}
	if !slices.Contains(got, "--kv-unified") {
		t.Errorf("several slots over one -c need one shared pool: %v", got)
	}

	// The model's ceiling is the council's; a width above it still stands.
	cfg.Xollama = &xollama.Config{Slots: &xollama.Slots{Max: 6}}
	if live := councilLive(cfg, 1, true); live != 6 {
		t.Errorf("slots.max 6: live %d", live)
	}
	cfg.Xollama, cfg.CouncilSlots = nil, 5
	if live := councilLive(cfg, 1, true); live != 5 {
		t.Errorf("width 5 over ceiling 4: live %d", live)
	}
	// Without elastic slots there is no ceiling: the width.
	t.Setenv("XOLLAMA_DYNAMIC_SLOTS", "0")
	cfg.CouncilSlots = 2
	if live := councilLive(cfg, 1, true); live != 2 {
		t.Errorf("static slots: live %d, want the width", live)
	}
}

func TestCouncilSlotsLeaveEveryOtherLaunchAlone(t *testing.T) {
	no := false
	for name, tc := range map[string]struct {
		cfg      LlamaServerConfig
		parallel int
		opencoti bool
	}{
		"not a council":          {LlamaServerConfig{}, 1, true},
		"stock llama.cpp":        {LlamaServerConfig{CouncilSlots: 2}, 1, false},
		"already at the ceiling": {LlamaServerConfig{CouncilSlots: 2}, 4, true},
		"held to one sequence":   {LlamaServerConfig{CouncilSlots: 2, SingleSequenceOnly: true}, 1, true},
		"cells split per slot":   {LlamaServerConfig{CouncilSlots: 2, Xollama: &xollama.Config{KV: &xollama.KV{Unified: &no}}}, 1, true},
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

// eleven2go (2026-09-28): a council with PolyKV pool seats launched with
// "--kv-unified --kv-unified" -- councilSlotArgs and appendSlotArgs each wrote
// it. Whichever says it first, the argv carries it once.
func TestACouncilWithPoolsSaysKVUnifiedOnce(t *testing.T) {
	args := councilSlotArgs([]string{"--model", "m", "-c", "16384", "-np", "1"}, 2, 1)
	yes := true
	for name, unified := range map[string]*bool{"derived from the pools": nil, "stated by the model": &yes} {
		got := appendSlotArgs(slices.Clone(args), slotPlan{Live: 2, Max: 4, Dynamic: true}, 6, unified, true)
		n := 0
		for _, a := range got {
			if a == "--kv-unified" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: --kv-unified %d times: %v", name, n, got)
		}
	}
}
