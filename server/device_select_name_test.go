package server

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// eleven2goDevices is that machine on two boots of one day (2026-10-04):
// Vulkan on Windows gives no PCI ID, and the RX 9070 XT was Vulkan2 on one
// boot and Vulkan1 on the next.
func eleven2goDevices(xt, igpu string) []ml.DeviceInfo {
	devs := []ml.DeviceInfo{
		{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, Name: "CUDA0", Description: "NVIDIA GeForce RTX 3090", PCIID: "0000:11:00.0"},
		{DeviceID: ml.DeviceID{ID: igpu, Library: "Vulkan"}, Name: "Vulkan" + igpu, Description: "AMD Radeon(TM) Graphics", Integrated: true},
		{DeviceID: ml.DeviceID{ID: xt, Library: "Vulkan"}, Name: "Vulkan" + xt, Description: "AMD Radeon RX 9070 XT"},
	}
	slices.SortStableFunc(devs[1:], func(a, b ml.DeviceInfo) int { return strings.Compare(a.ID, b.ID) })
	return devs
}

func TestAPinByNameFollowsTheCardAcrossAVulkanReorder(t *testing.T) {
	pin := pinned("Vulkan", "name:AMD Radeon RX 9070 XT")
	for _, boot := range [][2]string{{"2", "0"}, {"1", "2"}, {"1", "0"}} {
		got, err := selectModelDevices(pin, eleven2goDevices(boot[0], boot[1]))
		if err != nil {
			t.Fatalf("9070 XT at Vulkan%s: %v", boot[0], err)
		}
		if len(got) != 1 || got[0].Description != "AMD Radeon RX 9070 XT" || got[0].ID != boot[0] {
			t.Errorf("9070 XT at Vulkan%s: got %v", boot[0], ids(got))
		}
	}

	// The same pin by index lands on whatever is there now.
	got, err := selectModelDevices(pinned("Vulkan", "2"), eleven2goDevices("1", "2"))
	if err != nil || len(got) != 1 || !got[0].Integrated {
		t.Fatalf("an index pin after the reorder: %v %v, want the integrated GPU now at 2", ids(got), err)
	}
}

func TestAPinByNameOfAMissingCardIsRefused(t *testing.T) {
	_, err := selectModelDevices(pinned("Vulkan", "name:AMD Radeon RX 7900 XTX"), eleven2goDevices("2", "0"))
	if err == nil || !strings.Contains(err.Error(), "name:AMD Radeon RX 7900 XTX") || !strings.Contains(err.Error(), "AMD Radeon RX 9070 XT") {
		t.Fatalf("got %v, want a refusal naming the pin and what is present", err)
	}
	// A CUDA card of that name is another backend's device.
	if _, err := selectModelDevices(pinned("Vulkan", "name:NVIDIA GeForce RTX 3090"), eleven2goDevices("2", "0")); err == nil {
		t.Fatal("a name matched a device of another backend")
	}
}

func TestTwoCardsOfOneNameAreToldApartByTheirPlace(t *testing.T) {
	twins := []ml.DeviceInfo{
		{DeviceID: ml.DeviceID{ID: "0", Library: "Vulkan"}, Description: "AMD Radeon(TM) Graphics", Integrated: true},
		{DeviceID: ml.DeviceID{ID: "1", Library: "Vulkan"}, Description: "AMD Radeon RX 9070 XT"},
		{DeviceID: ml.DeviceID{ID: "2", Library: "Vulkan"}, Description: "AMD Radeon RX 9070 XT"},
	}
	for sel, want := range map[string]string{
		"name:AMD Radeon RX 9070 XT#1": "Vulkan:1",
		"name:AMD Radeon RX 9070 XT#2": "Vulkan:2",
		"name:AMD Radeon RX 9070 XT":   "Vulkan:1,Vulkan:2",
	} {
		got, err := selectModelDevices(pinned("Vulkan", sel), twins)
		if err != nil || strings.Join(ids(got), ",") != want {
			t.Errorf("%s: got %v %v, want %s", sel, ids(got), err, want)
		}
	}
	if _, err := selectModelDevices(pinned("Vulkan", "name:AMD Radeon RX 9070 XT#3"), twins); err == nil {
		t.Error("a third card of two was found")
	}
}

func TestAnIndexPinStillLoadsAndIsSaidToBePositional(t *testing.T) {
	var log bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got, err := selectModelDevices(pinned("Vulkan", "2"), eleven2goDevices("2", "0"))
	if err != nil || len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("got %v %v", ids(got), err)
	}
	if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), "positional") {
		t.Errorf("an index pin loaded without a word: %q", log.String())
	}

	log.Reset()
	if _, err := selectModelDevices(pinned("Vulkan", "name:AMD Radeon RX 9070 XT"), eleven2goDevices("2", "0")); err != nil {
		t.Fatal(err)
	}
	if _, err := selectModelDevices(pinned("CUDA", "0000:11:00.0"), eleven2goDevices("2", "0")); err != nil {
		t.Fatal(err)
	}
	if log.Len() != 0 {
		t.Errorf("a pin by name or PCI ID was called positional: %q", log.String())
	}
}

func TestTheGPUPolicyReachesAGPUWithoutAPCIIDByName(t *testing.T) {
	setGPUPolicy(t, &xollama.GPUSettings{Devices: []xollama.GPUDevice{
		{ID: "name:AMD Radeon RX 9070 XT", Priority: 9},
		{ID: "name:AMD Radeon(TM) Graphics", Disabled: true},
	}})
	for _, boot := range [][2]string{{"2", "0"}, {"1", "2"}} {
		got := applyGPUPolicy(nil, eleven2goDevices(boot[0], boot[1]))
		if len(got) != 2 || got[0].Description != "AMD Radeon RX 9070 XT" || got[1].Library != "CUDA" {
			t.Errorf("9070 XT at Vulkan%s: got %v, want the 9070 XT first and the integrated GPU left out", boot[0], ids(got))
		}
	}
	devs := eleven2goDevices("2", "0")
	if priorityOrder(devs[2], devs[0]) <= 0 {
		t.Error("the named GPU's priority is not seen when two GPUs are compared")
	}
}
