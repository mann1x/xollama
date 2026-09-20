package engine

import "testing"

func TestSupportsCoversEveryTestedRow(t *testing.T) {
	for _, s := range tested {
		if !Supports(s.Platform, s.Backend) {
			t.Errorf("Supports(%v, %s) = false, want true", s.Platform, s.Backend)
		}
	}
}

func TestSupportsRefusesWhatIsNotTested(t *testing.T) {
	cases := []struct {
		name string
		p    Platform
		b    Backend
	}{
		// The routing rule that matters most: ROCm has no tested opencoti
		// backend, on any platform.
		{"rocm on linux amd64", Platform{"linux", "amd64"}, BackendROCm},
		{"rocm on windows", Platform{"windows", "amd64"}, BackendROCm},
		// macOS keeps ollama's own MLX path; this package never routes there.
		{"metal on darwin", Platform{"darwin", "arm64"}, BackendMetal},
		{"cpu on darwin", Platform{"darwin", "arm64"}, BackendCPU},
		// Published artifacts are x86_64 and aarch64 only.
		{"cuda on linux 386", Platform{"linux", "386"}, BackendCUDA},
		// No aarch64 Windows artifact.
		{"cuda on windows arm64", Platform{"windows", "arm64"}, BackendCUDA},
		// No Vulkan artifact validated on aarch64 linux.
		{"vulkan on linux arm64", Platform{"linux", "arm64"}, BackendVulkan},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if Supports(tt.p, tt.b) {
				t.Errorf("Supports(%v, %s) = true, want false", tt.p, tt.b)
			}
		})
	}
}

// ada is a modern CUDA device (RTX 4000-class, compute 8.9) — the ordinary
// case, spelled once so a test that is not about capability does not have to
// mention one.
func ada() Device { return Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 9} }

// TestSupportsDeviceHonoursTheCUDAComputeFloor pins the axis that Backend
// alone cannot express. Every device here is CUDA on linux/amd64, a row that
// IS in the tested matrix, so a policy that looks only at the backend passes
// all of these and routes a V100 into an artifact with no code for it.
func TestSupportsDeviceHonoursTheCUDAComputeFloor(t *testing.T) {
	linux := Platform{OS: "linux", Arch: "amd64"}
	// Route against an artifact that carries every payload, so this measures
	// the compute floor and not which channel the branch happens to pin. A dev
	// snapshot ships no Vulkan payload, and the Vulkan case below is a control
	// for "the floor is CUDA-specific", not a claim about the shipped pin.
	withPin(t, Pin{
		Repo: "o/r", Channel: ChannelRelease, Tag: "test",
		Assets: []Asset{{Kind: "bin", Arch: "x86_64"}},
		Accels: []Accel{
			{Arch: "x86_64", Backend: BackendCUDA},
			{Arch: "x86_64", Backend: BackendVulkan},
		},
	})

	cases := []struct {
		name string
		dev  Device
		want bool
	}{
		// Below the floor: llama.cpp's cuda_v12 payload is the only thing
		// that carries code for these.
		{"maxwell gtx 980", Device{BackendCUDA, 5, 2}, false},
		{"pascal gtx 1080 ti", Device{BackendCUDA, 6, 1}, false},
		{"volta tesla v100", Device{BackendCUDA, 7, 0}, false},

		// The floor itself and above.
		{"turing rtx 2080", Device{BackendCUDA, 7, 5}, true},
		{"ampere a100", Device{BackendCUDA, 8, 0}, true},
		{"ada rtx 4090", Device{BackendCUDA, 8, 9}, true},
		{"blackwell rtx 5090", Device{BackendCUDA, 12, 0}, true},

		// Unknown capability is not assumed modern: routing wrongly to
		// llama.cpp is logged, routing wrongly to opencoti is a silent
		// drop to CPU.
		{"capability discovery returned nothing", Device{Backend: BackendCUDA}, false},

		// The floor is CUDA-specific. Vulkan compiles SPIR-V at runtime and
		// CPU has no capability, so neither is gated on it.
		{"vulkan carries no compute floor", Device{Backend: BackendVulkan}, true},
		{"cpu carries no compute floor", Device{Backend: BackendCPU}, true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsDevice(linux, tt.dev); got != tt.want {
				t.Errorf("SupportsDevice(%v, %v) = %v, want %v (%s)",
					linux, tt.dev, got, tt.want, deviceUnsupported(linux, tt.dev))
			}
		})
	}
}

// TestDeviceUnsupportedNamesTheCapability keeps the log line diagnosable. The
// whole reason this floor exists is that the alternative failure is silent, so
// a reason that does not say which device and which capability is no better.
func TestDeviceUnsupportedNamesTheCapability(t *testing.T) {
	why := deviceUnsupported(Platform{OS: "linux", Arch: "amd64"}, Device{BackendCUDA, 7, 0})
	for _, want := range []string{"7.0", "7.5"} {
		if !contains(why, want) {
			t.Errorf("reason %q does not mention %q", why, want)
		}
	}
}
