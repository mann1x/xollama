package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// What llama.cpp printed for a CPU-only load of a model whose own kv.v is
// q8_0 (solidPC, b208, 2026-09-29).
var refusedV = errors.New("llama-server process has terminated: exit status 1: llama_init_from_model: failed to initialize the context: " + quantizedVNeedsFlashAttention)

func TestAQuantizedVTheModelAskedForIsRetriedAtF16(t *testing.T) {
	t.Setenv("XOLLAMA_V_CACHE_TYPE", "")
	cfg := LlamaServerConfig{Xollama: &xollama.Config{Version: 4, KV: &xollama.KV{K: "f16", V: "q8_0"}}}

	why := f16VRetryReason(refusedV, cfg, "", false, false, false)
	if !strings.Contains(why, "q8_0") || !strings.Contains(why, "the model's kv.v") {
		t.Fatalf("reason = %q, want it to name q8_0 and the model's kv.v", why)
	}
	if why := f16VRetryReason(refusedV, cfg, "", false, true, false); why != "" {
		t.Errorf("a second retry was offered: %q", why)
	}
	if why := f16VRetryReason(errors.New("CUDA error: out of memory"), cfg, "", false, false, false); why != "" {
		t.Errorf("an unrelated failure was retried: %q", why)
	}
}

func TestAQuantizedVFromXollamasEnvironmentIsRetriedAtF16(t *testing.T) {
	t.Setenv("XOLLAMA_V_CACHE_TYPE", "q4_0")
	if why := f16VRetryReason(refusedV, LlamaServerConfig{}, "", false, false, false); !strings.Contains(why, "XOLLAMA_V_CACHE_TYPE") {
		t.Fatalf("reason = %q, want it to name XOLLAMA_V_CACHE_TYPE", why)
	}
}

// Upstream fails this load the same way and has no retry, so a V that came
// only from OLLAMA_KV_CACHE_TYPE keeps upstream's failure: off means off.
func TestUpstreamsKVCacheTypeAloneKeepsUpstreamsFailure(t *testing.T) {
	t.Setenv("XOLLAMA_V_CACHE_TYPE", "")
	if why := f16VRetryReason(refusedV, LlamaServerConfig{}, "q8_0", false, false, false); why != "" {
		t.Fatalf("OLLAMA_KV_CACHE_TYPE alone was retried: %q", why)
	}
}

func TestOpencotiIsNeverAnsweredWithAnF16VRelaunch(t *testing.T) {
	t.Setenv("XOLLAMA_V_CACHE_TYPE", "")
	cfg := LlamaServerConfig{Xollama: &xollama.Config{Version: 4, KV: &xollama.KV{K: "f16", V: "q8_0"}}}
	// opencoti resolves -fa auto on whenever V is quantized (0523, c8): its
	// refusal is a defect to surface, not a reason for a second load.
	if why := f16VRetryReason(refusedV, cfg, "", false, false, true); why != "" {
		t.Fatalf("opencoti was offered an f16 V relaunch: %q", why)
	}
}

func TestTheRetryChangesOnlyTheVHalf(t *testing.T) {
	kv := kvCacheTypes{K: "q8_0", V: "q8_0", KSWA: "x", VSWA: "y"}
	if got := (llamaServerLaunchConfig{}).withF16V(kv); got != kv {
		t.Errorf("without the retry the cache changed: %+v", got)
	}
	got := (llamaServerLaunchConfig{forceF16V: true}).withF16V(kv)
	want := kvCacheTypes{K: "q8_0", V: "f16", KSWA: "x", VSWA: "y"}
	if got != want {
		t.Errorf("retry cache = %+v, want %+v", got, want)
	}
}
