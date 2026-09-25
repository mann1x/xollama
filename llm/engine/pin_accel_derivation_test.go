package engine

import "testing"

// opencoti dev snapshot 2609242056001, verbatim in shape: it states its payload
// set in a header comment and carries NO accel rows. Keyed on accel rows alone
// this pin accelerates nothing, and every GPU load routes to llama.cpp while
// the pinned engine holds a working Vulkan payload.
const pinWithNoAccelRows = `
# GPU payloads in this build: x86_64, x86_64-vulkan
# NOT in this build (CPU-only there on this snapshot): win-x86_64, aarch64, win-x86_64-vulkan
repo     ManniX-ITA/opencoti-llamafile-dev
rev      7b3b0fe0cecbaf4573a43a0ea4df13392596eb2f
tag      opencoti-0.10.5-c7-2609242056001
channel  dev
bin  x86_64         builds/x/engine                    6296bbc4c15d904cf641daafaf17084087d65185f6e608e97fc49a84b1a4d2de
dso  x86_64         builds/x/ggml-cuda.so              241bd3821f6b25f33767b4cb1380b2a658c9fbef574bd143ae1a18a613db71f1
dso  x86_64-vulkan  builds/x/ggml-vulkan-x86_64.so     5a85ce93572f8bb0615401b51a0f60fafde070d628ca73f72ed9f1add2e8902b
`

func TestAPinThatStatesNoAccelsStillAcceleratesWhatItsPayloadsProve(t *testing.T) {
	pin, err := ParsePin(pinWithNoAccelRows)
	if err != nil {
		t.Fatalf("ParsePin: %v", err)
	}
	for _, tc := range []struct {
		backend Backend
		want    bool
	}{
		{BackendCUDA, true},   // from `dso x86_64`
		{BackendVulkan, true}, // from `dso x86_64-vulkan`
		{BackendROCm, false},  // no payload, so no claim
		{BackendCPU, true},    // the host binary is the engine
	} {
		if got := pin.Accelerates("x86_64", tc.backend); got != tc.want {
			t.Errorf("Accelerates(x86_64, %s) = %v, want %v", tc.backend, got, tc.want)
		}
	}
}

// A dso for a platform the pin ships no engine for accelerates nothing HERE.
// Today's committed pin carries a win-x86_64 CUDA payload and no win-x86_64
// bin row; deriving an accel from it would trip the "accel has no bin row"
// check and make the pin unparseable.
func TestADSOForAnArchWithNoEngineIsNotAnAccelClaim(t *testing.T) {
	pin, err := ParsePin(`
repo     r/r
rev      7b3b0fe0cecbaf4573a43a0ea4df13392596eb2f
tag      t
channel  dev
bin  x86_64      builds/x/engine            ` + "6296bbc4c15d904cf641daafaf17084087d65185f6e608e97fc49a84b1a4d2de" + `
dso  win-x86_64  builds/x/ggml-cuda.dll     ` + "241bd3821f6b25f33767b4cb1380b2a658c9fbef574bd143ae1a18a613db71f1" + `
`)
	if err != nil {
		t.Fatalf("ParsePin: %v", err)
	}
	if pin.Accelerates("win-x86_64", BackendCUDA) {
		t.Error("a dso for an arch with no bin row must not become an accel claim")
	}
	if pin.Accelerates("x86_64", BackendCUDA) {
		t.Error("x86_64 has no dso here, so it accelerates no CUDA")
	}
}

// Derivation only ever ADDS, so a pin that states its accels explicitly keeps
// behaving exactly as it did.
func TestExplicitAccelRowsAreUnchangedByDerivation(t *testing.T) {
	pin, err := ParsePin(`
repo     r/r
rev      7b3b0fe0cecbaf4573a43a0ea4df13392596eb2f
tag      t
channel  dev
accel    x86_64  CUDA
bin  x86_64  builds/x/engine        ` + "6296bbc4c15d904cf641daafaf17084087d65185f6e608e97fc49a84b1a4d2de" + `
dso  x86_64  builds/x/ggml-cuda.so  ` + "241bd3821f6b25f33767b4cb1380b2a658c9fbef574bd143ae1a18a613db71f1" + `
`)
	if err != nil {
		t.Fatalf("ParsePin: %v", err)
	}
	if n := len(pin.Accels); n != 1 {
		t.Errorf("accels = %v, want exactly one (no duplicate from derivation)", pin.Accels)
	}
	if !pin.Accelerates("x86_64", BackendCUDA) {
		t.Error("the stated accel was lost")
	}
}

// The committed pin must keep accelerating what it did before this change.
func TestTheCommittedPinStillAcceleratesCUDAOnX86(t *testing.T) {
	pin, err := DefaultPin()
	if err != nil {
		t.Fatalf("DefaultPin: %v", err)
	}
	if !pin.Accelerates("x86_64", BackendCUDA) {
		t.Errorf("committed pin %s no longer accelerates CUDA on x86_64", pin.Tag)
	}
}
