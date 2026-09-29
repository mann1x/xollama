package llm

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/ollama/ollama/envconfig"
)

// A quantized V cache needs flash attention, and llama.cpp decides "auto" only
// once it knows where each layer landed (llm_fused_op_flash_attn_probe in
// src/llama-context.cpp). When that probe says no, it refuses the cache at
// context creation -- a CPU-only load of a model whose own kv.v is q8_0 does
// exactly that. xollama cannot answer the probe before the launch, so it
// answers the refusal instead: one relaunch with the V half at f16, which
// costs memory and nothing else. Only for a quantized V that xollama itself
// asked for (the model's kv.v or XOLLAMA_V_CACHE_TYPE): a V that came only from
// upstream's OLLAMA_KV_CACHE_TYPE keeps upstream's failure (kv-fa-retry hook).

// quantizedVNeedsFlashAttention is llama.cpp's refusal, verbatim.
const quantizedVNeedsFlashAttention = "quantized V cache was requested, but this requires Flash Attention"

// f16VRetryReason says why a failed load should be relaunched with an f16 V
// cache, or "" when it should not.
func f16VRetryReason(loadErr error, cfg LlamaServerConfig, base string, stock, retried bool) string {
	if loadErr == nil || retried || !strings.Contains(loadErr.Error(), quantizedVNeedsFlashAttention) {
		return ""
	}
	fromModel := cfg.Xollama != nil && cfg.Xollama.KV != nil && cfg.Xollama.KV.V != ""
	fromEnv := strings.TrimSpace(envconfig.VCacheType()) != ""
	if !fromModel && !fromEnv {
		return ""
	}
	v := resolveKVCacheTypesOn(cfg, base, stock).V
	if v == "" || v == "f16" || v == "f32" || v == "bf16" {
		return ""
	}
	source := "XOLLAMA_V_CACHE_TYPE"
	if fromModel {
		source = "the model's kv.v"
	}
	return fmt.Sprintf("the engine ran without flash attention, which a %s V cache (%s) needs", v, source)
}

// withF16V is the relaunch's cache: the V half at f16, everything else as
// resolved.
func (launch llamaServerLaunchConfig) withF16V(kv kvCacheTypes) kvCacheTypes {
	if launch.forceF16V {
		kv.V = "f16"
	}
	return kv
}

// retryWithF16V relaunches a load the engine refused for a quantized V cache
// without flash attention, once.
func (s *llamaServerRunner) retryWithF16V(loadErr error) (bool, error) {
	why := f16VRetryReason(loadErr, s.launch.config, s.launch.kvCacheType, s.launch.stockKV, s.launch.forceF16V)
	if why == "" {
		return false, nil
	}
	slog.Warn("retrying the load with an f16 V cache", "model", s.modelPath, "reason", why)
	s.launch.forceF16V = true

	if err := s.stopProcess(); err != nil {
		return false, fmt.Errorf("load failed: %w; error stopping failed process: %v", loadErr, err)
	}
	s.resetLoadAccounting()

	if err := s.startProcess(); err != nil {
		return false, fmt.Errorf("load failed: %w; error starting the f16 V cache retry: %v", loadErr, err)
	}
	return true, nil
}
