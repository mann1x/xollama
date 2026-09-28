package server

import (
	"testing"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// A model whose KV cache only opencoti runs is placed on the GPUs opencoti
// serves, even when a GPU only stock llama.cpp serves has more free memory:
// eleven2go's council model went to the RX 9070 XT (Vulkan) beside its RTX 3090
// and was refused at launch. Anything stock can serve is placed as upstream
// places it, and XOLLAMA_ENGINE=llamacpp changes nothing.
func TestAModelOnlyOpencotiServesIsPlacedWhereOpencotiRuns(t *testing.T) {
	cuda := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, ComputeMajor: 8, ComputeMinor: 6, FreeMemory: 22 << 30}
	vulkan := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "1", Library: "Vulkan"}, FreeMemory: 30 << 30}
	gpus := []ml.DeviceInfo{cuda, vulkan}
	kvarn := llm.LlamaServerConfig{Xollama: &xollama.Config{KV: &xollama.KV{K: "kvarn3", V: "kvarn3"}}}
	plain := llm.LlamaServerConfig{Xollama: &xollama.Config{KV: &xollama.KV{K: "q8_0", V: "q8_0"}}}

	t.Setenv("XOLLAMA_ENGINE", "")
	if !llm.WouldUseOpencoti(kvarn, []ml.DeviceInfo{cuda}) || llm.WouldUseOpencoti(kvarn, []ml.DeviceInfo{vulkan}) {
		t.Skip("this build's pin does not serve CUDA 8.6 alone and not Vulkan; the case does not arise")
	}
	if got := opencotiPlacement(kvarn, gpus); len(got) != 1 || got[0].Library != "CUDA" {
		t.Errorf("kvarn3 model placed on %v, want the CUDA GPU only", got)
	}
	if got := opencotiPlacement(plain, gpus); len(got) != 2 {
		t.Errorf("q8_0 model placed on %v, want every GPU (upstream's placement decides)", got)
	}
	if got := opencotiPlacement(llm.LlamaServerConfig{}, gpus); len(got) != 2 {
		t.Errorf("a model with no KV setting placed on %v, want every GPU", got)
	}

	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	if got := opencotiPlacement(kvarn, gpus); len(got) != 2 {
		t.Errorf("with XOLLAMA_ENGINE=llamacpp the placement changed: %v", got)
	}
}
