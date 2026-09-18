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
