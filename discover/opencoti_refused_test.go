package discover

import (
	"context"
	"errors"
	"testing"

	"github.com/ollama/ollama/llm/engine"
)

// solidPCRefusedVulkan is the c10 release engine's Vulkan listing on solidPC
// (Debian 11, Mesa 20.3.5 and amdgpu-pro 20.40 for the Renoir iGPU): both
// ICDs refused, no device listed, exit 1.
const solidPCRefusedVulkan = `vulkan: loaded /srv/ml/gates/engine-x86_64-2610072136001/ggml-vulkan-x86_64.so (executable directory, 56322272 bytes)
ggml_vulkan: AMD RADV RENOIR (ACO) (driver radv 20.3.5) is REFUSED: RADV from Mesa 20 or older loses the device or crashes inside the driver (measured: 20.3.5); Mesa 25.0.7 runs the same requests correctly. Update the driver, or set OPENCOTI_VK_ALLOW_OLD_DRIVER=1 to use the device as it is.
ggml_vulkan: Unknown AMD GPU (driver AMD proprietary driver 2.0.154) is REFUSED: the AMD driver 2.0.154 or older (amdgpu-pro 20.40) aborts the engine while it loads. Update the driver, or set OPENCOTI_VK_ALLOW_OLD_DRIVER=1 to use the device as it is.
vulkan: INFO: Vulkan library loaded but no devices detected; trying next backend
fatal error: --gpu vulkan was explicitly requested but Vulkan is not usable on this system
`

func TestARefusedDriverIsReadFromTheListing(t *testing.T) {
	r := parseRefusals(solidPCRefusedVulkan)
	if len(r) != 2 {
		t.Fatalf("want both refused ICDs, got %+v", r)
	}
	if r[0].description != "AMD RADV RENOIR (ACO)" || r[0].driver != "radv 20.3.5" {
		t.Errorf("first refusal read as %+v", r[0])
	}
	if r[1].description != "Unknown AMD GPU" || r[1].driver != "AMD proprietary driver 2.0.154" {
		t.Errorf("second refusal read as %+v", r[1])
	}
	// A name with parentheses, printed twice (the probe child prints it too),
	// and a kept device, which is not a refusal.
	odd := "ggml_vulkan: Card (rev 2) (driver radv 20.3.5) is REFUSED: old.\n" +
		"ggml_vulkan: Card (rev 2) (driver radv 20.3.5) is REFUSED: old.\n" +
		"ggml_vulkan: Other (driver radv 20.3.5) is KEPT (OPENCOTI_VK_ALLOW_OLD_DRIVER=1): old.\n"
	if r := parseRefusals(odd); len(r) != 1 || r[0].description != "Card (rev 2)" || r[0].driver != "radv 20.3.5" {
		t.Errorf("want one refusal of \"Card (rev 2)\" on radv 20.3.5, got %+v", r)
	}
	if parseRefusals(solidPCVulkanListing) != nil {
		t.Error("a listing that refuses nothing has no refusals")
	}
}

// Every load went to the refused iGPU and failed ("Vulkan is not usable on
// this system") while a refused listing was taken for a failed one and
// llama.cpp's view of the device stood.
func TestADeviceTheEngineRefusesIsNotPlaced(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, map[engine.Backend]string{engine.BackendCUDA: solidPCCUDAListing})
	listCUDA := opencotiListDevices
	opencotiListDevices = func(ctx context.Context, a string, b engine.Backend) (string, error) {
		if b == engine.BackendVulkan {
			return solidPCRefusedVulkan, withRefusals(solidPCRefusedVulkan, errors.New("exit status 1"))
		}
		return listCUDA(ctx, a, b)
	}

	out := overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices())
	if v := byLibrary(out, "Vulkan"); len(v) != 0 {
		t.Fatalf("the engine refused every Vulkan device; none may be placed on, got %+v", v)
	}
	if c := byLibrary(out, "CUDA"); len(c) != 1 {
		t.Fatalf("the CUDA card is not touched by a Vulkan refusal, got %+v", c)
	}
}

// A listing that fails without refusing anything keeps llama.cpp's view, as
// before: nothing in it says the device is unusable.
func TestAFailedListingWithoutARefusalKeepsDiscovery(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, nil)
	opencotiListDevices = func(context.Context, string, engine.Backend) (string, error) {
		return "boom", withRefusals("boom", errors.New("exit status 1"))
	}
	if v := byLibrary(overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices()), "Vulkan"); len(v) == 0 {
		t.Fatal("a failed listing with no refusal must leave llama.cpp's devices in place")
	}
}
