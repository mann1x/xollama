package discover

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/ml"
)

// eleven2goVulkanListing is the c10 engine's Vulkan listing on eleven2go with
// OPENCOTI_LIST_DEVICE_IDS=1 (2026-10-08): NVIDIA prints its PCI address as
// the id, AMD under Windows a uuid and the PCI address beside it.
const eleven2goVulkanListing = `ggml_vulkan: Found 3 Vulkan devices:
ggml_vulkan: 0 = AMD Radeon(TM) Graphics (AMD proprietary driver) | uma: 1 | fp16: 1 | bf16: 0 | fp4: 0 | warp size: 32 | shared memory: 32768 | int dot: 1 | matrix cores: none
ggml_vulkan: 1 = NVIDIA GeForce RTX 3090 (NVIDIA) | uma: 0 | fp16: 1 | bf16: 1 | fp4: 0 | warp size: 32 | shared memory: 49152 | int dot: 1 | matrix cores: NV_coopmat2
ggml_vulkan: 2 = AMD Radeon RX 9070 XT (AMD proprietary driver) | uma: 0 | fp16: 1 | bf16: 1 | fp4: 0 | warp size: 64 | shared memory: 32768 | int dot: 1 | matrix cores: KHR_coopmat
Available devices:
  Vulkan0: AMD Radeon(TM) Graphics (31812 MiB, 30221 MiB free) id=uuid:000000007a0000000000000000000000 pci=0000:7a:00.0
  Vulkan1: NVIDIA GeForce RTX 3090 (24322 MiB, 23554 MiB free) id=0000:11:00.0
  Vulkan2: AMD Radeon RX 9070 XT (16304 MiB, 15419 MiB free) id=uuid:00000000030000000000000000000000 pci=0000:03:00.0
`

func TestTheEngineListingGivesEachDeviceItsPCIID(t *testing.T) {
	got := parseOpencotiDevices(eleven2goVulkanListing, "Vulkan")
	want := []string{"0000:7a:00.0", "0000:11:00.0", "0000:03:00.0"}
	if len(got) != 3 {
		t.Fatalf("parsed %d devices, want 3", len(got))
	}
	for i, d := range got {
		if d.pciID != want[i] {
			t.Errorf("%s: PCI ID %q, want %q", d.description, d.pciID, want[i])
		}
	}
	// An engine that prints no ids (older than list_device_ids_v1) and a uuid
	// with no PCI address beside it leave the PCI ID empty.
	for _, line := range []string{
		"  Vulkan1: NVIDIA GeForce RTX 3090 (24322 MiB, 23554 MiB free)",
		"  Vulkan2: AMD Radeon RX 9070 XT (16304 MiB, 15419 MiB free) id=uuid:00000000030000000000000000000000",
	} {
		if id := listedPCIID(line); id != "" {
			t.Errorf("%q: got PCI ID %q, want none", line, id)
		}
	}
}

func dev(lib, id, pci string, integrated bool) ml.DeviceInfo {
	return ml.DeviceInfo{DeviceID: ml.DeviceID{Library: lib, ID: id}, PCIID: pci, Integrated: integrated}
}

func TestOnlyADiscreteGPUWithOnePCIIDIsABackendCopy(t *testing.T) {
	cuda := dev("CUDA", "0", "0000:11:00.0", false)
	for _, c := range []struct {
		name string
		b    ml.DeviceInfo
		want bool
	}{
		{"its Vulkan entry", dev("Vulkan", "1", "0000:11:00.0", false), true},
		{"another card", dev("Vulkan", "2", "0000:03:00.0", false), false},
		{"no PCI ID to prove it", dev("Vulkan", "1", "", false), false},
		{"an integrated GPU", dev("Vulkan", "1", "0000:11:00.0", true), false},
		{"the same backend", dev("CUDA", "1", "0000:11:00.0", false), false},
	} {
		if got := isBackendCopy(cuda, c.b); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestDiscoveryKeepsTheCopyButGPUDevicesDoesNot: upstream's dedup keeps both
// entries of the 3090 while opencoti may serve; GPUDevices still returns one
// per GPU, CUDA's, and BackendCopies the Vulkan one. Under llamacpp the dedup
// is upstream's.
func TestDiscoveryKeepsTheCopyButGPUDevicesDoesNot(t *testing.T) {
	cuda, vk := dev("CUDA", "0", "0000:11:00.0", false), dev("Vulkan", "1", "0000:11:00.0", false)
	t.Setenv("XOLLAMA_ENGINE", "auto")
	if !keepBackendCopy(cuda, vk) || !keepBackendCopy(vk, cuda) {
		t.Fatal("the dedup must keep the 3090's Vulkan entry beside its CUDA one")
	}

	all := []ml.DeviceInfo{cuda, dev("Vulkan", "2", "0000:03:00.0", false), vk}
	one := onePerGPU(slices.Clone(all))
	if len(one) != 2 || slices.ContainsFunc(one, func(d ml.DeviceInfo) bool { return d.Library == "Vulkan" && d.PCIID == "0000:11:00.0" }) {
		t.Fatalf("GPUDevices must hold one entry per GPU, CUDA's for the 3090: %v", one)
	}

	restore := devices
	t.Cleanup(func() { devices = restore })
	devices = all
	copies := BackendCopies()
	if len(copies) != 1 || copies[0].Library != "Vulkan" || copies[0].PCIID != "0000:11:00.0" {
		t.Fatalf("BackendCopies: %v, want the 3090's Vulkan entry", copies)
	}

	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	if keepBackendCopy(cuda, vk) {
		t.Fatal("under llamacpp the dedup must be upstream's")
	}
}

// TestTheEnginesVulkanListingOfACUDACardIsKeptAsACopy: when llama.cpp's own
// Vulkan discovery did not list the 3090, opencoti's listing of it used to be
// dropped as "a second listing" of the CUDA card. With its PCI ID it is that
// card's Vulkan copy, and kept; without one (an older engine) it is dropped as
// before.
func TestTheEnginesVulkanListingOfACUDACardIsKeptAsACopy(t *testing.T) {
	onLinux(t)
	t.Setenv("XOLLAMA_ENGINE", "auto")
	cuda := ml.DeviceInfo{DeviceID: ml.DeviceID{Library: "CUDA", ID: "0"}, PCIID: "0000:11:00.0", Description: "NVIDIA GeForce RTX 3090", TotalMemory: 24575 * mib, LibraryPath: []string{ml.LibOllamaPath, "cuda_v13"}}
	vk9070 := ml.DeviceInfo{DeviceID: ml.DeviceID{Library: "Vulkan", ID: "1"}, Description: "AMD Radeon RX 9070 XT", TotalMemory: 16304 * mib, LibraryPath: []string{ml.LibOllamaPath, "vulkan"}}
	listed := parseOpencotiDevices(eleven2goVulkanListing, "Vulkan")[1:] // the 3090 and the 9070 XT

	merged := mergeOpencotiBackend([]ml.DeviceInfo{cuda, vk9070}, "Vulkan", listed, "auto")
	var vk3090 *ml.DeviceInfo
	for i, d := range merged {
		if d.Library == "Vulkan" && d.PCIID == "0000:11:00.0" {
			vk3090 = &merged[i]
		}
		if d.Library == "Vulkan" && d.Description == "AMD Radeon RX 9070 XT" && d.PCIID != "0000:03:00.0" {
			t.Errorf("the 9070 XT must take the engine's PCI ID: %q", d.PCIID)
		}
	}
	if vk3090 == nil || vk3090.ID != "1" {
		t.Fatalf("the 3090's Vulkan listing must be kept as a copy at the engine's index 1: %v", merged)
	}

	listed[0].pciID = "" // an engine that prints no ids
	merged = mergeOpencotiBackend([]ml.DeviceInfo{cuda, vk9070}, "Vulkan", listed, "auto")
	if slices.ContainsFunc(merged, func(d ml.DeviceInfo) bool { return d.Library == "Vulkan" && d.Description == "NVIDIA GeForce RTX 3090" }) {
		t.Fatal("without a PCI ID the second listing must be dropped, as before")
	}
}
