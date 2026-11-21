package llm

import (
	"context"
	"io"
	"testing"

	"github.com/ollama/ollama/ml"
)

// eleven2go, 2026-11-21, the v0.40.17-rc.1.xollama #2362 check: a model pinned
// to the RX 9070 XT (discovery's Vulkan2), num_gpu 99. The engine saw only
// that card and called it Vulkan0. Lines as the engine wrote them; the last
// KV and compute lines are the allocation after the measure pass.
const renumberedLoadLog = `cmn  common_param:   - Vulkan0 : AMD Radeon RX 9070 XT (16304 MiB, 15419 MiB free)
llama_prepare_model_devices: using device Vulkan0 (AMD Radeon RX 9070 XT) (uuid:00000000030000000000000000000000) - 15419 MiB free
load_tensors: offloaded 66/66 layers to GPU
load_tensors:   CPU_Mapped model buffer size =   682.03 MiB
load_tensors:      Vulkan0 model buffer size = 15621.77 MiB
llama_kv_cache:    Vulkan0 KV buffer size =   512.00 MiB
sched_reserve:    Vulkan0 compute buffer size =   200.02 MiB
`

var rx9070 = ml.DeviceInfo{
	DeviceID:    ml.DeviceID{ID: "2", Library: "Vulkan"},
	Name:        "Vulkan2",
	Description: "AMD Radeon RX 9070 XT",
	TotalMemory: 16304 << 20,
}

func renumberedRunner(t *testing.T, opencoti bool, gpus ...ml.DeviceInfo) *llamaServerRunner {
	t.Helper()
	t.Setenv("XOLLAMA_ENGINE", "")
	r := &llamaServerRunner{
		usedOpencoti:     opencoti,
		vramByDevice:     make(map[string]uint64),
		systemFreeAtLoad: make(map[string]uint64),
		gpus:             gpus,
	}
	w := &memoryParsingWriter{inner: io.Discard, runner: r}
	if _, err := w.Write([]byte(renumberedLoadLog)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAPinnedCardTheEngineRenumberedIsFoundUnderItsEngineName(t *testing.T) {
	r := renumberedRunner(t, true, rx9070)
	mib := func(f float64) uint64 { return uint64(f * (1 << 20)) }
	used := mib(15621.77) + mib(512) + mib(200.02)
	if got := r.VRAMByGPU(rx9070.DeviceID); got != used {
		t.Fatalf("VRAMByGPU = %d MiB, want the engine's Vulkan0 buffers (%d MiB)", got>>20, used>>20)
	}
	if got, want := r.Overcommitted()>>20, (used-15419<<20)>>20; got != want {
		t.Fatalf("overcommitted %d MiB, want %d: the load went past the 15419 MiB the card had free", got, want)
	}
	if _, vram := r.MemorySize(); vram>>20 != 15419 {
		t.Fatalf("size_vram = %d MiB, want what the card had free (15419), not 100%%", vram>>20)
	}
	if info := r.GetDeviceInfos(context.Background()); len(info) != 1 || info[0].FreeMemory != 0 {
		t.Fatalf("device free = %+v, want 0: the model fills the card", info)
	}
}

func TestStockLlamaCppKeepsUpstreamsDeviceNames(t *testing.T) {
	r := renumberedRunner(t, false, rx9070)
	if got := r.VRAMByGPU(rx9070.DeviceID); got != 0 {
		t.Fatalf("VRAMByGPU = %d on stock llama.cpp, want upstream's lookup by Vulkan2 (0)", got)
	}
}

func TestADeviceTheEngineNamesAsXollamaDoesIsLeftAlone(t *testing.T) {
	igpu := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "0", Library: "Vulkan"}, Name: "Vulkan0", Description: "AMD Radeon(TM) Graphics"}
	r := &llamaServerRunner{usedOpencoti: true, gpus: []ml.DeviceInfo{igpu, rx9070}}
	r.noteEngineDevice([]byte(`using device Vulkan0 (AMD Radeon(TM) Graphics) (0000:7a:00.0) - 30221 MiB free`))
	r.noteEngineDevice([]byte(`using device Vulkan2 (AMD Radeon RX 9070 XT) (0000:03:00.0) - 15419 MiB free`))
	for _, g := range r.gpus {
		if got := r.engineKey(g); got != g.Name {
			t.Errorf("engineKey(%s) = %s, want %s: the engine saw every card", g.Name, got, g.Name)
		}
	}
}

func TestTwoIdenticalCardsKeepTheirOrder(t *testing.T) {
	a := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "1", Library: "CUDA"}, Name: "CUDA1", Description: "NVIDIA GeForce RTX 3090"}
	b := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "3", Library: "CUDA"}, Name: "CUDA3", Description: "NVIDIA GeForce RTX 3090"}
	r := &llamaServerRunner{usedOpencoti: true, gpus: []ml.DeviceInfo{a, b}}
	r.noteEngineDevice([]byte(`using device CUDA0 (NVIDIA GeForce RTX 3090) (0000:11:00.0) - 23000 MiB free`))
	r.noteEngineDevice([]byte(`using device CUDA1 (NVIDIA GeForce RTX 3090) (0000:21:00.0) - 23000 MiB free`))
	if got := r.engineKey(a); got != "CUDA0" {
		t.Errorf("engineKey(CUDA1) = %s, want CUDA0", got)
	}
	if got := r.engineKey(b); got != "CUDA1" {
		t.Errorf("engineKey(CUDA3) = %s, want CUDA1", got)
	}
}
