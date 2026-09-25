package tweak

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

func solidPC(t *testing.T) {
	t.Helper()
	setLiveSource(func() *api.XollamaDevicesResponse {
		return &api.XollamaDevicesResponse{
			Backends: []string{"CPU", "CUDA", "Vulkan"},
			Devices: []api.XollamaDevice{
				{Backend: "CUDA", ID: "0", PCIID: "0000:01:00.0", Description: "NVIDIA GeForce RTX 3090", TotalMemory: 24 << 30, FreeMemory: 23 << 30, Engine: "opencoti"},
				{Backend: "Vulkan", ID: "1", PCIID: "0000:18:00.0", Description: "AMD RADV RENOIR (ACO)", Integrated: true, TotalMemory: 63 << 30, FreeMemory: 60 << 30, Engine: "opencoti"},
			},
		}
	})
	t.Cleanup(func() { setLiveSource(nil) })
}

func TestTheBackendQuestionShowsTheServersDevices(t *testing.T) {
	solidPC(t)
	cfg, out, err := run(t, &xollama.Config{}, []string{"--device-backend"}, "2", "1", "y")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"NVIDIA GeForce RTX 3090", "0000:18:00.0", "integrated", "served by opencoti"} {
		if !strings.Contains(out, want) {
			t.Errorf("the walk does not show %q\n%s", want, out)
		}
	}
	// Menu: 1) CUDA 2) Vulkan 3) CPU 4) unset. Then the Vulkan device menu.
	if cfg.Devices == nil || cfg.Devices.Backend != "Vulkan" {
		t.Fatalf("devices = %+v, want Vulkan", cfg.Devices)
	}
	if len(cfg.Devices.IDs) != 1 || cfg.Devices.IDs[0] != "0000:18:00.0" {
		t.Fatalf("a menu pick must be written as the PCI ID, got %q", cfg.Devices.IDs)
	}
}

func TestOnlyBackendsWithADeviceAreOffered(t *testing.T) {
	solidPC(t)
	got := backendChoices(nil)
	if strings.Join(got, ",") != "CUDA,Vulkan,CPU" {
		t.Fatalf("offered %v; ROCm has no device here", got)
	}
}

func TestSeveralDevicesByMenuNumber(t *testing.T) {
	setLiveSource(func() *api.XollamaDevicesResponse {
		return &api.XollamaDevicesResponse{Devices: []api.XollamaDevice{
			{Backend: "CUDA", ID: "0", PCIID: "0000:01:00.0"},
			{Backend: "CUDA", ID: "1", PCIID: "0000:02:00.0"},
		}}
	})
	t.Cleanup(func() { setLiveSource(nil) })
	cfg, out, err := run(t, &xollama.Config{Devices: &xollama.Devices{Backend: "CUDA"}}, []string{"--devices"}, "", "2,1", "y")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Join(cfg.Devices.IDs, ",") != "0000:02:00.0,0000:01:00.0" {
		t.Fatalf("ids = %q", cfg.Devices.IDs)
	}
}

func TestAFlagNumberIsADeviceIndexNotAMenuLine(t *testing.T) {
	solidPC(t)
	cfg, out, err := run(t, &xollama.Config{}, []string{"--device-backend=cuda", "--devices=0"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if cfg.Devices.Backend != "CUDA" || strings.Join(cfg.Devices.IDs, ",") != "0" {
		t.Fatalf("devices = %+v", cfg.Devices)
	}
}

func TestChangingBackendForgetsTheOldSelection(t *testing.T) {
	c := &xollama.Config{Devices: &xollama.Devices{Backend: "CUDA", IDs: []string{"0000:01:00.0"}}}
	if err := setDeviceBackend(c, "vulkan"); err != nil {
		t.Fatal(err)
	}
	if len(c.Devices.IDs) != 0 {
		t.Fatal("a CUDA PCI ID is not a Vulkan selection; it must not carry over")
	}
}

func TestDevicesWithoutABackendAreNotAsked(t *testing.T) {
	solidPC(t)
	_, out, err := run(t, &xollama.Config{}, []string{"--devices"}, "", "y")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "devices.backend is not set") && !strings.Contains(out, "device-backend") {
		t.Fatalf("the scope pulls in the backend first\n%s", out)
	}
}

func TestShowListsTheDevicePin(t *testing.T) {
	rows := SettingRows(&xollama.Config{Devices: &xollama.Devices{Backend: "Vulkan", IDs: []string{"0000:18:00.0"}}})
	if len(rows) != 2 || rows[0] != [2]string{"devices.backend", "Vulkan"} || rows[1] != [2]string{"devices.ids", "0000:18:00.0"} {
		t.Fatalf("rows = %v", rows)
	}
}
