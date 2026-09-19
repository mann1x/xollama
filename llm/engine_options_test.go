package llm

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

func flashCfg(mode string) LlamaServerConfig {
	return LlamaServerConfig{Xollama: &xollama.Config{Version: 1, FlashAttention: mode}}
}

// TestFlashAttentionPerModel is the point of moving this into the model: whether
// flash attention helps or breaks is a property of the model and its cache, and
// upstream can only answer once for the whole server.
func TestFlashAttentionPerModel(t *testing.T) {
	// A device list that supports it, so "auto" is distinguishable from "off".
	gpus := []ml.DeviceInfo{}

	for _, tc := range []struct {
		name string
		env  map[string]string
		cfg  LlamaServerConfig
		want []string
	}{
		{
			name: "the model turns it on where the server left it alone",
			cfg:  flashCfg("on"),
			want: []string{"--flash-attn", "on"},
		},
		{
			// The case that matters most: a server that switched it on globally
			// must not force it onto a model that cannot take it.
			name: "the model turns it off where the server turned it on",
			env:  map[string]string{"OLLAMA_FLASH_ATTENTION": "1"},
			cfg:  flashCfg("off"),
			want: []string{"--flash-attn", "off"},
		},
		{
			name: "the model turns it on where the server turned it off",
			env:  map[string]string{"OLLAMA_FLASH_ATTENTION": "0"},
			cfg:  flashCfg("on"),
			want: []string{"--flash-attn", "on"},
		},
		{
			// "auto" means decide for me, not force on. Against devices that
			// support it that is auto -- and it still overrides a server that
			// had forced it on, which is the point of saying it.
			name: "the model asks to be decided for",
			env:  map[string]string{"OLLAMA_FLASH_ATTENTION": "1"},
			cfg:  flashCfg("auto"),
			want: []string{"--flash-attn", "auto"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := appendFlashAttentionArgs(nil, tc.cfg, gpus); !slices.Equal(got, tc.want) {
				t.Errorf("args = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFlashAttentionAutoStillDefersToTheDevices proves "auto" is a request to
// be decided for rather than a request to switch on: against a device that
// cannot do it, the answer is still off.
func TestFlashAttentionAutoStillDefersToTheDevices(t *testing.T) {
	t.Setenv("OLLAMA_FLASH_ATTENTION", "1")

	// An old CUDA card: supported by the backend, but not for this.
	unsupported := []ml.DeviceInfo{{DeviceID: ml.DeviceID{Library: "CUDA"}, ComputeMajor: 5, ComputeMinor: 0}}
	if ml.FlashAttentionSupported(unsupported) {
		t.Skip("this device list is considered capable; the fixture needs updating")
	}

	got := appendFlashAttentionArgs(nil, flashCfg("auto"), unsupported)
	if !slices.Equal(got, []string{"--flash-attn", "off"}) {
		t.Errorf("args = %v, want --flash-attn off", got)
	}
}

// TestFlashAttentionWithoutAModelOpinionIsUpstreams is the off path: a model
// that says nothing must produce exactly what upstream produces.
func TestFlashAttentionWithoutAModelOpinionIsUpstreams(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{"OLLAMA_FLASH_ATTENTION": "1"},
		{"OLLAMA_FLASH_ATTENTION": "0"},
	} {
		for k, v := range env {
			t.Setenv(k, v)
		}
		gpus := []ml.DeviceInfo{}

		want := appendFlashAttentionArgs(nil, LlamaServerConfig{}, gpus)
		got := appendFlashAttentionArgs(nil, flashCfg(""), gpus)
		if !slices.Equal(got, want) {
			t.Errorf("env %v: a model with no opinion produced %v, want upstream's %v", env, got, want)
		}
	}
}

func TestResolveSWASeqBudget(t *testing.T) {
	off := false

	for _, tc := range []struct {
		name string
		env  map[string]string
		cfg  LlamaServerConfig
		want int
	}{
		{name: "unset"},
		{
			name: "the environment sets it",
			env:  map[string]string{"XOLLAMA_SWA_SEQ_BUDGET": "3"},
			want: 3,
		},
		{
			name: "the model sets it",
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Slots: &xollama.Slots{SWASeqBudget: 2}}},
			want: 2,
		},
		{
			name: "the model overrides the environment",
			env:  map[string]string{"XOLLAMA_SWA_SEQ_BUDGET": "3"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Slots: &xollama.Slots{SWASeqBudget: 8}}},
			want: 8,
		},
		{
			// The budget sizes a cache, not the slot mechanism. A sliding
			// window model with fixed slots still keeps one window per
			// sequence, so switching dynamic slots off must not discard it.
			name: "it survives dynamic slots being off",
			env:  map[string]string{"XOLLAMA_SWA_SEQ_BUDGET": "3", "XOLLAMA_DYNAMIC_SLOTS": "false"},
			want: 3,
		},
		{
			name: "it survives a model that switched dynamic slots off",
			env:  map[string]string{"XOLLAMA_SWA_SEQ_BUDGET": "3"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Slots: &xollama.Slots{Dynamic: &off}}},
			want: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := resolveSlotPlan(tc.cfg, 1, false).SWASeqBudget; got != tc.want {
				t.Errorf("SWASeqBudget = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAppendSWABudgetArgs(t *testing.T) {
	if got := appendSWABudgetArgs(nil, slotPlan{SWASeqBudget: 3}, true); !slices.Equal(got, []string{"--swa-seq-budget", "3"}) {
		t.Errorf("args = %v, want --swa-seq-budget 3", got)
	}
	if got := appendSWABudgetArgs(nil, slotPlan{SWASeqBudget: 3}, false); len(got) != 0 {
		t.Errorf("stock llama.cpp has no such flag, got %v", got)
	}
	if got := appendSWABudgetArgs(nil, slotPlan{}, true); len(got) != 0 {
		t.Errorf("no budget must add nothing, got %v", got)
	}
}

// TestSWABudgetLowersThePrediction ties the setting to the estimate. A budget
// that the prediction ignored would make the setting look like it did nothing:
// the memory would be freed and the scheduler would go on refusing to use it.
func TestSWABudgetLowersThePrediction(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f := loadTestGGUF(t, slidingModelKV(512))
	dynamic := true

	plain := LlamaServerConfig{Xollama: &xollama.Config{
		Version: 1,
		Slots:   &xollama.Slots{Dynamic: &dynamic, Max: 8},
	}}
	budgeted := LlamaServerConfig{Xollama: &xollama.Config{
		Version: 1,
		Slots:   &xollama.Slots{Dynamic: &dynamic, Max: 8, SWASeqBudget: 2},
	}}

	full := PredictServerSlotVRAM(f, plain, nil, 8192, 512, 1)
	trimmed := PredictServerSlotVRAM(f, budgeted, nil, 8192, 512, 1)
	if full == 0 {
		t.Fatal("the unbudgeted prediction is zero; the fixture charges nothing")
	}
	if trimmed >= full {
		t.Errorf("budgeted prediction %d is not below the unbudgeted %d", trimmed, full)
	}
}

// TestSWABudgetAboveTheSequenceCountIsClamped mirrors the engine, which clamps
// a budget above n_seq_max. Predicting an unclamped budget would over-count.
func TestSWABudgetAboveTheSequenceCountIsClamped(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")

	f := loadTestGGUF(t, slidingModelKV(512))
	dynamic := true

	plain := LlamaServerConfig{Xollama: &xollama.Config{
		Version: 1,
		Slots:   &xollama.Slots{Dynamic: &dynamic, Max: 4},
	}}
	huge := LlamaServerConfig{Xollama: &xollama.Config{
		Version: 1,
		Slots:   &xollama.Slots{Dynamic: &dynamic, Max: 4, SWASeqBudget: 999},
	}}

	if got, want := PredictServerSlotVRAM(f, huge, nil, 8192, 512, 1), PredictServerSlotVRAM(f, plain, nil, 8192, 512, 1); got != want {
		t.Errorf("budget above the sequence count predicted %d, want the unbudgeted %d", got, want)
	}
}
