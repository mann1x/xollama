package engine

import (
	"strings"
	"testing"
)

// withPin routes the package against a given pin for the duration of a test,
// so a test that is about policy is not also a test of which channel this
// branch pins.
func withPin(t *testing.T, p Pin) {
	t.Helper()
	restore := loadPin
	loadPin = func() (Pin, error) { return p, nil }
	t.Cleanup(func() { loadPin = restore })
}

// TestRoutingIsTheTestedMatrixIntersectedWithThePin is the guard on the one
// failure mode that leaves no trace: an artifact that does not carry a payload
// does not refuse the load, it serves it on the CPU. The tested matrix says
// what the engine is validated on; the pin says what the bytes we ship can do.
// Routing has to be both.
func TestRoutingIsTheTestedMatrixIntersectedWithThePin(t *testing.T) {
	// A dev snapshot as opencoti actually publishes one: a bare APE that runs
	// everywhere, with a single Linux x86_64 CUDA payload beside it.
	dev := Pin{
		Repo: "o/r-dev", Channel: ChannelDev, Tag: "opencoti-0.10.5-c7-2609200554001",
		Assets: []Asset{
			{Kind: "bin", Arch: "x86_64"},
			{Kind: "bin", Arch: "aarch64"},
			{Kind: "dso", Arch: "x86_64"},
		},
		Accels: []Accel{{Arch: "x86_64", Backend: BackendCUDA}},
	}

	cases := []struct {
		name    string
		p       Platform
		b       Backend
		covered bool
	}{
		{"the one accelerator it ships", Platform{"linux", "amd64"}, BackendCUDA, true},
		{"CPU always, the host binary is the engine", Platform{"linux", "amd64"}, BackendCPU, true},
		// Vulkan is in the tested matrix, so without the pin check this would
		// route to opencoti and run on the CPU without saying so.
		{"Vulkan, which the snapshot has no payload for", Platform{"linux", "amd64"}, BackendVulkan, false},
		// The APE runs on aarch64; only the CUDA payload is missing.
		{"aarch64 CUDA, binary present but no payload", Platform{"linux", "arm64"}, BackendCUDA, false},
		// Dev snapshots publish no Windows GPU artifact at all.
		{"windows, which the snapshot does not ship", Platform{"windows", "amd64"}, BackendCUDA, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			reason := pinUncoveredIn(dev, tt.p, tt.b)
			if tt.covered && reason != "" {
				t.Fatalf("pinUncoveredIn() = %q, want covered", reason)
			}
			if !tt.covered {
				if reason == "" {
					t.Fatal("pinUncoveredIn() = \"\", want a refusal; a missing payload is served on the CPU in silence")
				}
				// The reason is the only place a user meets this, so it has to
				// name the artifact rather than blame the platform.
				if !strings.Contains(reason, dev.Tag) {
					t.Errorf("reason %q does not name the pinned artifact %q", reason, dev.Tag)
				}
			}
		})
	}
}

// TestCommittedPinCoversWhatThisBranchRoutes keeps the pin and the tested
// matrix from drifting apart on whichever branch this is. Every tested
// combination must either be served by the pinned bytes or be refused for a
// reason the pin states -- what must not happen is routing to a payload that
// is not there.
func TestCommittedPinCoversWhatThisBranchRoutes(t *testing.T) {
	pin, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	accelerated := 0
	for _, s := range tested {
		reason := pinUncoveredIn(pin, s.Platform, s.Backend)
		if reason == "" && s.Backend != BackendCPU {
			accelerated++
		}
		if reason != "" {
			t.Logf("%s/%s %s routes to llama.cpp: %s", s.Platform.OS, s.Platform.Arch, s.Backend, reason)
		}
	}
	// A pin that accelerates nothing at all is a packaging mistake, not a
	// deliberate channel choice.
	if accelerated == 0 {
		t.Error("the committed pin accelerates no tested backend; nothing would ever route to opencoti-llamafile")
	}
}

// TestDeviceRoutingConsultsThePin is the wiring test. The case above proves
// pinUncoveredIn answers correctly; this proves deviceUnsupported actually
// asks it. Without the call, SupportsDevice returns true here and the load is
// served on the CPU in silence.
func TestDeviceRoutingConsultsThePin(t *testing.T) {
	dev := Pin{
		Repo: "o/r-dev", Channel: ChannelDev, Tag: "opencoti-0.10.5-c7-2609200554001",
		Assets: []Asset{{Kind: "bin", Arch: "x86_64"}, {Kind: "dso", Arch: "x86_64"}},
		Accels: []Accel{{Arch: "x86_64", Backend: BackendCUDA}},
	}
	withPin(t, dev)

	linux := Platform{"linux", "amd64"}
	// Vulkan is in the tested matrix and this artifact has no Vulkan payload.
	vulkan := Device{Backend: BackendVulkan}
	if SupportsDevice(linux, vulkan) {
		t.Error("SupportsDevice() = true for a backend the pinned artifact carries no payload for")
	}
	if r := deviceUnsupported(linux, vulkan); !strings.Contains(r, dev.Tag) {
		t.Errorf("reason = %q, want it to name the pinned artifact", r)
	}
	// The accelerator it does ship still routes, on a device above the floor.
	if !SupportsDevice(linux, Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 9}) {
		t.Error("SupportsDevice() = false for the backend the pin does cover")
	}
}

func TestAccelRowsMustHaveAnEngineToAccelerate(t *testing.T) {
	_, err := ParsePin("repo o/r\nrev " + strings.Repeat("a", 40) +
		"\ntag v1\nchannel dev\naccel aarch64 CUDA\nbin x86_64 a " + strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("ParsePin() = nil error; claiming acceleration for an arch with no bin row must not parse")
	}
	if !strings.Contains(err.Error(), "aarch64") {
		t.Errorf("error %q should name the arch", err)
	}
}

func TestDSORowsAreAddressedSeparatelyFromTheEngine(t *testing.T) {
	p, err := ParsePin("repo o/r\nrev " + strings.Repeat("a", 40) +
		"\ntag v1\nchannel dev\naccel x86_64 CUDA\n" +
		"bin x86_64 builds/18/engine " + strings.Repeat("a", 64) + "\n" +
		"dso x86_64 builds/18/ggml-cuda.so " + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	// The engine is always the bin row, even when a dso shares its arch label.
	bin, ok := p.Asset("x86_64")
	if !ok || bin.Path != "builds/18/engine" {
		t.Fatalf("Asset(x86_64) = %+v, %v; want the bin row", bin, ok)
	}
	dso, ok := p.DSO("x86_64")
	if !ok || dso.Path != "builds/18/ggml-cuda.so" {
		t.Fatalf("DSO(x86_64) = %+v, %v; want the dso row", dso, ok)
	}
}

func TestAccelBackendMustBeSpelledAsDiscoverySpellsIt(t *testing.T) {
	// "cuda" is how everyone writes it and is not how ml.DeviceID.Library
	// does; a typo here would silently accelerate nothing.
	_, err := ParsePin("repo o/r\nrev " + strings.Repeat("a", 40) +
		"\ntag v1\nchannel dev\naccel x86_64 cuda\nbin x86_64 a " + strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("ParsePin() = nil error; an unknown backend spelling must not parse")
	}
}
