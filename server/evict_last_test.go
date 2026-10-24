package server

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// eleven2go's two cards as the scheduler sees them, the 3090 with a model on it.
func elevenCards() (cuda, vulkan ml.DeviceInfo) {
	cuda = ml.DeviceInfo{
		DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, PCIID: "0000:01:00.0", Description: "NVIDIA GeForce RTX 3090",
		ComputeMajor: 8, ComputeMinor: 6, TotalMemory: 24 << 30, FreeMemory: 10 << 30,
	}
	vulkan = ml.DeviceInfo{
		DeviceID: ml.DeviceID{ID: "2", Library: "Vulkan"}, PCIID: "0000:03:00.0", Description: "AMD Radeon RX 9070 XT",
		TotalMemory: 16 << 30, FreeMemory: 15 << 30,
	}
	return cuda, vulkan
}

func skipUnlessOpencotiServesBoth(t *testing.T, cuda, vulkan ml.DeviceInfo) {
	t.Helper()
	t.Setenv("XOLLAMA_ENGINE", "")
	if !llm.WouldUseOpencoti(llm.LlamaServerConfig{}, []ml.DeviceInfo{cuda}) || !llm.WouldUseOpencoti(llm.LlamaServerConfig{}, []ml.DeviceInfo{vulkan}) {
		t.Skip("this build's pin does not serve both CUDA and Vulkan here; the case does not arise")
	}
}

func loadedOn(s *Scheduler, name string, idle time.Duration, on ...ml.DeviceInfo) *runnerRef {
	r := &runnerRef{
		llama:           &mockLlm{vramByGPU: map[ml.DeviceID]uint64{}},
		modelKey:        name,
		sessionDuration: idle,
	}
	for _, g := range on {
		r.gpus = append(r.gpus, g.DeviceID)
	}
	s.loadedMu.Lock()
	s.loaded[name] = r
	s.loadedMu.Unlock()
	return r
}

// The KV cache never evicts on opencoti: a load whose weights fit the card is
// loaded beside the other models, and the engine keeps what the card cannot
// hold in host memory. On eleven2go two clients evicted each other's model
// from the 3090 on every request while the 9070 XT sat empty (2026-10-11).
func TestALoadEvictsOnlyWhenItsWeightsDoNotFit(t *testing.T) {
	cuda, vulkan := elevenCards()
	skipUnlessOpencotiServesBoth(t, cuda, vulkan)
	ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	req := newScenarioRequest(t, ctx, "second-model", 10, nil, nil).req
	f, err := llm.LoadModel(req.model.ModelPath, 1024)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(req.model.ModelPath)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("the scenario's model has no size: %v", err)
	}
	weights := uint64(fi.Size())
	gpus := []ml.DeviceInfo{vulkan}
	huge := weights * 100 // a cache far past the card, as upstream predicted it

	if needsEviction(req, f, gpus, huge, weights) {
		t.Error("evicted although the weights fit: the cache is the engine's to place")
	}
	// A fifth of the weights may go to host memory, not more.
	if needsEviction(req, f, gpus, huge, weights*80/100) {
		t.Error("evicted with exactly 20% of the weights off the card")
	}
	if !needsEviction(req, f, gpus, huge, weights*80/100-1) {
		t.Error("not evicted with more than 20% of the weights off the card")
	}

	// Off means off: stock llama.cpp keeps upstream's 80% of the prediction.
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	if !needsEviction(req, f, gpus, 81, 100) || needsEviction(req, f, gpus, 80, 100) {
		t.Error("with XOLLAMA_ENGINE=llamacpp the decision is not upstream's")
	}
	want := llm.PredictServerVRAM(req.model.ModelPath, f, 4096) + slotCeilingVRAM(req, f, gpus, 1)
	if got := predictVRAM(req, f, gpus, 4096, 1); got != want {
		t.Errorf("with XOLLAMA_ENGINE=llamacpp the prediction is %d, upstream's is %d", got, want)
	}
}

// A model stating kv.pool_cells is predicted at that pool, the -c its launch
// gets, and stock llama.cpp keeps upstream's figure.
func TestAStatedPoolIsWhatThePredictionPricesTheCacheAt(t *testing.T) {
	cuda, vulkan := elevenCards()
	skipUnlessOpencotiServesBoth(t, cuda, vulkan)
	ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	req := newScenarioRequest(t, ctx, "pooled-model", 10, nil, nil).req
	f, err := llm.LoadModel(req.model.ModelPath, 1024)
	if err != nil {
		t.Fatal(err)
	}
	gpus := []ml.DeviceInfo{cuda}
	req.opts.NumCtx = 4096
	unstated := predictVRAM(req, f, gpus, 8192, 2)

	req.model.Xollama = &xollama.Config{Version: 1, KV: &xollama.KV{PoolCells: 65536}}
	cfg := llamaServerConfigForModel(req.model)
	numCtxSeq := effectiveModelContext(req.opts.NumCtx, f, false)
	want := llm.PredictOpencotiVRAM(req.model.ModelPath, f, cfg, 65536, numCtxSeq, llamaServerGenerationBatchDefault, 2, req.opts.DraftNumPredict).Total()
	if got := predictVRAM(req, f, gpus, 8192, 2); got != want || got <= unstated {
		t.Fatalf("predicted %d with the pool stated (want %d, unstated %d)", got, want, unstated)
	}

	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	upstream := llm.PredictServerVRAM(req.model.ModelPath, f, 8192) + slotCeilingVRAM(req, f, gpus, 2)
	if got := predictVRAM(req, f, gpus, 8192, 2); got != upstream {
		t.Fatalf("with XOLLAMA_ENGINE=llamacpp the prediction is %d, upstream's is %d", got, upstream)
	}
}

// On Apple silicon the GPU's memory is the host's, so nothing can be kept "in
// host memory": a load that does not fit whole evicts. On the Mac mini a
// 10.8 GiB load beside two models with 8.9 GiB free placed every layer and
// then failed every decode (2026-10-13).
func TestALoadThatDoesNotFitEvictsWhereTheGPUSharesHostMemory(t *testing.T) {
	metal := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "0", Library: "Metal"}, Description: "Apple M4", Integrated: true, TotalMemory: 18 << 30, FreeMemory: 9 << 30}
	cuda, vulkan := elevenCards()
	if !sharesHostMemory([]ml.DeviceInfo{metal}) {
		t.Fatal("Metal is not read as memory shared with the host")
	}
	if sharesHostMemory([]ml.DeviceInfo{cuda, vulkan}) || sharesHostMemory([]ml.DeviceInfo{metal, cuda}) || sharesHostMemory(nil) {
		t.Fatal("a load with a discrete card, or with no GPU, is read as memory shared with the host")
	}
	igpu := vulkan
	igpu.Integrated = true
	if !sharesHostMemory([]ml.DeviceInfo{igpu}) {
		t.Fatal("a unified-memory Vulkan GPU is not read as memory shared with the host")
	}

	t.Setenv("XOLLAMA_ENGINE", "")
	gpus := []ml.DeviceInfo{metal}
	if !llm.WouldUseOpencoti(llm.LlamaServerConfig{}, gpus) {
		t.Skip("opencoti does not serve Metal on this platform; the decision is upstream's here")
	}
	ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	req := newScenarioRequest(t, ctx, "third-model", 10, nil, nil).req
	f, err := llm.LoadModel(req.model.ModelPath, 1024)
	if err != nil {
		t.Fatal(err)
	}
	// The Mac mini's figures: the weights alone pass the 20% rule.
	predicted, free := uint64(10800)<<20, uint64(8900)<<20
	if !needsEviction(req, f, gpus, predicted, free) {
		t.Error("a load that does not fit whole was started beside the loaded models")
	}
	if needsEviction(req, f, gpus, free, free) {
		t.Error("evicted although the load fits whole")
	}
}

// A runner on a card's CUDA side is on the card: its Vulkan copy is the same
// GPU, by PCI ID.
func TestARunnerIsOnBothBackendsOfItsCard(t *testing.T) {
	cuda, vulkan := elevenCards()
	ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	s := InitScheduler(ctx)
	on3090 := loadedOn(s, "on-the-3090", time.Minute, cuda)
	copy3090 := cuda
	copy3090.DeviceID = ml.DeviceID{ID: "1", Library: "Vulkan"}

	busy := s.busyGPUs([]ml.DeviceInfo{cuda, vulkan})
	if got := busy[gpuKey(copy3090)]; len(got) != 1 || got[0] != on3090 {
		t.Errorf("runners on the 3090's Vulkan copy = %v, want the one loaded through CUDA", got)
	}
	if got := busy[gpuKey(vulkan)]; len(got) != 0 {
		t.Errorf("runners on the empty 9070 XT = %v", got)
	}
}

// When something has to go, it is a runner on the cards the load can use: the
// one idle longest on another card frees nothing for it.
func TestTheEvictedRunnerIsOnTheLoadsGPUs(t *testing.T) {
	cuda, vulkan := elevenCards()
	skipUnlessOpencotiServesBoth(t, cuda, vulkan)
	ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	s := InitScheduler(ctx)
	loadedOn(s, "a-on-the-3090", time.Second, cuda) // upstream's pick: shortest duration, first by name
	loadedOn(s, "b-on-the-9070", time.Hour, vulkan)
	req := newScenarioRequest(t, ctx, "pinned-to-the-9070", 10, nil, nil).req
	busy := s.busyGPUs([]ml.DeviceInfo{cuda, vulkan})

	if got := s.findRunnerToUnload(); got == nil || got.modelKey != "a-on-the-3090" {
		t.Fatalf("upstream's victim = %v; the test no longer tells the two apart", got)
	}
	if got := s.findRunnerToUnloadOn(req, []ml.DeviceInfo{vulkan}, busy); got == nil || got.modelKey != "b-on-the-9070" {
		t.Errorf("victim = %v, want the runner on the 9070 XT", got)
	}
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	if got := s.findRunnerToUnloadOn(req, []ml.DeviceInfo{vulkan}, busy); got == nil || got.modelKey != "a-on-the-3090" {
		t.Errorf("with XOLLAMA_ENGINE=llamacpp the victim = %v, want upstream's", got)
	}
	t.Setenv("XOLLAMA_ENGINE", "")
	// A card with no runner: upstream decides.
	other := vulkan
	other.PCIID = "0000:09:00.0"
	if got := s.findRunnerToUnloadOn(req, []ml.DeviceInfo{other}, busy); got == nil || got.modelKey != "a-on-the-3090" {
		t.Errorf("victim for a card no runner is on = %v, want upstream's", got)
	}
}

// The scheduler's own load: one model on the 3090, the 9070 XT with room for
// the second model's weights but not for the cache upstream predicts. On
// opencoti it loads; on stock llama.cpp the same request evicts, as upstream
// has it.
func TestSchedLoadsBesideALoadedModelWhenOnlyTheCacheIsShort(t *testing.T) {
	cuda, vulkan := elevenCards()
	skipUnlessOpencotiServesBoth(t, cuda, vulkan)
	for _, tc := range []struct {
		engine string
		evict  bool
	}{{"", false}, {"llamacpp", true}} {
		t.Run("engine="+tc.engine, func(t *testing.T) {
			t.Setenv("XOLLAMA_ENGINE", tc.engine)
			ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
			defer done()
			s := InitScheduler(ctx)
			s.waitForRecovery = 10 * time.Millisecond
			loadedOn(s, "on-the-3090", time.Minute, cuda)
			a := newScenarioRequestWithContext(t, ctx, "second-model", 10, nil, nil, 131072)
			a.req.opts.NumCtx = 131072
			fi, err := os.Stat(a.req.model.ModelPath)
			if err != nil {
				t.Fatal(err)
			}
			// Room for the weights and little else on either card.
			busy3090, free9070 := cuda, vulkan
			busy3090.FreeMemory = uint64(fi.Size())
			free9070.FreeMemory = uint64(fi.Size()) * 2
			var placed []ml.DeviceInfo
			s.newServerFn = func(_ ml.SystemInfo, gpus []ml.DeviceInfo, model string, _ *gguf.Model, _ []string, _ []string, _ api.Options, _ int, _ llm.LlamaServerConfig) (llm.LlamaServer, error) {
				placed = gpus
				return &mockLlm{modelPath: model, vramByGPU: map[ml.DeviceID]uint64{}}, nil
			}

			evict := s.load(a.req, getSystemInfoFn(), []ml.DeviceInfo{busy3090, free9070}, true)
			if evict != tc.evict {
				t.Fatalf("eviction asked = %v, want %v", evict, tc.evict)
			}
			if !tc.evict && (len(placed) != 1 || placed[0].PCIID != vulkan.PCIID) {
				t.Fatalf("placed on %v, want the 9070 XT", ids(placed))
			}
		})
	}
}

type opencotiMock struct{ *mockLlm }

func (opencotiMock) EngineName() string { return "opencoti" }

// An unload on memory shared with the host is not over when the process is
// gone: the system's free memory lags it. On the Mac mini the scheduler read
// 3.4 GiB free 22 ms after a 10.8 GiB runner stopped and evicted a second
// model for a 3.6 GiB load (2026-10-13).
func TestAnUnloadOnSharedMemoryWaitsForTheHostsMemory(t *testing.T) {
	s := InitScheduler(t.Context())
	s.waitForRecovery = 2 * time.Second
	var reads atomic.Int32
	s.getSystemInfoFn = func() ml.SystemInfo {
		// The first reading is the baseline; the memory is back at the fourth.
		if reads.Add(1) < 4 {
			return ml.SystemInfo{FreeMemory: 3 << 30}
		}
		return ml.SystemInfo{FreeMemory: 14 << 30}
	}
	metal := ml.DeviceID{ID: "0", Library: "Metal"}
	shared := &runnerRef{llama: opencotiMock{&mockLlm{}}, gpus: []ml.DeviceID{metal}, vramSize: 10 << 30}

	waiting := s.waitForHostMemory(shared)
	if waiting == nil {
		t.Fatal("no wait for an opencoti runner on shared memory")
	}
	select {
	case <-waiting:
		if reads.Load() < 4 {
			t.Errorf("the wait ended after %d readings, before the memory was back", reads.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("the wait did not end when the memory was back")
	}

	// Memory that never comes back ends the wait at waitForRecovery.
	s.waitForRecovery = 120 * time.Millisecond
	s.getSystemInfoFn = func() ml.SystemInfo { return ml.SystemInfo{FreeMemory: 3 << 30} }
	select {
	case <-s.waitForHostMemory(shared):
	case <-time.After(2 * time.Second):
		t.Fatal("the wait has no end")
	}

	// A card is upstream's wait, and so is stock llama.cpp anywhere.
	if s.waitForHostMemory(&runnerRef{llama: opencotiMock{&mockLlm{}}, gpus: []ml.DeviceID{metal}, vramSize: 1, discreteGPUs: true}) != nil {
		t.Error("a runner on a discrete card waits for host memory")
	}
	if s.waitForHostMemory(&runnerRef{llama: &mockLlm{}, gpus: []ml.DeviceID{metal}, vramSize: 1}) != nil {
		t.Error("a stock llama.cpp runner waits for host memory: off means off")
	}
}
