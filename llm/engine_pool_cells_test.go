package llm

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// rtx3090 is the card the case was measured on, as discovery reports it.
var rtx3090 = ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, PCIID: "0000:01:00.0", ComputeMajor: 8, ComputeMinor: 6, TotalMemory: 24 << 30}

func pooled(cells int) LlamaServerConfig {
	return LlamaServerConfig{Xollama: &xollama.Config{Version: 1, KV: &xollama.KV{PoolCells: cells}}}
}

// The case that asked for it: num_ctx 262144 on two live slots launched
// -c 524288, while the 3090 holds 655360 resident (eleven2go council,
// 2026-10-24). The stated pool replaces -c and nothing else.
func TestAStatedPoolIsTheEnginesContext(t *testing.T) {
	args := []string{"--model", "m.gguf", "-c", "524288", "-np", "2", "--kv-unified", "--max-parallel", "6"}
	got := withPoolCells(args, pooled(655360), 262144, 2, true)
	want := []string{"--model", "m.gguf", "-c", "655360", "-np", "2", "--kv-unified", "--max-parallel", "6"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %v\nwant   %v", got, want)
	}
	if args[3] != "524288" {
		t.Fatal("the caller's argv was changed in place")
	}
}

// Without --kv-unified the engine divides -c between its slots, and each slot
// would get a share no request was sized for: the stated pool is shared.
func TestAStatedPoolIsShared(t *testing.T) {
	got := withPoolCells([]string{"-c", "262144", "-np", "1"}, pooled(655360), 262144, 1, true)
	if !slices.Contains(got, "--kv-unified") {
		t.Fatalf("argv %v has no --kv-unified", got)
	}
}

// A pool smaller than one conversation's window could not hold a prompt ollama
// lets through, so the pool is never below num_ctx.
func TestAPoolBelowOneWindowIsTheWindow(t *testing.T) {
	got := withPoolCells([]string{"-c", "8192", "-np", "2"}, pooled(1000), 4096, 2, true)
	if got[1] != "4096" {
		t.Fatalf("-c %s, want num_ctx 4096", got[1])
	}
}

// Off means off: nothing stated, or stock llama.cpp serving the load, keeps
// upstream's argv and upstream's prediction.
func TestAnUnstatedPoolOrStockKeepsUpstreamsContext(t *testing.T) {
	args := []string{"-c", "524288", "-np", "2"}
	if got := withPoolCells(args, LlamaServerConfig{}, 262144, 2, true); !slices.Equal(got, args) {
		t.Fatalf("unstated: %v", got)
	}
	if got := withPoolCells(args, pooled(655360), 262144, 2, false); !slices.Equal(got, args) {
		t.Fatalf("stock llama.cpp: %v", got)
	}

	gpus := []ml.DeviceInfo{rtx3090}
	if got := PoolCellsFor(LlamaServerConfig{}, gpus, 262144, 524288); got != 524288 {
		t.Fatalf("unstated prediction: %d", got)
	}
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	if got := PoolCellsFor(pooled(655360), gpus, 262144, 524288); got != 524288 {
		t.Fatalf("XOLLAMA_ENGINE=llamacpp prediction: %d, want upstream's 524288", got)
	}
}

// The scheduler predicts the pool the launch starts.
func TestThePredictionIsTheLaunchedPool(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	gpus := []ml.DeviceInfo{rtx3090}
	if !WouldUseOpencoti(LlamaServerConfig{}, gpus) {
		t.Skip("this build's pin does not serve CUDA here")
	}
	if got := PoolCellsFor(pooled(655360), gpus, 262144, 524288); got != 655360 {
		t.Fatalf("predicted %d cells, the launch starts 655360", got)
	}
	launched := withPoolCells([]string{"-c", "524288"}, pooled(655360), 262144, 2, true)
	if launched[1] != "655360" {
		t.Fatalf("launched -c %s", launched[1])
	}
}
