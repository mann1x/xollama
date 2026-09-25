package discover

import (
	"context"
	"runtime"
	"testing"

	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

// Captured on solidPC (Ryzen Cezanne + RTX 3090, Debian 11) from
// opencoti-0.10.5-c7-2609242056001 with RADV and amdvlk 20.40 both installed,
// VK_ICD_FILENAMES listing amdvlk first. The service's default ICD order puts
// RADV first; this is the order that matters, because amdvlk's listing then
// sits at the index llama.cpp's single (deduplicated) Renoir device has.
const solidPCVulkanListing = `ggml_vulkan: 0 = Unknown AMD GPU (AMD proprietary driver) | uma: 1 | fp16: 1 | bf16: 0 | fp4: 0 | warp size: 64 | shared memory: 65536 | int dot: 0 | matrix cores: none
ggml_vulkan: 1 = AMD RADV RENOIR (ACO) (radv) | uma: 1 | fp16: 1 | bf16: 0 | fp4: 0 | warp size: 64 | shared memory: 32768 | int dot: 0 | matrix cores: none
Available devices:
  Vulkan0: Unknown AMD GPU (64600 MiB, 61370 MiB free)
  Vulkan1: AMD RADV RENOIR (ACO) (64601 MiB, 64553 MiB free)
`

const solidPCCUDAListing = `ggml_cuda_init: found 1 CUDA devices (Total VRAM: 24124 MiB):
  Device 0: NVIDIA GeForce RTX 3090, compute capability 8.6, VMM: yes, VRAM: 24124 MiB
Available devices:
  CUDA0: NVIDIA GeForce RTX 3090 (24124 MiB, 23853 MiB free)
`

const mib = 1024 * 1024

func solidPCLlamaCppDevices() []ml.DeviceInfo {
	return []ml.DeviceInfo{
		{
			DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, Description: "NVIDIA GeForce RTX 3090",
			PCIID: "0000:01:00.0", TotalMemory: 24124 * mib, FreeMemory: 23000 * mib, ComputeMajor: 8, ComputeMinor: 6,
		},
		{
			DeviceID: ml.DeviceID{ID: "0", Library: "Vulkan"}, Description: "AMD RADV RENOIR (ACO)",
			PCIID: "0000:18:00.0", Integrated: true, TotalMemory: 64000 * mib, FreeMemory: 60000 * mib,
		},
	}
}

func withOpencoti(t *testing.T, listings map[engine.Backend]string) {
	t.Helper()
	restoreList, restoreFind := opencotiListDevices, opencotiArtifact
	t.Cleanup(func() { opencotiListDevices, opencotiArtifact = restoreList, restoreFind })
	opencotiArtifact = func() (string, error) { return "/lib/ollama/opencoti", nil }
	opencotiListDevices = func(_ context.Context, _ string, b engine.Backend) (string, error) {
		return listings[b], nil
	}
}

func byLibrary(devices []ml.DeviceInfo, library string) []ml.DeviceInfo {
	var out []ml.DeviceInfo
	for _, d := range devices {
		if d.Library == library {
			out = append(out, d)
		}
	}
	return out
}

func TestTheEngineThatRunsTheIGPUIsTheOneThatNumbersIt(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, map[engine.Backend]string{engine.BackendVulkan: solidPCVulkanListing, engine.BackendCUDA: solidPCCUDAListing})

	vulkan := byLibrary(overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices()), "Vulkan")
	if len(vulkan) != 1 {
		t.Fatalf("the Renoir iGPU is one device listed twice; want 1 Vulkan device, got %d: %+v", len(vulkan), vulkan)
	}
	d := vulkan[0]
	if d.Description != "AMD RADV RENOIR (ACO)" || d.PCIID != "0000:18:00.0" {
		t.Errorf("the kept listing must be RADV with the identity discovery found, got %q pci %q", d.Description, d.PCIID)
	}
	if d.ID != "1" {
		t.Errorf("GGML_VK_VISIBLE_DEVICES must select opencoti's index for RADV (1), not llama.cpp's (0); got %q", d.ID)
	}
	if d.TotalMemory != 64601*mib || d.FreeMemory != 64553*mib {
		t.Errorf("memory must be opencoti's, got total %d free %d", d.TotalMemory/mib, d.FreeMemory/mib)
	}
	if !d.Integrated {
		t.Error("the iGPU must stay integrated")
	}
}

func TestCUDAMemoryComesFromTheEngineIdentityFromDiscovery(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, map[engine.Backend]string{engine.BackendCUDA: solidPCCUDAListing})

	cuda := byLibrary(overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices()), "CUDA")
	if len(cuda) != 1 || cuda[0].PCIID != "0000:01:00.0" || cuda[0].FreeMemory != 23853*mib || cuda[0].ComputeMajor != 8 {
		t.Fatalf("want the 3090 with opencoti's free memory and discovery's PCI ID, got %+v", cuda)
	}
}

func TestOffMeansOffForDiscovery(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	ran := false
	restore := opencotiListDevices
	t.Cleanup(func() { opencotiListDevices = restore })
	opencotiListDevices = func(context.Context, string, engine.Backend) (string, error) {
		ran = true
		return solidPCVulkanListing, nil
	}
	in := solidPCLlamaCppDevices()
	out := overlayOpencotiDevices(context.Background(), in)
	if ran {
		t.Fatal("XOLLAMA_ENGINE=llamacpp must not run the engine at all during discovery")
	}
	if len(out) != len(in) || out[1].ID != "0" || out[1].TotalMemory != in[1].TotalMemory {
		t.Fatalf("XOLLAMA_ENGINE=llamacpp must return discovery untouched, got %+v", out)
	}
}

func TestADeviceOnlyTheEngineSeesIsKept(t *testing.T) {
	// A payload with no llama.cpp Vulkan backend at all: opencoti is the only
	// thing that can see the iGPU, and it must still end up schedulable.
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, map[engine.Backend]string{engine.BackendVulkan: solidPCVulkanListing})

	vulkan := byLibrary(overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices()[:1]), "Vulkan")
	if len(vulkan) != 1 {
		t.Fatalf("want one Vulkan device, got %+v", vulkan)
	}
	if vulkan[0].Description != "AMD RADV RENOIR (ACO)" || vulkan[0].ID != "1" {
		t.Errorf("with no identity to join, the named driver's listing is kept over %q; got %q id %q",
			"Unknown AMD GPU", vulkan[0].Description, vulkan[0].ID)
	}
}

func TestADeviceTheServingEngineCannotSeeIsDropped(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, map[engine.Backend]string{engine.BackendVulkan: "Available devices:\n"})

	if v := byLibrary(overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices()), "Vulkan"); len(v) != 0 {
		t.Fatalf("opencoti listed no Vulkan device, so none may be scheduled on it; got %+v", v)
	}
}

func TestAVulkanListingOfACUDACardIsNotASecondGPU(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, map[engine.Backend]string{
		engine.BackendCUDA:   solidPCCUDAListing,
		engine.BackendVulkan: "Available devices:\n  Vulkan0: NVIDIA GeForce RTX 3090 (24124 MiB, 23800 MiB free)\n",
	})
	if v := byLibrary(overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices()[:1]), "Vulkan"); len(v) != 0 {
		t.Fatalf("the 3090 is CUDA's; want no Vulkan device, got %+v", v)
	}
}

func TestParseOpencotiDevicesReadsTheVerboseMetadata(t *testing.T) {
	v := parseOpencotiDevices(solidPCVulkanListing, "Vulkan")
	if len(v) != 2 || !v[0].integrated || !v[1].integrated || v[1].index != 1 {
		t.Fatalf("want two integrated Vulkan listings, got %+v", v)
	}
	c := parseOpencotiDevices(solidPCCUDAListing, "CUDA")
	if len(c) != 1 || c[0].computeMajor != 8 || c[0].computeMinor != 6 {
		t.Fatalf("want the 3090 at compute 8.6, got %+v", c)
	}
	if len(parseOpencotiDevices(solidPCCUDAListing, "Vulkan")) != 0 {
		t.Fatal("a CUDA device line must not be read as a Vulkan device")
	}
}

func TestOffMeansTheIntegratedVulkanGPUIsHiddenAsUpstreamHidesIt(t *testing.T) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		// filterIntegratedGPUs keeps every device on Apple Silicon, upstream and
		// fork alike: there the integrated GPU is the only GPU.
		t.Skip("integrated GPUs are never filtered on darwin/arm64")
	}
	igpu := ml.DeviceInfo{DeviceID: ml.DeviceID{Library: "Vulkan", ID: "0"}, Description: "AMD RADV RENOIR (ACO)", Integrated: true}
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	if got := filterIntegratedGPUs([]ml.DeviceInfo{igpu}); len(got) != 0 {
		t.Fatalf("XOLLAMA_ENGINE=llamacpp must drop an integrated Vulkan GPU exactly as upstream does, got %+v", got)
	}
	t.Setenv("XOLLAMA_ENGINE", "")
	if got := filterIntegratedGPUs([]ml.DeviceInfo{igpu}); len(got) != 1 {
		t.Fatal("with the fork's engine selection the iGPU is admitted by default")
	}
}
