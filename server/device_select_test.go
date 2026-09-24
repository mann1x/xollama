package server

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

func solidPCDevices() []ml.DeviceInfo {
	return []ml.DeviceInfo{
		{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, Description: "NVIDIA GeForce RTX 3090", PCIID: "0000:01:00.0", TotalMemory: 24 << 30},
		{DeviceID: ml.DeviceID{ID: "1", Library: "Vulkan"}, Description: "AMD RADV RENOIR (ACO)", PCIID: "0000:18:00.0", Integrated: true, TotalMemory: 63 << 30},
	}
}

func pinned(backend string, ids ...string) *xollama.Config {
	return &xollama.Config{Devices: &xollama.Devices{Backend: backend, IDs: ids}}
}

func ids(devices []ml.DeviceInfo) []string {
	out := make([]string, len(devices))
	for i, d := range devices {
		out[i] = d.Library + ":" + d.ID
	}
	return out
}

func TestAModelPinnedToTheIGPURunsOnlyThere(t *testing.T) {
	for _, sel := range []string{"0000:18:00.0", "18:00.0", "1", "integrated"} {
		got, err := selectModelDevices(pinned("Vulkan", sel), solidPCDevices())
		if err != nil {
			t.Fatalf("%s: %v", sel, err)
		}
		if len(got) != 1 || got[0].PCIID != "0000:18:00.0" {
			t.Errorf("%s selected %v, want only the Renoir iGPU", sel, ids(got))
		}
	}
}

func TestTheSamePCIDeviceUnderAnotherBackendIsNotAMatch(t *testing.T) {
	// The 3090 is PCI 01:00.0 under CUDA; asking for it on Vulkan asks for a
	// Vulkan device that is not there, and must not quietly become CUDA.
	_, err := selectModelDevices(pinned("Vulkan", "0000:01:00.0"), solidPCDevices())
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range []string{"Vulkan device 0000:01:00.0", "CUDA 0 NVIDIA GeForce RTX 3090 [0000:01:00.0]", "Backends this installation carries"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

func TestAMissingPinnedDeviceIsARefusalNotAFallback(t *testing.T) {
	_, err := selectModelDevices(pinned("CUDA", "0000:02:00.0"), solidPCDevices())
	if err == nil || !strings.Contains(err.Error(), "CUDA device 0000:02:00.0") {
		t.Fatalf("got %v", err)
	}
	_, err = selectModelDevices(pinned("ROCm"), solidPCDevices())
	if err == nil || !strings.Contains(err.Error(), "ROCm (any device)") {
		t.Fatalf("got %v", err)
	}
}

func TestABackendPinTakesEveryDeviceOfThatBackend(t *testing.T) {
	gpus := append(solidPCDevices(), ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "1", Library: "CUDA"}, PCIID: "0000:02:00.0"})
	got, err := selectModelDevices(pinned("CUDA"), gpus)
	if err != nil || len(got) != 2 {
		t.Fatalf("want both CUDA cards, got %v %v", ids(got), err)
	}
	got, err = selectModelDevices(pinned("CUDA", "0000:02:00.0", "0"), gpus)
	if err != nil || len(got) != 2 {
		t.Fatalf("two ids spread the model across two cards, got %v %v", ids(got), err)
	}
}

func TestACPUPinUsesNoGPU(t *testing.T) {
	got, err := selectModelDevices(pinned("CPU"), solidPCDevices())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("want an empty, non-nil device list, got %v %v", got, err)
	}
}

func TestAnUnpinnedModelNeverLandsOnTheIGPUBesideADiscreteCard(t *testing.T) {
	got, _ := selectModelDevices(nil, solidPCDevices())
	if len(got) != 1 || got[0].Library != "CUDA" {
		t.Fatalf("an iGPU reports host RAM and would out-bid the card for a large model; got %v", ids(got))
	}
	onlyIGPU := solidPCDevices()[1:]
	got, _ = selectModelDevices(&xollama.Config{}, onlyIGPU)
	if len(got) != 1 {
		t.Fatal("with no discrete GPU the iGPU is the GPU")
	}
}
