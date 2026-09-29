package server

import (
	"context"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
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

// The filter runs before the load decides anything, not only where the model
// goes: slots.live and the single-sequence deny-list ask WouldUseOpencoti of
// the same list. Given the 3090 and the 9070 XT together, they answered "no",
// the council launched with one slot's context instead of slots.live's, and
// the conversation's owner took the whole window (eleven2go, 2026-09-29).
func TestAModelOnlyOpencotiServesLoadsWithItsOwnSlots(t *testing.T) {
	cuda := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, ComputeMajor: 8, ComputeMinor: 6, TotalMemory: 24 << 30, FreeMemory: 22 << 30}
	vulkan := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "1", Library: "Vulkan"}, TotalMemory: 16 << 30, FreeMemory: 30 << 30}
	cfg := &xollama.Config{KV: &xollama.KV{K: "kvarn3", V: "kvarn3"}, Slots: &xollama.Slots{Live: 2}}
	t.Setenv("XOLLAMA_ENGINE", "")
	kvarn := llm.LlamaServerConfig{Xollama: cfg}
	if !llm.WouldUseOpencoti(kvarn, []ml.DeviceInfo{cuda}) || llm.WouldUseOpencoti(kvarn, []ml.DeviceInfo{vulkan}) {
		t.Skip("this build's pin does not serve CUDA 8.6 alone and not Vulkan; the case does not arise")
	}

	ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	s := InitScheduler(ctx)
	s.waitForRecovery = 10 * time.Millisecond
	s.getGpuFn = func(context.Context, []ml.FilteredRunnerDiscovery) []ml.DeviceInfo {
		return []ml.DeviceInfo{cuda, vulkan}
	}
	s.getSystemInfoFn = getSystemInfoFn
	a := newScenarioRequest(t, ctx, "council-model", 10, nil, nil)
	a.req.model.Xollama = cfg
	var gotGpus []ml.DeviceInfo
	gotParallel := 0
	s.newServerFn = func(systemInfo ml.SystemInfo, gpus []ml.DeviceInfo, model string, f *gguf.Model, adapters, projectors []string, opts api.Options, numParallel int, c llm.LlamaServerConfig) (llm.LlamaServer, error) {
		gotGpus, gotParallel = gpus, numParallel
		return a.newServer(systemInfo, gpus, model, f, adapters, projectors, opts, numParallel, c)
	}
	s.pendingReqCh <- a.req
	s.Run(ctx)
	select {
	case <-a.req.successCh:
	case err := <-a.req.errCh:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	if len(gotGpus) != 1 || gotGpus[0].Library != "CUDA" {
		t.Errorf("loaded on %v, want the CUDA GPU only", gotGpus)
	}
	if gotParallel != 2 {
		t.Errorf("launched with %d slots, want slots.live's 2", gotParallel)
	}
}
