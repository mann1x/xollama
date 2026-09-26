package llm

// xollama: `num_ctx 0` on a PolyKV load -- plans/agentic-council-chat.md
// (Phase 6). Additive; reached from the `polykv-window` hooks in
// server/sched.go, server/routes.go and llm/server.go.
//
// On opencoti with PolyKV the KV cache is one pool that sessions book windows
// in. A model (or request) that states num_ctx 0 there asks for no window of
// its own: the launch takes the model's whole trained context as the pool,
// and requests send no num_ctx, so the engine books each from the pool as it
// sees fit (a council's pools are then unowned). Everywhere else -- stock
// llama.cpp, opencoti without pools -- num_ctx 0 keeps upstream's meaning,
// which the scheduler raises to its minimum of 4.

import (
	"math"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/types/xollama"
)

// NumCtxWholePool marks a load that asked for the whole pool. The scheduler
// sets it in place of a stated 0 so upstream's minimum does not turn the ask
// into a 4-token context; the launch resolves it once the model and the
// engine are known. It is never sent to an engine.
const NumCtxWholePool = math.MaxInt32

// WantsWholePool reports whether a stated num_ctx 0 means the whole pool for
// this model: it runs PolyKV pools (session pooling or a council's pool tree)
// and neither the model nor XOLLAMA_ENGINE pins stock llama.cpp. The engine that actually serves is
// only known at launch, which is where ResolveWholePool falls back.
func WantsWholePool(cfg LlamaServerConfig) bool {
	if cfg.enginePin() == xollama.EngineLlamaCpp ||
		engine.Kind(strings.ToLower(strings.TrimSpace(envconfig.Var(engine.EnvSelector)))) == engine.KindLlamaCpp {
		return false
	}
	return resolvePoolCount(cfg)+max(cfg.CouncilPools, 0) > 0
}

// ResolveWholePool turns the mark into the context a launch uses: the model's
// trained context when the load runs PolyKV, and upstream's minimum otherwise.
// Any other value is returned as it is.
func ResolveWholePool(numCtx, trainCtx int, polykv bool) int {
	if numCtx != NumCtxWholePool {
		return numCtx
	}
	if polykv && trainCtx > 0 {
		return trainCtx
	}
	return 4
}
