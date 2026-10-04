package tweak

import (
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

func TestALinkIsGBpsAPCIeShapeOrAuto(t *testing.T) {
	for in, want := range map[string]float64{
		"4x16": 25.6, "gen5x8": 25.6, "3.0x16": 12.8, "6x16": 102.4, "25.6": 25.6, "auto": 0,
	} {
		got, err := parseLink(in)
		if err != nil || got != want {
			t.Errorf("parseLink(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"7x16", "4x3", "fast", "-1", "4096"} {
		if _, err := parseLink(in); err == nil {
			t.Errorf("parseLink(%q) accepted", in)
		}
	}
}

func TestOneGPUIsOneRowWhateverItsBackends(t *testing.T) {
	got := physicalGPUs([]api.XollamaDevice{
		{Backend: "CUDA", PCIID: "0000:01:00.0", Description: "RTX 3090", TotalMemory: 24 << 30},
		{Backend: "Vulkan", PCIID: "01:00.0"},
		{Backend: "Vulkan", PCIID: "0000:02:00.0", Name: "Vulkan1"},
		{Backend: "Metal"},
	})
	if len(got) != 2 || strings.Join(got[0].Backends, ",") != "CUDA,Vulkan" || got[1].Name != "Vulkan1" {
		t.Fatalf("got %+v", got)
	}
}

func TestGPUFlagsEditOneDevice(t *testing.T) {
	cmd := gpuCommand(Options{})
	if err := cmd.Flags().Parse([]string{"--priority", "7", "--backend", "cuda", "--link", "4x8", "--split", "spread", "--split-mode", "row"}); err != nil {
		t.Fatal(err)
	}
	g := &xollama.GPUSettings{}
	if err := gpuFromFlags(cmd, []string{"01:00.0"}, g); err != nil {
		t.Fatal(err)
	}
	d, ok := g.Device("0000:01:00.0")
	if !ok || d.Priority != 7 || d.Backend != "CUDA" || d.LinkGBps != 12.8 || g.SplitPolicy != "spread" || g.SplitMode != "row" {
		t.Fatalf("got %+v %+v", g, d)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}

	unset := gpuCommand(Options{})
	unset.Flags().Parse([]string{"--priority", "unset", "--backend", "unset", "--link", "auto"})
	if err := gpuFromFlags(unset, []string{"0000:01:00.0"}, g); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Device("0000:01:00.0"); ok {
		t.Fatal("a device stating nothing is kept")
	}

	noID := gpuCommand(Options{})
	noID.Flags().Parse([]string{"--disable"})
	if err := gpuFromFlags(noID, nil, g); err == nil {
		t.Fatal("--disable without a PCI ID was accepted")
	}
}

func TestTheGPUWalkForcesALinkByGenerationAndLanes(t *testing.T) {
	g := &xollama.GPUSettings{}
	gpus := []gpuEntry{{Key: "0000:01:00.0", Name: "RTX 3090", Backends: []string{"CUDA", "Vulkan"}}}
	// gpu menu: the GPU (2); use: yes; priority 3; backend: CUDA; link:
	// pcie, gen 4, lanes x8; then done.
	in := "2\nyes\n3\nCUDA\npcie\ngen4\nx8\ndone\n"
	a := newAsker(strings.NewReader(in), &strings.Builder{})
	if err := askGPU(a, g, gpus, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := g.Device("0000:01:00.0")
	if d.Priority != 3 || d.Backend != "CUDA" || d.LinkGBps != 12.8 {
		t.Fatalf("got %+v", d)
	}
}

func TestTheLinkWizardStartsFromWhatWasDetected(t *testing.T) {
	var p api.LinkProbeDevice
	p.Link.Detected, p.Link.Gen, p.Link.Width = true, 5, 8
	p.Measured.Available, p.Measured.H2DGBps = true, 24.1
	d := &xollama.GPUDevice{ID: "0000:01:00.0"}
	var out strings.Builder
	// link: pcie; generation and lanes: the defaults.
	a := newAsker(strings.NewReader("pcie\n\n\n"), &out)
	if err := askLink(a, d, &p); err != nil {
		t.Fatal(err)
	}
	if d.LinkGBps != 25.6 {
		t.Fatalf("link = %v, want PCIe 5.0 x8 = 25.6", d.LinkGBps)
	}
	if !strings.Contains(out.String(), "measured 24.1 GB/s") || !strings.Contains(out.String(), "pcie-gen [3]>") {
		t.Fatalf("the detected link is not shown or not the default:\n%s", out.String())
	}
}

func TestAGPUWithoutAPCIIDIsWrittenByName(t *testing.T) {
	devs := []api.XollamaDevice{
		{Backend: "CUDA", ID: "0", PCIID: "0000:11:00.0", Description: "NVIDIA GeForce RTX 3090"},
		{Backend: "Vulkan", ID: "0", Name: "Vulkan0", Description: "AMD Radeon(TM) Graphics", Integrated: true},
		{Backend: "Vulkan", ID: "2", Name: "Vulkan2", Description: "AMD Radeon RX 9070 XT"},
	}
	if got := selectorFor(devs[2], devs); got != "name:AMD Radeon RX 9070 XT" {
		t.Errorf("a lone card: %q", got)
	}
	if got := selectorFor(devs[0], devs); got != "0000:11:00.0" {
		t.Errorf("a card with a PCI ID: %q", got)
	}
	twins := append(slices.Clone(devs), api.XollamaDevice{Backend: "Vulkan", ID: "3", Description: "AMD Radeon RX 9070 XT"})
	if a, b := selectorFor(twins[2], twins), selectorFor(twins[3], twins); a != "name:AMD Radeon RX 9070 XT#1" || b != "name:AMD Radeon RX 9070 XT#2" {
		t.Errorf("twins: %q %q", a, b)
	}

	// The model's device answer keeps a name whole, spaces and all.
	c := &xollama.Config{Devices: &xollama.Devices{Backend: "Vulkan"}}
	if err := setDeviceIDs(c, "name:AMD Radeon RX 9070 XT, integrated 0000:11:00.0"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"name:AMD Radeon RX 9070 XT", "integrated", "0000:11:00.0"}; !slices.Equal(c.Devices.IDs, want) {
		t.Errorf("ids = %q, want %q", c.Devices.IDs, want)
	}
	options := []string{"name:AMD Radeon(TM) Graphics", "name:AMD Radeon RX 9070 XT"}
	if got := resolveDeviceMenu("2", options); got != "name:AMD Radeon RX 9070 XT" {
		t.Errorf("menu pick 2 = %q", got)
	}
	if got := resolveDeviceMenu("name:AMD Radeon RX 9070 XT", options); got != "name:AMD Radeon RX 9070 XT" {
		t.Errorf("a typed name was taken apart: %q", got)
	}

	// The server's GPU table lists it, and its flags reach it.
	gpus := physicalGPUs(devs)
	if len(gpus) != 3 || gpus[2].Key != "name:AMD Radeon RX 9070 XT" || gpus[0].Key != "0000:11:00.0" {
		t.Fatalf("got %+v", gpus)
	}
	cmd := gpuCommand(Options{})
	cmd.Flags().Parse([]string{"--priority", "9"})
	g := &xollama.GPUSettings{}
	if err := gpuFromFlags(cmd, []string{"NAME:amd radeon rx 9070 xt"}, g); err != nil {
		t.Fatal(err)
	}
	if d, ok := g.Device("name:AMD Radeon RX 9070 XT"); !ok || d.Priority != 9 || g.Validate() != nil {
		t.Fatalf("got %+v", g)
	}
	link := gpuCommand(Options{})
	link.Flags().Parse([]string{"--link", "4x16"})
	if err := gpuFromFlags(link, []string{"name:AMD Radeon RX 9070 XT"}, g); err == nil || !strings.Contains(err.Error(), "PCI ID") {
		t.Fatalf("a link forced on a GPU without a PCI ID: %v", err)
	}
	var out strings.Builder
	printGPUTable(&out, g, gpus, nil)
	if !strings.Contains(out.String(), "name:AMD Radeon RX 9070 XT") || strings.Contains(out.String(), "not found now") {
		t.Errorf("table:\n%s", out.String())
	}
}
