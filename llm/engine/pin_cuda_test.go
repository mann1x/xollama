package engine

import (
	"strings"
	"testing"
)

func TestCoversCUDAFollowsSASSCompatibility(t *testing.T) {
	p := Pin{CUDASASS: []int{86, 120}}
	for _, tt := range []struct {
		major, minor int
		want         bool
	}{
		{8, 6, true},
		{8, 9, true},
		{12, 0, true},
		{12, 1, true},
		{7, 5, false},
		{8, 0, false},
		{9, 0, false},
		{10, 0, false},
	} {
		if got := p.CoversCUDA(tt.major, tt.minor); got != tt.want {
			t.Errorf("CoversCUDA(%d.%d) = %v, want %v", tt.major, tt.minor, got, tt.want)
		}
	}
	if !(Pin{}).CoversCUDA(7, 5) {
		t.Error("a pin without a sass line must not narrow anything")
	}
}

// A device the payload has no SASS for is served on the CPU, silently. Routing
// sends it to llama.cpp instead, naming why.
func TestADeviceOutsideThePinnedSASSGoesToLlamaCPP(t *testing.T) {
	withPin(t, Pin{
		Tag: "opencoti-dev", Channel: ChannelDev, CUDASASS: []int{86, 120},
		Assets: []Asset{{Kind: "bin", Arch: "x86_64"}, {Kind: "dso", Arch: "x86_64"}},
		Accels: []Accel{{Arch: "x86_64", Backend: BackendCUDA}},
	})
	linux := Platform{OS: "linux", Arch: "amd64"}
	if why := deviceUnsupported(linux, Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 6}); why != "" {
		t.Errorf("8.6: %s", why)
	}
	for _, d := range []Device{
		{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 0},
		{Backend: BackendCUDA, ComputeMajor: 9, ComputeMinor: 0},
	} {
		why := deviceUnsupported(linux, d)
		if !strings.Contains(why, "8.6+, 12.0+") {
			t.Errorf("%s: reason %q does not name the pinned SASS", d, why)
		}
	}
}

// cuda12Pin has both CUDA libraries, as the cuda and cuda12 components state
// them.
func cuda12Pin(t *testing.T) Pin {
	return mustLoad(t,
		enginePin(row("any", "bin", "components/engine/v/opencoti-1", "engine")),
		libPin("cuda", "sass 86 120", row("x86_64", "cuda", "components/cuda/v/ggml-cuda-x86_64.so", "cuda")),
		libPin("cuda12", "sass 70", row("x86_64", "cuda12", "components/cuda12/v/ggml-cuda-cu12-x86_64.so", "cuda12")))
}

func TestTheCUDA12LibraryCoversOnlyWhatItNames(t *testing.T) {
	p := cuda12Pin(t)
	if !p.CoversCUDA12(7, 0) || p.CoversCUDA12(8, 6) || p.CoversCUDA12(6, 1) {
		t.Fatalf("the cuda12 sass is not in force: %v", p.CUDA12SASS)
	}
	// Without the component the CUDA 12 library covers nothing.
	plain := mustLoad(t,
		enginePin(row("any", "bin", "components/engine/v/opencoti-1", "engine")),
		libPin("cuda", "sass 86 120", row("x86_64", "cuda", "components/cuda/v/ggml-cuda-x86_64.so", "cuda")))
	if plain.CoversCUDA12(7, 0) {
		t.Fatal("a pin with no CUDA 12 library claims to cover 7.0")
	}
}

// A V100 is the CUDA 12 library's; an RTX 3090 is the CUDA 13 one's; one load
// across both cannot be served by one engine process and goes to llama.cpp.
// Both libraries sit beside one engine, so the load that needs CUDA 12 says so
// to the engine: on a host with a newer card its own pick would be CUDA 13.
func TestEachLoadRunsOnTheCUDALibraryItsGPUsNeed(t *testing.T) {
	p := cuda12Pin(t)
	withPin(t, p)
	t.Setenv(EnvPath, "")

	v100 := Device{Backend: BackendCUDA, ComputeMajor: 7, ComputeMinor: 0}
	rtx3090 := Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 6}
	a100 := Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 0}
	linux := Platform{OS: "linux", Arch: "amd64"}

	if why := deviceUnsupported(linux, v100); why != "" {
		t.Errorf("V100 refused: %s", why)
	}
	if why := deviceUnsupported(linux, a100); why == "" {
		t.Error("A100 (8.0) is routed to payloads with no code for it")
	}
	for _, tt := range []struct {
		name    string
		devices []Device
		cuda12  bool
		refused bool
	}{
		{"V100", []Device{v100}, true, false},
		{"RTX 3090", []Device{rtx3090}, false, false},
		{"V100 + RTX 3090", []Device{v100, rtx3090}, false, true},
		{"Vulkan", []Device{{Backend: BackendVulkan}}, false, false},
	} {
		cuda12, why := cudaPayload(p, Platform{OS: "linux", Arch: "amd64"}, tt.devices)
		if cuda12 != tt.cuda12 || (why != "") != tt.refused {
			t.Errorf("%s: cudaPayload = %v, %q", tt.name, cuda12, why)
		}
		if got := legacyCUDAOn(Platform{OS: "linux", Arch: "amd64"}, tt.devices); got != tt.cuda12 {
			t.Errorf("%s: LegacyCUDA = %v, want %v", tt.name, got, tt.cuda12)
		}
	}

	// An operator's engine is used as given and makes its own pick.
	t.Setenv(EnvPath, "/opt/my/opencoti")
	if LegacyCUDA([]Device{v100}) {
		t.Error("LegacyCUDA forced a library on the operator's XOLLAMA_ENGINE_PATH engine")
	}
}
