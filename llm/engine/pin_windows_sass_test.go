package engine

import (
	"strings"
	"testing"
)

// A dev snapshot publishes Windows as the bare APE plus a CUDA dso, with no
// -gpu bin. That shape has to be packaged and routed; a -gpu bin still wins.
func TestArchForTakesTheBareWindowsBinOnlyWithoutAGPUOne(t *testing.T) {
	bare := Pin{Assets: []Asset{{Kind: "bin", Arch: "win-x86_64"}, {Kind: "dso", Arch: "win-x86_64"}}}
	both := Pin{Assets: []Asset{{Kind: "bin", Arch: "win-x86_64"}, {Kind: "bin", Arch: "win-x86_64-gpu"}}}
	none := Pin{Assets: []Asset{{Kind: "bin", Arch: "x86_64"}}}
	for _, tt := range []struct {
		name string
		pin  Pin
		want string
	}{
		{"bare only", bare, "win-x86_64"},
		{"gpu wins", both, "win-x86_64-gpu"},
		{"no windows row", none, "win-x86_64-gpu"},
	} {
		got, err := tt.pin.ArchFor("windows", "amd64")
		if err != nil || got != tt.want {
			t.Errorf("%s: ArchFor(windows, amd64) = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
	if got, _ := bare.ArchFor("linux", "amd64"); got != "x86_64" {
		t.Errorf("linux must not be touched by the Windows fallback, got %q", got)
	}
}

func TestTheCommittedPinServesWindowsCUDA(t *testing.T) {
	p, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	win := Platform{OS: "windows", Arch: "amd64"}
	if reason := pinUncoveredIn(p, win, BackendCUDA); reason != "" {
		t.Errorf("windows CUDA: %s", reason)
	}
	if reason := pinUncoveredIn(p, win, BackendVulkan); reason == "" {
		t.Error("windows Vulkan is routed to a pin that carries no Vulkan payload")
	}
	// The "#! cuda-sass" line is what keeps 8.0/9.0 cards off these bytes.
	if p.CoversCUDA(8, 0) || !p.CoversCUDA(8, 6) {
		t.Errorf("the committed pin's cuda-sass is not in force: %v", p.CUDASASS)
	}
}

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
		t.Error("a pin without cuda-sass must not narrow anything")
	}
}

func TestCUDASASSParses(t *testing.T) {
	base := "repo o/r\nrev " + strings.Repeat("a", 40) + "\ntag t\nchannel dev\nbin x86_64 p " + strings.Repeat("b", 64) + "\n"
	p, err := ParsePin(base + "cuda-sass 86 120\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.CUDASASS) != 2 || p.CUDASASS[0] != 86 || p.CUDASASS[1] != 120 {
		t.Errorf("CUDASASS = %v", p.CUDASASS)
	}
	// opencoti publishes it as a "#!" line (mail #449); an unknown "#!" key
	// is a comment, and a plain comment never parses as a directive.
	p, err = ParsePin(base + "#! cuda-sass 86 120\n#! cuda-ptx 90\n# cuda-sass 75\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.CUDASASS) != 2 || p.CUDASASS[0] != 86 {
		t.Errorf("#! form: CUDASASS = %v", p.CUDASASS)
	}
	for _, bad := range []string{"cuda-sass\n", "cuda-sass sm_86\n", "cuda-sass 8\n"} {
		if _, err := ParsePin(base + bad); err == nil {
			t.Errorf("%q parsed", strings.TrimSpace(bad))
		}
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
