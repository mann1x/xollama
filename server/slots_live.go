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
// model, the count ollama decided stands.
func liveSlots(m *Model, gpus []ml.DeviceInfo, completion bool, numParallel int) int {
	if !completion || !llm.WouldUseOpencoti(llamaServerConfigForModel(m), gpus) {
		return numParallel
	}
	if m.Xollama != nil && m.Xollama.Slots != nil && m.Xollama.Slots.Live > 0 {
		return m.Xollama.Slots.Live
	}
	return max(int(envconfig.Parallel()), 1)
}
