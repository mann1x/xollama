package server

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// eleven2go as the scheduler sees it after discovery: one entry per GPU, the
// 3090 under CUDA (upstream's choice), with 4 GiB of it in use by a loaded
// model; discovery also holds the 3090's Vulkan copy, read before that load.
func eleven2goGPUs() []ml.DeviceInfo {
	cuda := gpu("CUDA", "0", "0000:11:00.0", 20)
	vk9070 := gpu("Vulkan", "2", "0000:03:00.0", 15)
	igpu := gpu("Vulkan", "0", "0000:7a:00.0", 29)
	igpu.Integrated = true
	return []ml.DeviceInfo{cuda, vk9070, igpu}
}

func withCopies(t *testing.T, copies ...ml.DeviceInfo) {
	t.Helper()
	restore := discoverBackendCopies
	t.Cleanup(func() { discoverBackendCopies = restore })
	discoverBackendCopies = func() []ml.DeviceInfo { return slices.Clone(copies) }
}

func vk3090() ml.DeviceInfo { return gpu("Vulkan", "1", "0000:11:00.0", 23) }

// TestNoBackendAskedAddsNoCopy: without a pin or a policy naming a backend the
// scheduler's list is the one it always had, one entry per GPU.
func TestNoBackendAskedAddsNoCopy(t *testing.T) {
	settingsHome(t)
	withCopies(t, vk3090())
	gpus := eleven2goGPUs()
	got := withBackendCopies(nil, gpus)
	if len(got) != len(gpus) || &got[0] != &gpus[0] {
		t.Fatalf("an unasked load got the copies: %v", gpuIDs(got))
	}
	// A policy that only orders GPUs asks for no backend either.
	setGPUPolicy(t, &xollama.GPUSettings{Devices: []xollama.GPUDevice{{ID: "0000:03:00.0", Priority: 3}}})
	if got := withBackendCopies(nil, eleven2goGPUs()); len(got) != 3 {
		t.Fatalf("a priority-only policy got the copies: %v", gpuIDs(got))
	}
}

// TestAPolicyChoosingVulkanRunsTheThreeNinetyOnVulkan is xo-20: the policy's
// backend for the 3090 is Vulkan, so the load gets the 3090's Vulkan entry and
// not its CUDA one, with the free memory the scheduler accounted for the card.
func TestAPolicyChoosingVulkanRunsTheThreeNinetyOnVulkan(t *testing.T) {
	setGPUPolicy(t, &xollama.GPUSettings{Devices: []xollama.GPUDevice{{ID: "0000:11:00.0", Backend: "Vulkan"}}})
	withCopies(t, vk3090())
	got := applyGPUPolicy(nil, withBackendCopies(nil, eleven2goGPUs()))
	ids := gpuIDs(got)
	if !slices.Contains(ids, "Vulkan:0000:11:00.0") || slices.Contains(ids, "CUDA:0000:11:00.0") {
		t.Fatalf("the 3090 must run on Vulkan only, got %v", ids)
	}
	if !slices.Contains(ids, "Vulkan:0000:03:00.0") {
		t.Fatalf("the 9070 XT must stay, so one Vulkan split can use both: %v", ids)
	}
	for _, g := range got {
		if g.Library == "Vulkan" && g.PCIID == "0000:11:00.0" && g.FreeMemory != 20*gib {
			t.Errorf("the copy must share the card's accounted free memory: %d GiB, want 20", g.FreeMemory/gib)
		}
	}
}

// TestAGPUWithoutAChosenBackendKeepsUpstreamsChoice: a policy naming a backend
// for another GPU brings the copies in; the 3090, with none chosen, still runs
// on CUDA, and appears once.
func TestAGPUWithoutAChosenBackendKeepsUpstreamsChoice(t *testing.T) {
	setGPUPolicy(t, &xollama.GPUSettings{Devices: []xollama.GPUDevice{{ID: "0000:03:00.0", Backend: "Vulkan"}}})
	withCopies(t, vk3090())
	ids := gpuIDs(applyGPUPolicy(nil, withBackendCopies(nil, eleven2goGPUs())))
	n := 0
	for _, id := range ids {
		if id == "CUDA:0000:11:00.0" || id == "Vulkan:0000:11:00.0" {
			n++
		}
	}
	if n != 1 || !slices.Contains(ids, "CUDA:0000:11:00.0") {
		t.Fatalf("the 3090 must appear once, on CUDA: %v", ids)
	}
}

// TestAModelPinnedToVulkanCanUseTheThreeNinety: a model pinned to Vulkan by the
// 3090's and the 9070 XT's PCI IDs gets both, through one backend.
func TestAModelPinnedToVulkanCanUseTheThreeNinety(t *testing.T) {
	settingsHome(t)
	withCopies(t, vk3090())
	cfg := &xollama.Config{Devices: &xollama.Devices{Backend: "Vulkan", IDs: []string{"0000:11:00.0", "0000:03:00.0"}}}
	selected, err := selectModelDevices(cfg, withBackendCopies(cfg, eleven2goGPUs()))
	if err != nil {
		t.Fatal(err)
	}
	got := gpuIDs(applyGPUPolicy(cfg, selected))
	slices.Sort(got)
	if want := []string{"Vulkan:0000:03:00.0", "Vulkan:0000:11:00.0"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Without the copies the same pin is refused: what xo-20 fixed.
	if _, err := selectModelDevices(cfg, eleven2goGPUs()); err == nil {
		t.Fatal("the pin was satisfied without the 3090's Vulkan entry")
	}
}
