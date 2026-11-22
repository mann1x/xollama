package server

// xollama: per-model live slots (slots.live) -- plans/agentic-council-chat.md
// (Phase 6). Reached from the `slots-live` hook in Scheduler.load.

import (
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

// liveSlots is the slot count a load starts with. Where opencoti will serve a
// completion model, whose slots grow on demand, it is the model's slots.live,
// else XOLLAMA_PARALLEL, else one: OLLAMA_NUM_PARALLEL is not read there, since
// it is shared with any stock ollama on the machine and sized for an engine
// that reserves a KV copy per slot. On stock llama.cpp, and for an embedding
// model, the count ollama decided stands. So it does for an MLX model, which
// opencoti never serves: upstream v0.40.17 (#19197) runs MLX generation
// concurrently on that count, and XOLLAMA_PARALLEL's default of one turned it
// off.
func liveSlots(m *Model, gpus []ml.DeviceInfo, completion bool, numParallel int) int {
	if !completion || m.IsMLX() || !llm.WouldUseOpencoti(llamaServerConfigForModel(m), gpus) {
		return numParallel
	}
	// The server's default counts as the model's own here (system-settings).
	if x := launchXollama(m); x != nil && x.Slots != nil && x.Slots.Live > 0 {
		return x.Slots.Live
	}
	return max(int(envconfig.Parallel()), 1)
}
