package llm

import (
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func cfgWithKV(kv *xollama.KV) LlamaServerConfig {
	return LlamaServerConfig{Xollama: &xollama.Config{Version: 1, KV: kv}}
}

func TestResolveKVCacheTypesPrecedence(t *testing.T) {
	tests := []struct {
		name string
		base string // what OLLAMA_KV_CACHE_TYPE resolved to
		env  map[string]string
		cfg  LlamaServerConfig
		want kvCacheTypes
		why  string
	}{
		{
			name: "nothing set anywhere leaves the engine's own default",
			why:  "an unmodified server must not start passing flags it never passed",
		},
		{
			name: "upstream's server-wide setting still writes both halves",
			base: "q8_0",
			want: kvCacheTypes{K: "q8_0", V: "q8_0"},
			why:  "OLLAMA_KV_CACHE_TYPE keeps behaving exactly as it did",
		},
		{
			name: "one half can be overridden without touching the other",
			base: "q8_0",
			env:  map[string]string{"XOLLAMA_V_CACHE_TYPE": "q4_0"},
			want: kvCacheTypes{K: "q8_0", V: "q4_0"},
			why:  "keys cost more quality than values, so a wider K is the usual recipe",
		},
		{
			name: "both halves can be set with no server-wide default at all",
			env:  map[string]string{"XOLLAMA_K_CACHE_TYPE": "q8_0", "XOLLAMA_V_CACHE_TYPE": "kvarn3"},
			want: kvCacheTypes{K: "q8_0", V: "kvarn3"},
		},
		{
			name: "the model overrides the environment",
			base: "q8_0",
			env:  map[string]string{"XOLLAMA_K_CACHE_TYPE": "q5_1", "XOLLAMA_V_CACHE_TYPE": "q5_1"},
			cfg:  cfgWithKV(&xollama.KV{K: "f16", V: "q4_0"}),
			want: kvCacheTypes{K: "f16", V: "q4_0"},
			why:  "a model that states a type means it, whatever the server prefers",
		},
		{
			name: "a model may override one half and inherit the other",
			base: "q8_0",
			cfg:  cfgWithKV(&xollama.KV{V: "q4_0"}),
			want: kvCacheTypes{K: "q8_0", V: "q4_0"},
		},
		{
			name: "the sliding-window ring is separate from the global cache",
			base: "kvarn3",
			cfg:  cfgWithKV(&xollama.KV{KSWA: "q4_0", VSWA: "q4_0"}),
			want: kvCacheTypes{K: "kvarn3", V: "kvarn3", KSWA: "q4_0", VSWA: "q4_0"},
			why:  "on a sliding-window model the ring is most of the cost",
		},
		{
			name: "type names are case-insensitive",
			env:  map[string]string{"XOLLAMA_K_CACHE_TYPE": "Q8_0"},
			cfg:  cfgWithKV(&xollama.KV{V: "KVarN3"}),
			want: kvCacheTypes{K: "q8_0", V: "kvarn3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got := resolveKVCacheTypes(tt.cfg, tt.base)
			if got != tt.want {
				t.Errorf("resolved %+v, want %+v (%s)", got, tt.want, tt.why)
			}
		})
	}
}

// Every setting above ends as an argument, or it did nothing. The global pair
// goes on both engines; the ring is added only once opencoti is the answer,
// because stock llama.cpp has no such flag.
func TestKVCacheArgs(t *testing.T) {
	kv := kvCacheTypes{K: "q8_0", V: "q4_0", KSWA: "q4_0", VSWA: "q4_0"}

	params := appendKVCacheArgs(nil, kv)
	want := []string{"--cache-type-k", "q8_0", "--cache-type-v", "q4_0"}
	if !slices.Equal(params, want) {
		t.Errorf("global args = %v, want %v", params, want)
	}

	if got := appendKVCacheRingArgs(slices.Clone(params), kv, false); !slices.Equal(got, params) {
		t.Errorf("stock llama.cpp must get no ring flags, got %v", got)
	}

	got := appendKVCacheRingArgs(slices.Clone(params), kv, true)
	wantRing := append(slices.Clone(want), "--cache-type-k-swa", "q4_0", "--cache-type-v-swa", "q4_0")
	if !slices.Equal(got, wantRing) {
		t.Errorf("opencoti args = %v, want %v", got, wantRing)
	}

	// Nothing set means nothing passed -- this is the off path.
	if got := appendKVCacheArgs(nil, kvCacheTypes{}); len(got) != 0 {
		t.Errorf("an unset configuration must add no arguments, got %v", got)
	}
}

// A type stock llama.cpp has never heard of has to be caught before the process
// starts. Otherwise it surfaces as an unrecognised argument from a subprocess,
// which does not say which of four places set it.
func TestKVCacheTypesRequiringTheEngineExtension(t *testing.T) {
	for _, tt := range []struct {
		name    string
		kv      kvCacheTypes
		wantWhy string
	}{
		{
			name: "every stock type is fine",
			kv:   kvCacheTypes{K: "q8_0", V: "q4_0"},
		},
		{
			name: "unset is fine",
		},
		{
			name:    "an engine cache width is not",
			kv:      kvCacheTypes{K: "q8_0", V: "kvarn3"},
			wantWhy: "kv.v",
		},
		{
			name:    "and the name of the setting is in the message",
			kv:      kvCacheTypes{K: "turbo3_tcq"},
			wantWhy: "kv.k",
		},
		{
			name: "a ring needs the extension even when its type is a stock one",
			// stock llama.cpp has no --cache-type-k-swa at all, so q4_0 being
			// an ordinary type does not make this reachable there
			kv:      kvCacheTypes{K: "f16", V: "f16", KSWA: "q4_0", VSWA: "q4_0"},
			wantWhy: "k_swa",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			why := tt.kv.requiresEngineExtension()
			if tt.wantWhy == "" {
				if why != "" {
					t.Errorf("expected no objection, got %q", why)
				}
				return
			}
			if why == "" {
				t.Fatalf("expected %q to need the engine extension", tt.wantWhy)
			}
			if !strings.Contains(why, tt.wantWhy) {
				t.Errorf("message %q should name the setting (%q) that caused it", why, tt.wantWhy)
			}
		})
	}
}

// The suggestion list is a measurement of the pinned artifact, not a pattern.
// kvarn7 is the trap: the run 2,3,4,5,6,8 invites it, and the engine answers
// "Unsupported cache type: kvarn7". A model configured with one does not load.
func TestTheSuggestedCacheTypesAreOnesThePinnedEngineTakes(t *testing.T) {
	got := KnownCacheTypes(true)
	for _, absent := range []string{"kvarn1", "kvarn7", "turbo2", "turbo3_tcq"} {
		if slices.Contains(got, absent) {
			t.Errorf("%q is suggested; it is either refused by the engine or a frozen tier", absent)
		}
	}
	for _, want := range []string{"kvarn2", "kvarn8", "q6_0", "f16", "q8_0"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q is not suggested; the pinned engine accepts it", want)
		}
	}
	// An engine pin of llamacpp narrows to what upstream's own parser takes.
	stock := KnownCacheTypes(false)
	for _, absent := range []string{"kvarn2", "q6_0"} {
		if slices.Contains(stock, absent) {
			t.Errorf("%q is suggested for stock llama.cpp, which has no such type", absent)
		}
	}
}
