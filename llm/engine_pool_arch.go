package llm

import (
	"slices"

	"github.com/ollama/ollama/fs/gguf"
)

// xollama-hook: engine-session
//
// Which models a shared prefix pool can actually help.
//
// Attaching to a pool shares a prefix of P tokens, and the engine works P out
// itself. On an attention-only model that is exactly right: the KV cache is a
// per-token record, so any prefix of it is a valid thing to share.
//
// A model that keeps recurrent state is different. Mamba and RWKV, and the
// growing family of hybrids that put an SSM, a short convolution or a linear
// attention block beside the attention ones, carry a single rolling summary of
// everything seen so far rather than a per-token record. There is no "first P
// tokens" of such a summary to hand out, so the engine can share it only when P
// covers the pool's state ENTIRELY -- and a pool snapshotted from a finished
// conversation never matches a conversation that merely begins the same way.
//
// Nothing fails when that happens. The attach succeeds, shares nothing, and the
// reserved sequence is spent for no benefit, which is worse than not pooling
// because it looks like it is working.
//
// Pooling used to be switched off for these models for that reason. It is not
// any more, because both halves of what made it impossible are now fixed. The
// pool is materialised from the prefix TOKENS -- POST /polykv/pools {"tokens":
// [...]} -- rather than snapshotted from a finished session, so there is a
// prefix to share at all; and the length of that prefix is measured against the
// template instead of guessed from two conversations, so it stops exactly where
// every conversation with this prefix stops agreeing.
//
// Exactly is the operative word. server-context.cpp takes the share only when
// P == pool_max + 1 and the prompt is strictly longer -- the match must cover
// the pool entirely -- so a pool one token too long can never be matched by
// anything, and one token too short works fine. That asymmetry is why
// templateBoundary in engine_pool.go measures the boundary from renders that
// disagree immediately, and why the measurement is then clamped against real
// prompts rather than trusted on its own.
//
// What this still gates: the boundary can only be measured where the engine
// owns the template, which is the Chat path. A Completion request on one of
// these models is not pooled from, because the only prefix available there is
// what two conversations happened to have in common. See capturePool.

// recurrentStateKeys are the GGUF keys a model carries when it has recurrent
// state to keep: an SSM block, an RWKV time-mix, or a short convolution. The
// lookup qualifies them with the architecture, so a model newer than the list
// below is still caught as long as it announces its state the usual way.
var recurrentStateKeys = []string{
	"ssm.state_size",
	"ssm.conv_kernel",
	"ssm.inner_size",
	"wkv.head_size",
	"shortconv.l_cache",
}

// recurrentArchitectures mirrors llm_arch_is_recurrent and llm_arch_is_hybrid
// in llama.cpp's src/llama-arch.cpp, by the architecture strings GGUF actually
// carries -- several of which are hyphenated where the enum is not. It is the
// belt to recurrentStateKeys' braces: llama.cpp classifies by this list and
// nothing else, so matching it is exactly as accurate as the engine is.
var recurrentArchitectures = []string{
	"mamba", "mamba2", "rwkv6", "rwkv6qwen2", "rwkv7", "arwkv7",
	"jamba", "falcon-h1", "plamo2", "granitehybrid", "lfm2", "lfm2moe",
	"nemotron_h", "nemotron_h_moe", "qwen3next", "kimi-linear",
	"bailingmoe3", "kimi-k3", "qwen35", "qwen35moe", "qwen4exp",
	"deepseek4", "minimax-01",
}

// modelKeepsRecurrentState reports whether this model carries state that cannot
// be cut at an arbitrary prefix, and so can be pooled only from a prefix that
// is known to be exact.
func modelKeepsRecurrentState(f *gguf.Model) bool {
	if f == nil {
		return false
	}
	for _, key := range recurrentStateKeys {
		if f.KV().Has(key) {
			return true
		}
	}
	return slices.Contains(recurrentArchitectures, f.KV().Architecture())
}
