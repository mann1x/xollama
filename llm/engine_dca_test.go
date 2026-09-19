package llm

import (
	"slices"
	"strings"
	"testing"

	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/types/xollama"
)

func TestResolveDCAPlan(t *testing.T) {
	on, off := true, false

	for _, tc := range []struct {
		name string
		env  map[string]string
		cfg  LlamaServerConfig
		want dcaPlan
	}{
		{
			name: "off is the default",
			want: dcaPlan{},
		},
		{
			name: "the environment turns it on",
			env:  map[string]string{"XOLLAMA_DCA": "1"},
			want: dcaPlan{Enabled: true},
		},
		{
			name: "the environment sets a chunk size",
			env:  map[string]string{"XOLLAMA_DCA": "1", "XOLLAMA_DCA_CHUNK_SIZE": "4096"},
			want: dcaPlan{Enabled: true, ChunkSize: 4096},
		},
		{
			name: "the model turns it on where the environment did not",
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, DCA: &xollama.DCA{Enabled: &on}}},
			want: dcaPlan{Enabled: true},
		},
		{
			// The model is the more specific plane and wins, which is the whole
			// point of it: one server, different answers per model.
			name: "the model turns it off where the environment turned it on",
			env:  map[string]string{"XOLLAMA_DCA": "1"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, DCA: &xollama.DCA{Enabled: &off}}},
			want: dcaPlan{},
		},
		{
			name: "the model overrides the chunk size",
			env:  map[string]string{"XOLLAMA_DCA": "1", "XOLLAMA_DCA_CHUNK_SIZE": "4096"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, DCA: &xollama.DCA{ChunkSize: 8192}}},
			want: dcaPlan{Enabled: true, ChunkSize: 8192},
		},
		{
			// A chunk size left over from the environment must not survive the
			// model switching the route off; there is nothing for it to size.
			name: "switching it off drops the chunk size with it",
			env:  map[string]string{"XOLLAMA_DCA": "1", "XOLLAMA_DCA_CHUNK_SIZE": "4096"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, DCA: &xollama.DCA{Enabled: &off}}},
			want: dcaPlan{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := resolveDCAPlan(tc.cfg); got != tc.want {
				t.Errorf("resolveDCAPlan() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAppendDCAArgs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		plan         dcaPlan
		arch         string
		numCtx       int
		trainCtx     int
		usedOpencoti bool
		want         []string
	}{
		{
			name:         "off adds nothing",
			arch:         "qwen3",
			numCtx:       1 << 20,
			trainCtx:     32768,
			usedOpencoti: true,
		},
		{
			// The gate is absolute. Nothing about this may reach a stock
			// launch, which has no flag for any of it.
			name:         "stock llama.cpp gets nothing",
			plan:         dcaPlan{Enabled: true},
			arch:         "qwen3",
			numCtx:       65536,
			trainCtx:     32768,
			usedOpencoti: false,
		},
		{
			// Within the trained window the chunked route is all that was
			// asked for. Stretching positions that already fit would change
			// the answers for no reason.
			name:         "within the trained context, only the route",
			plan:         dcaPlan{Enabled: true},
			arch:         "qwen3",
			numCtx:       16384,
			trainCtx:     32768,
			usedOpencoti: true,
			want:         []string{"--dca", "on"},
		},
		{
			name:         "past the trained context, the whole recipe",
			plan:         dcaPlan{Enabled: true},
			arch:         "qwen3",
			numCtx:       131072,
			trainCtx:     32768,
			usedOpencoti: true,
			want: []string{
				"--dca", "on",
				"--rope-scaling", "yarn",
				"--override-kv", "qwen3.context_length=int:131072",
			},
		},
		{
			name:         "a chunk size is passed through",
			plan:         dcaPlan{Enabled: true, ChunkSize: 4096},
			arch:         "gemma4",
			numCtx:       16384,
			trainCtx:     32768,
			usedOpencoti: true,
			want:         []string{"--dca", "on", "--dca-chunk-size", "4096"},
		},
		{
			// A file that does not declare its trained context gives nothing to
			// compare against, so the override is not invented.
			name:         "no declared training context, no override",
			plan:         dcaPlan{Enabled: true},
			arch:         "qwen3",
			numCtx:       131072,
			trainCtx:     0,
			usedOpencoti: true,
			want:         []string{"--dca", "on"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := appendDCAArgs(nil, tc.plan, tc.arch, tc.numCtx, tc.trainCtx, tc.usedOpencoti)
			if !slices.Equal(got, tc.want) {
				t.Errorf("appendDCAArgs() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDCARequiresEngineExtension pins the refusal that keeps a DCA load off
// stock llama.cpp, which has no flag for any of it.
func TestDCARequiresEngineExtension(t *testing.T) {
	if why := (dcaPlan{}).requiresEngineExtension(); why != "" {
		t.Errorf("a disabled plan needs no extension, got %q", why)
	}
	why := dcaPlan{Enabled: true}.requiresEngineExtension()
	if why == "" {
		t.Fatal("an enabled plan must name its engine requirement")
	}
	if !strings.Contains(why, "--dca") {
		t.Errorf("the reason must name the flag so the message says which setting caused it, got %q", why)
	}
}

// TestCheckDCAArchitecture is the guard against the failure mode that makes DCA
// worth gating at all: on an architecture whose graph never builds the chunked
// route, the flag parses, the server starts, and nothing happens.
func TestCheckDCAArchitecture(t *testing.T) {
	t.Run("a supported architecture passes silently", func(t *testing.T) {
		warn, refuse := checkDCAArchitecture("qwen3", 131072, 32768)
		if refuse != nil || warn != "" {
			t.Errorf("got refuse=%v warn=%q, want neither", refuse, warn)
		}
	})

	t.Run("within the trained context it is only a warning", func(t *testing.T) {
		// Nothing is unsafe here: the model is inside its own window and DCA
		// would merely have been an extra route.
		warn, refuse := checkDCAArchitecture("llama", 16384, 32768)
		if refuse != nil {
			t.Errorf("refused a load that is inside its trained context: %v", refuse)
		}
		if warn == "" {
			t.Error("a setting that will do nothing must say so")
		}
	})

	t.Run("past the trained context it is refused", func(t *testing.T) {
		warn, refuse := checkDCAArchitecture("llama", 131072, 32768)
		if refuse == nil {
			t.Fatal("started a load past its trained context with nothing holding it together")
		}
		if warn != "" {
			t.Errorf("a refusal should not also warn, got %q", warn)
		}
		// The message has to say what would make it start, because the person
		// reading it is choosing between two numbers.
		for _, want := range []string{"llama", "131072", "32768"} {
			if !strings.Contains(refuse.Error(), want) {
				t.Errorf("refusal %q does not mention %q", refuse, want)
			}
		}
	})
}

func TestArchHasDCARoute(t *testing.T) {
	for _, arch := range []string{"qwen3", "qwen3moe", "qwen35", "qwen35moe", "qwen2", "gemma4", "gemma4-assistant"} {
		if !archHasDCARoute(arch) {
			t.Errorf("%q builds a DCA input in the engine but is not listed", arch)
		}
	}
	for _, arch := range []string{"llama", "gemma3", "deepseek2", "mllama", ""} {
		if archHasDCARoute(arch) {
			t.Errorf("%q has no chunked route in the engine but is listed", arch)
		}
	}
}

// TestDCAUnlocksContext covers the one question the scheduler asks, which has
// to be answered the same way as the launch does or the memory prediction and
// the process disagree.
func TestDCAUnlocksContext(t *testing.T) {
	on := true
	enabled := LlamaServerConfig{Xollama: &xollama.Config{Version: 1, DCA: &xollama.DCA{Enabled: &on}}}

	for _, tc := range []struct {
		name string
		env  map[string]string
		cfg  LlamaServerConfig
		arch string
		want bool
	}{
		{"on, opencoti, supported architecture", map[string]string{"XOLLAMA_ENGINE": "opencoti"}, enabled, "qwen3", true},
		{"off", map[string]string{"XOLLAMA_ENGINE": "opencoti"}, LlamaServerConfig{}, "qwen3", false},
		{"pinned to stock llama.cpp", map[string]string{"XOLLAMA_ENGINE": "llamacpp"}, enabled, "qwen3", false},
		{"architecture with no route", map[string]string{"XOLLAMA_ENGINE": "opencoti"}, enabled, "llama", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			f := loadTestGGUF(t, gguftest.KV{
				"general.architecture":   tc.arch,
				tc.arch + ".block_count": uint32(4),
			})
			if got := DCAUnlocksContext(tc.cfg, nil, f); got != tc.want {
				t.Errorf("DCAUnlocksContext() = %v, want %v", got, tc.want)
			}
		})
	}
	if DCAUnlocksContext(enabled, nil, nil) {
		t.Error("a nil model unlocks nothing")
	}
}
