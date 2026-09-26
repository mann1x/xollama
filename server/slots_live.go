package server

// xollama: per-model live slots (slots.live) -- plans/agentic-council-chat.md
// (Phase 6). Reached from the `slots-live` hook in Scheduler.load.

import (
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

// liveSlots is the slot count a load starts with. A model's slots.live
// replaces the operator's OLLAMA_NUM_PARALLEL only where opencoti will serve
// it, whose slots grow on demand; on stock llama.cpp, which reserves a KV
// copy per slot at launch, and for an embedding model, the count stands.
func liveSlots(m *Model, gpus []ml.DeviceInfo, completion bool, numParallel int) int {
	if !completion || m.Xollama == nil || m.Xollama.Slots == nil || m.Xollama.Slots.Live <= 0 {
		return numParallel
	}
	if !llm.WouldUseOpencoti(llamaServerConfigForModel(m), gpus) {
		return numParallel
	}
	return m.Xollama.Slots.Live
}
