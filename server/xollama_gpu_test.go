package server

import (
	"net/http"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

const gib = uint64(1) << 30

func gpu(lib, id, pci string, freeGiB uint64) ml.DeviceInfo {
	d := ml.DeviceInfo{DeviceID: ml.DeviceID{Library: lib, ID: id}, PCIID: pci}
	d.FreeMemory = freeGiB * gib
	d.TotalMemory = freeGiB * gib
	return d
}

// setGPUPolicy writes g through the settings route, as tweak server gpu does.
func setGPUPolicy(t *testing.T, g *xollama.GPUSettings) {
	t.Helper()
	settingsHome(t)
	w, _ := settingsCall(t, keyRoutes(t), "127.0.0.1:4000", api.SettingsRequest{GPU: g}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("set gpu policy: %d %s", w.Code, w.Body)
	}
}

func gpuIDs(gpus []ml.DeviceInfo) []string {
	out := make([]string, len(gpus))
	for i, g := range gpus {
		out[i] = g.Library + ":" + g.PCIID
	}
	return out
}

func TestNoGPUPolicyChangesNothing(t *testing.T) {
	settingsHome(t)
	gpus := []ml.DeviceInfo{gpu("CUDA", "0", "0000:01:00.0", 24), gpu("Vulkan", "0", "0000:01:00.0", 24)}
	if got := applyGPUPolicy(nil, gpus); len(got) != 2 || &got[0] != &gpus[0] {
		t.Fatalf("got %v", gpuIDs(got))
	}
	if gpuPolicyEnvs(gpus) != nil || neverSplit() || priorityOrder(gpus[0], gpus[1]) != 0 {
		t.Fatal("no policy still steers the load")
	}
	if llm.DeviceEnvs == nil {
		t.Fatal("the launch hook is not wired")
	}
}

func TestTheGPUPolicyDisablesOrdersAndPicksABackend(t *testing.T) {
	setGPUPolicy(t, &xollama.GPUSettings{Devices: []xollama.GPUDevice{
		{ID: "0000:01:00.0", Priority: 1, Backend: "CUDA"},
		{ID: "0000:02:00.0", Priority: 5},
		{ID: "0000:03:00.0", Disabled: true},
	}})
	gpus := []ml.DeviceInfo{
		gpu("CUDA", "0", "0000:01:00.0", 24),
		gpu("Vulkan", "0", "0000:01:00.0", 24),
		gpu("Vulkan", "1", "0000:02:00.0", 16),
		gpu("Vulkan", "2", "0000:03:00.0", 16),
	}
	got := gpuIDs(applyGPUPolicy(nil, gpus))
	want := []string{"Vulkan:0000:02:00.0", "CUDA:0000:01:00.0"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}

	// A model's own pin keeps what it pinned, the disabled GPU included.
	pinned := &xollama.Config{Devices: &xollama.Devices{Backend: "Vulkan", IDs: []string{"0000:03:00.0"}}}
	if got := gpuIDs(applyGPUPolicy(pinned, gpus)); len(got) != 4 {
		t.Fatalf("a pinned model lost devices: %v", got)
	}
}

func TestPriorityBeatsFreeMemoryForASingleGPU(t *testing.T) {
	setGPUPolicy(t, &xollama.GPUSettings{Devices: []xollama.GPUDevice{{ID: "0000:02:00.0", Priority: 9}}})
	big, small := gpu("CUDA", "0", "0000:01:00.0", 48), gpu("CUDA", "1", "0000:02:00.0", 24)
	selected, _ := selectLlamaServerPlacement(ml.SystemInfo{}, []ml.DeviceInfo{big, small}, 8*gib, api.Options{Runner: api.Runner{NumGPU: -1}})
	if len(selected) != 1 || selected[0].PCIID != "0000:02:00.0" {
		t.Fatalf("selected %v, want the priority GPU", gpuIDs(selected))
	}
}

func TestSingleNeverSplitsAndSpreadAlwaysDoes(t *testing.T) {
	two := []ml.DeviceInfo{gpu("CUDA", "0", "0000:01:00.0", 24), gpu("CUDA", "1", "0000:02:00.0", 24)}
	opts := api.Options{Runner: api.Runner{NumGPU: -1}}

	setGPUPolicy(t, &xollama.GPUSettings{SplitPolicy: xollama.SplitSingle})
	if selected, _ := selectLlamaServerPlacement(ml.SystemInfo{}, two, 40*gib, opts); len(selected) != 1 {
		t.Fatalf("single split a model too large for one GPU: %v", gpuIDs(selected))
	}

	setGPUPolicy(t, &xollama.GPUSettings{SplitPolicy: xollama.SplitSpread, SplitMode: "row"})
	if selected, _ := selectLlamaServerPlacement(ml.SystemInfo{}, two, 4*gib, opts); len(selected) != 2 {
		t.Fatalf("spread kept a small model on one GPU: %v", gpuIDs(selected))
	}
	if env := gpuPolicyEnvs(two); env["LLAMA_ARG_SPLIT_MODE"] != "row" {
		t.Fatalf("split mode not passed: %v", env)
	}
	if env := gpuPolicyEnvs(two[:1]); env != nil {
		t.Fatalf("a one-GPU load got a split mode: %v", env)
	}
}

func TestEachGPUOfALoadGetsItsOwnForcedLink(t *testing.T) {
	setGPUPolicy(t, &xollama.GPUSettings{Devices: []xollama.GPUDevice{
		{ID: "0000:01:00.0", LinkGBps: 25.6},
		{ID: "02:00.0", LinkGBps: 12.8},
	}})
	a, b := gpu("CUDA", "0", "0000:01:00.0", 24), gpu("CUDA", "1", "0000:02:00.0", 24)
	probed := gpu("CUDA", "2", "0000:03:00.0", 24)
	if env := gpuPolicyEnvs([]ml.DeviceInfo{a}); env["OPENCOTI_LINK_GBPS"] != "0000:01:00.0=25.6" {
		t.Fatalf("one GPU: %v", env)
	}
	if env := gpuPolicyEnvs([]ml.DeviceInfo{a, b, probed}); env["OPENCOTI_LINK_GBPS"] != "0000:01:00.0=25.6,0000:02:00.0=12.8" {
		t.Fatalf("three GPUs, two forced: %v", env)
	}
	if env := gpuPolicyEnvs([]ml.DeviceInfo{probed}); env != nil {
		t.Fatalf("a GPU without a forced link got one: %v", env)
	}
}

func TestABadGPUPolicyIsRefused(t *testing.T) {
	settingsHome(t)
	for _, g := range []*xollama.GPUSettings{
		{SplitPolicy: "sometimes"},
		{SplitMode: "tensor"},
		{SplitPolicy: xollama.SplitSingle, SplitMode: "row"},
		{Devices: []xollama.GPUDevice{{ID: "gpu0"}}},
		{Devices: []xollama.GPUDevice{{ID: "0000:01:00.0", Backend: "CPU"}}},
		{Devices: []xollama.GPUDevice{{ID: "01:00.0"}, {ID: "0000:01:00.0"}}},
	} {
		w, _ := settingsCall(t, keyRoutes(t), "127.0.0.1:4000", api.SettingsRequest{GPU: g}, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%+v: status %d, want 400", g, w.Code)
		}
	}
}
