package systel

import "testing"

func TestIsIntegratedUMA(t *testing.T) {
	const gib = 1 << 30
	cases := []struct {
		name           string
		dedicated      uint64
		shared         uint64
		wantIntegrated bool
	}{
		// bug-320: a Radeon iGPU with a small UMA carve-out and a large shared pool.
		{"radeon igpu 509MB/33GB", 509 * 1024 * 1024, 33 * gib, true},
		{"apu 2GB UMA / 32GB shared", 2 * gib, 32 * gib, true},
		// A discrete board: its dedicated VRAM is larger than the integrated ceiling.
		{"rtx 3090 24GB / 32GB shared", 24 * gib, 32 * gib, false},
		{"radeon dgpu 16GB / 32GB shared", 16 * gib, 32 * gib, false},
		// Edge cases that must not be mistaken for an iGPU.
		{"no dedicated at all", 0, 32 * gib, false},
		{"small dedicated but tiny shared", 512 * 1024 * 1024, 2 * gib, false},
		{"dedicated below ceiling but shared not 4x", 4 * gib, 10 * gib, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isIntegratedUMA(c.dedicated, c.shared); got != c.wantIntegrated {
				t.Errorf("isIntegratedUMA(%d, %d) = %v, want %v", c.dedicated, c.shared, got, c.wantIntegrated)
			}
		})
	}
}
