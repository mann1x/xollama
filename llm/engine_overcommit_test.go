package llm

import (
	"io"
	"testing"

	"github.com/ollama/ollama/ml"
)

// eleven2go, 2026-11-21 (mail #2362): num_gpu 99 on the RX 9070 XT. The
// engine's fit declined to move a layer and every buffer went to the card,
// past what it had free; Windows paged the rest into shared memory.
const overcommittedLoadLog = `llama_prepare_model_devices: using device Vulkan2 (AMD Radeon RX 9070 XT) (0000:03:00.0) - 14986 MiB free
load_tensors: offloaded 65/65 layers to GPU
load_tensors:      Vulkan2 model buffer size = 16000.00 MiB
llama_kv_cache:    Vulkan2 KV buffer size =   200.00 MiB
llama_context:     Vulkan2 compute buffer size =   112.00 MiB
`

func overcommittedRunner(t *testing.T, opencoti, integrated bool) *llamaServerRunner {
	t.Helper()
	t.Setenv("XOLLAMA_ENGINE", "")
	runner := &llamaServerRunner{
		usedOpencoti:     opencoti,
		vramByDevice:     make(map[string]uint64),
		systemFreeAtLoad: make(map[string]uint64),
		gpus:             []ml.DeviceInfo{{Name: "Vulkan2", TotalMemory: 16304 << 20, Integrated: integrated}},
	}
	w := &memoryParsingWriter{inner: io.Discard, runner: runner}
	if _, err := w.Write([]byte(overcommittedLoadLog)); err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestAnOvercommittedLoadIsNotReportedAllOnTheGPU(t *testing.T) {
	r := overcommittedRunner(t, true, false)
	if got, want := r.Overcommitted()>>20, uint64(16312-14986); got != want {
		t.Fatalf("overcommitted %d MiB, want %d", got, want)
	}
	total, vram := r.MemorySize()
	if total>>20 != 16312 {
		t.Fatalf("size = %d MiB, want every buffer (16312)", total>>20)
	}
	if vram>>20 != 14986 {
		t.Fatalf("size_vram = %d MiB, want what the card had free (14986)", vram>>20)
	}
}

func TestALoadThatFitsReportsAllOnTheGPU(t *testing.T) {
	r := overcommittedRunner(t, true, false)
	r.systemFreeAtLoad["Vulkan2"] = 16400 << 20
	if over := r.Overcommitted(); over != 0 {
		t.Fatalf("overcommitted %d bytes on a load that fit", over)
	}
	if total, vram := r.MemorySize(); total != vram {
		t.Fatalf("size %d, size_vram %d: a load that fit is all on the GPU", total, vram)
	}
}

func TestStockLlamaCppAndIntegratedGPUsKeepUpstreamsFigures(t *testing.T) {
	for name, r := range map[string]*llamaServerRunner{
		"stock llama.cpp": overcommittedRunner(t, false, false),
		"integrated GPU":  overcommittedRunner(t, true, true),
	} {
		if over := r.Overcommitted(); over != 0 {
			t.Errorf("%s: overcommitted %d, want 0", name, over)
		}
		if total, vram := r.MemorySize(); total != vram {
			t.Errorf("%s: size %d, size_vram %d, want upstream's (equal)", name, total, vram)
		}
	}
}
