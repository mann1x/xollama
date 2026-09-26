package llm

import (
	"testing"

	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/types/xollama"
)

// slidingModelKV is a model that keeps a short cache beside the global one.
// The numbers are small and exact so a surcharge can be checked by hand:
// 4 layers x 2 kv heads x (256/4) head dim x K and V x 2 bytes = 2048 B/cell.
func slidingModelKV(window uint32) gguftest.KV {
	kv := gguftest.KV{
		"general.architecture":            "gemma3",
		"gemma3.block_count":              uint32(4),
		"gemma3.embedding_length":         uint32(256),
		"gemma3.context_length":           uint32(8192),
		"gemma3.attention.head_count":     uint32(4),
		"gemma3.attention.head_count_kv":  uint32(2),
		"gemma3.attention.sliding_window": window,
	}
	if window == 0 {
		delete(kv, "gemma3.attention.sliding_window")
	}
	return kv
}

const slidingModelCellBytes = 2 * 4 * 2 * 64 * 2

// TestSlidingWindowCellsMirrorsLlamaCpp pins the sizing to the line it copies,
//
//	GGML_PAD(std::min(size_base, n_swa*(unified ? n_seq_max : 1) + n_ubatch), 256)
//
// including the order of the clamp and the padding. The last case is the one
// that tells the two orders apart: padding first would round 5000 up to 5120
// and then clamp to 4000, where llama.cpp clamps to 4000 and pads to 4096.
func TestSlidingWindowCellsMirrorsLlamaCpp(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		window, batch, numCtxSeq, seqs int
		want                           int
	}{
		{"no window means no cache", 0, 512, 4096, 4, 0},
		{"one sequence", 512, 512, 4096, 1, 1024},
		{"four sequences multiply the window", 512, 512, 4096, 4, 2560},
		{"clamped to the context", 1024, 512, 4096, 4, 4096},
		{"padded up to a whole page", 500, 0, 4096, 1, 512},
		{"clamped first, padded after", 5000, 0, 4000, 1, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := slidingWindowCells(tc.window, tc.batch, tc.numCtxSeq, tc.seqs); got != tc.want {
				t.Errorf("slidingWindowCells(%d, %d, %d, %d) = %d, want %d",
					tc.window, tc.batch, tc.numCtxSeq, tc.seqs, got, tc.want)
			}
		})
	}
}

// TestSlotCeilingIsFreeWithoutASlidingWindow is the half of the user-visible
// claim that says four slots sharing a pool cost what one slot cost. On a model
// with only the global cache that is exact, because --kv-unified trades
// n_seq_max streams of n_ctx/n_seq_max cells for one stream of n_ctx.
func TestSlotCeilingIsFreeWithoutASlidingWindow(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f := loadTestGGUF(t, slidingModelKV(0))
	if got := PredictServerSlotVRAM(f, LlamaServerConfig{}, nil, 4096, 512, 1); got != 0 {
		t.Errorf("surcharge = %d bytes, want 0: a shared pool is the same size as the split it replaced", got)
	}
}

// TestSlotCeilingChargesTheSlidingWindow is the other half: "almost the same"
// is not the same, and this is where the difference lives.
func TestSlotCeilingChargesTheSlidingWindow(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	for _, tc := range []struct {
		name      string
		window    int
		wantCells int
	}{
		// 512*4+512 = 2560 cells for the ceiling, 512+512 = 1024 for one slot.
		{"window that fits", 512, 2560 - 1024},
		// 1024*4+512 = 4608 clamps to the 4096-cell context; one slot needs
		// 1024+512 = 1536.
		{"window that clamps", 1024, 4096 - 1536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := loadTestGGUF(t, slidingModelKV(uint32(tc.window)))

			want := uint64(tc.wantCells) * slidingModelCellBytes
			if got := PredictServerSlotVRAM(f, LlamaServerConfig{}, nil, 4096, 512, 1); got != want {
				t.Errorf("surcharge = %d bytes, want %d", got, want)
			}
		})
	}
}

// TestSlotCeilingIsBoundedByOneContext holds the guarantee that lets every
// layer be counted rather than only the sliding-window ones: the short cache is
// clamped to the context, so however large the declared window, the surcharge
// cannot exceed one extra context's worth of cells.
//
// The largest window here is also the case where the charge falls back to zero,
// and correctly so: a window past the context means the short cache is already
// the whole context at one slot, and a ceiling cannot grow what is full.
func TestSlotCeilingIsBoundedByOneContext(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	const numCtxSeq = 4096
	limit := uint64(numCtxSeq) * slidingModelCellBytes

	var charged bool
	for _, window := range []int{128, 512, 2000, 4096, 1 << 20} {
		f := loadTestGGUF(t, slidingModelKV(uint32(window)))

		got := PredictServerSlotVRAM(f, LlamaServerConfig{}, nil, numCtxSeq, 512, 1)
		if got > limit {
			t.Errorf("window %d: surcharge = %d bytes, want at most one context's worth (%d)", window, got, limit)
		}
		charged = charged || got > 0
	}
	if !charged {
		t.Error("no window was charged anything; the bound is being held vacuously")
	}
}

// TestSlotCeilingFollowsTheResolvedPlan checks the surcharge is driven by the
// same plan that builds the argv, through each plane that can set it.
func TestSlotCeilingFollowsTheResolvedPlan(t *testing.T) {
	no, yes := false, true

	for _, tc := range []struct {
		name    string
		env     map[string]string
		cfg     LlamaServerConfig
		wantPos bool
	}{
		{
			name:    "default ceiling of four",
			env:     map[string]string{"XOLLAMA_ENGINE": "opencoti"},
			wantPos: true,
		},
		{
			// Off means off: with the engine pinned to stock llama.cpp nothing
			// here may move, because appendSlotArgs passes no flag there.
			name: "engine pinned to llamacpp",
			env:  map[string]string{"XOLLAMA_ENGINE": "llamacpp"},
		},
		{
			name: "engine pinned to llamacpp by the model",
			env:  map[string]string{"XOLLAMA_ENGINE": "opencoti"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Engine: xollama.EngineLlamaCpp}},
		},
		{
			name: "dynamic slots switched off in the environment",
			env:  map[string]string{"XOLLAMA_ENGINE": "opencoti", "XOLLAMA_DYNAMIC_SLOTS": "false"},
		},
		{
			name: "dynamic slots switched off by the model",
			env:  map[string]string{"XOLLAMA_ENGINE": "opencoti"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Slots: &xollama.Slots{Dynamic: &no}}},
		},
		{
			// An embedding model never grows, so there is nothing extra to
			// pay for.
			name: "embedding model held to a single sequence",
			env:  map[string]string{"XOLLAMA_ENGINE": "opencoti"},
			cfg:  LlamaServerConfig{SingleSequenceOnly: true},
		},
		{
			// ollama/ollama#4165 is stock llama.cpp's deny-list: on opencoti
			// the architecture grows like any other.
			name:    "deny-listed architecture on opencoti",
			env:     map[string]string{"XOLLAMA_ENGINE": "opencoti"},
			cfg:     LlamaServerConfig{SingleSequenceOnly: true, SingleSequenceStockOnly: true},
			wantPos: true,
		},
		{
			name: "deny-listed architecture on llamacpp",
			env:  map[string]string{"XOLLAMA_ENGINE": "llamacpp"},
			cfg:  LlamaServerConfig{SingleSequenceOnly: true, SingleSequenceStockOnly: true},
		},
		{
			name:    "ceiling raised by the model",
			env:     map[string]string{"XOLLAMA_ENGINE": "opencoti"},
			cfg:     LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Slots: &xollama.Slots{Dynamic: &yes, Max: 8}}},
			wantPos: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			f := loadTestGGUF(t, slidingModelKV(512))

			got := PredictServerSlotVRAM(f, tc.cfg, nil, 4096, 512, 1)
			if tc.wantPos && got == 0 {
				t.Error("surcharge = 0, want a charge: this plan raises the ceiling")
			}
			if !tc.wantPos && got != 0 {
				t.Errorf("surcharge = %d bytes, want 0: this plan does not raise the ceiling", got)
			}
		})
	}
}

// TestSlotCeilingGrowsWithTheCeiling is the property the prediction exists for:
// a higher ceiling costs more, so an estimate made against the live count alone
// describes only the request that spawned the runner.
func TestSlotCeilingGrowsWithTheCeiling(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f := loadTestGGUF(t, slidingModelKV(256))
	dynamic := true

	var previous uint64
	for _, maximum := range []int{2, 4, 8} {
		cfg := LlamaServerConfig{Xollama: &xollama.Config{
			Version: 1,
			Slots:   &xollama.Slots{Dynamic: &dynamic, Max: maximum},
		}}
		got := PredictServerSlotVRAM(f, cfg, nil, 8192, 512, 1)
		if got <= previous {
			t.Errorf("ceiling %d: surcharge = %d bytes, want more than the %d of the ceiling below it", maximum, got, previous)
		}
		previous = got
	}
}

// TestSlotCeilingCountsReservedPools is the correction opencoti supplied:
// n_seq_max is common_n_parallel_max(params) + polykv_max_pools, so a reserved
// pool id costs a sequence's worth of sliding window exactly as a slot does.
// Leaving pools out under-counts a pooled load by that much.
func TestSlotCeilingCountsReservedPools(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f := loadTestGGUF(t, slidingModelKV(256))
	dynamic, pooled := true, true

	plain := LlamaServerConfig{Xollama: &xollama.Config{
		Version: 1,
		Slots:   &xollama.Slots{Dynamic: &dynamic, Max: 4},
	}}
	withPools := LlamaServerConfig{Xollama: &xollama.Config{
		Version: 1,
		Slots:   &xollama.Slots{Dynamic: &dynamic, Max: 4},
		Session: &xollama.Session{Pool: &pooled, MaxPools: 2},
	}}

	without := PredictServerSlotVRAM(f, plain, nil, 8192, 512, 1)
	with := PredictServerSlotVRAM(f, withPools, nil, 8192, 512, 1)
	if with <= without {
		t.Errorf("pooled prediction %d is not above the unpooled %d; reserved pools cost sequences too", with, without)
	}
}

// TestSlotCeilingChargesPoolsWithoutDynamicSlots covers the load that has no
// elastic slots at all: pooling alone still raises n_seq_max, so it still costs.
func TestSlotCeilingChargesPoolsWithoutDynamicSlots(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	t.Setenv("XOLLAMA_DYNAMIC_SLOTS", "false")

	f := loadTestGGUF(t, slidingModelKV(256))
	pooled := true
	cfg := LlamaServerConfig{Xollama: &xollama.Config{
		Version: 1,
		Session: &xollama.Session{Pool: &pooled, MaxPools: 2},
	}}

	if got := PredictServerSlotVRAM(f, cfg, nil, 8192, 512, 1); got == 0 {
		t.Error("surcharge = 0, want a charge: two reserved pools are two more sequences")
	}
}

// TestAStockLaunchServesTheDenyListOneSequence is the other half of the
// ollama/ollama#4165 exemption: the scheduler lifts the rule on a prediction,
// and a launch that lands on stock llama.cpp anyway must take it back.
func TestAStockLaunchServesTheDenyListOneSequence(t *testing.T) {
	denied := LlamaServerConfig{SingleSequenceOnly: true, SingleSequenceStockOnly: true}
	embedding := LlamaServerConfig{SingleSequenceOnly: true}

	for _, tc := range []struct {
		name     string
		cfg      LlamaServerConfig
		opencoti bool
		want     int
	}{
		{"deny-listed on opencoti", denied, true, 4},
		{"deny-listed fell back to stock", denied, false, 1},
		{"embedding on opencoti", embedding, true, 1},
		{"unrestricted on stock", LlamaServerConfig{}, false, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := servedSequences(tc.cfg, 4, tc.opencoti); got != tc.want {
				t.Errorf("servedSequences = %d, want %d", got, tc.want)
			}
		})
	}
}
