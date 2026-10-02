package tweak

import (
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
	gpus := []gpuEntry{{PCI: "0000:01:00.0", Name: "RTX 3090", Backends: []string{"CUDA", "Vulkan"}}}
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
