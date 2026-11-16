package llm

import (
	"io"
	"testing"

	"github.com/ollama/ollama/ml"
)

// An assistant drafter is a second model in the engine process, and the
// engine prints the device line again when it opens it (c41/run4, c13,
// RTX 3090): the second number is what the target left free.
const assistantDrafterLoadLog = `llama_prepare_model_devices: using device CUDA0 (NVIDIA GeForce RTX 3090) (0000:01:00.0) - 22089 MiB free
load_tensors:        CUDA0 model buffer size = 13000.00 MiB
llama_kv_cache:      CUDA0 KV buffer size =  1500.00 MiB
llama_context:      CUDA0 compute buffer size =   700.00 MiB
llama_prepare_model_devices: using device CUDA0 (NVIDIA GeForce RTX 3090) (0000:01:00.0) - 6875 MiB free
`

func TestFreeAtLoadIsTheFirstDeviceLine(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	runner := &llamaServerRunner{
		vramByDevice:     make(map[string]uint64),
		systemFreeAtLoad: make(map[string]uint64),
		gpus:             []ml.DeviceInfo{{Name: "CUDA0", TotalMemory: 24576 << 20}},
	}
	w := &memoryParsingWriter{inner: io.Discard, runner: runner}
	if _, err := w.Write([]byte(assistantDrafterLoadLog)); err != nil {
		t.Fatal(err)
	}
	if got := runner.systemFreeAtLoad["CUDA0"] >> 20; got != 22089 {
		t.Fatalf("free at load = %d MiB, want 22089 (the line before the target allocated)", got)
	}
	// What discovery writes into its device table for the card: the 0 of
	// mails #2309/#2310 when the drafter's line was kept.
	infos := runner.GetDeviceInfos(t.Context())
	if len(infos) != 1 || infos[0].FreeMemory == 0 {
		t.Fatalf("free memory reported for CUDA0 = %v, want the card's real headroom", infos)
	}
	if got, want := infos[0].FreeMemory>>20, uint64(22089-15200); got != want {
		t.Fatalf("free memory = %d MiB, want %d", got, want)
	}
}

func TestStockEngineKeepsTheLastDeviceLine(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	m := map[string]uint64{}
	recordFreeAtLoad(m, "CUDA0", 22089<<20)
	recordFreeAtLoad(m, "CUDA0", 6875<<20)
	if m["CUDA0"]>>20 != 6875 {
		t.Fatalf("XOLLAMA_ENGINE=llamacpp must keep upstream's last-line rule, got %d MiB", m["CUDA0"]>>20)
	}
}
