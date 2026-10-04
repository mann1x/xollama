package engine

import (
	"slices"
	"testing"
)

// A Mac without the Xcode tools cannot start the engine any other way: run
// directly or through sh it compiles its own loader with cc.
func TestMacOSStartsTheEngineThroughItsLoader(t *testing.T) {
	const artifact = "/Applications/xOllama.app/Contents/Resources/engines/opencoti-x"
	const loader = "/Applications/xOllama.app/Contents/Resources/engines/" + MacLoader
	metal := []Device{{Backend: BackendMetal}}

	name, args := Command(artifact, []string{"--port", "1"}, metal, "darwin")
	if name != loader || args[0] != artifact || args[1] != "--server" {
		t.Fatalf("launch = %q %q, want the loader with the engine first", name, args)
	}
	if i := slices.Index(args, "--gpu"); i < 0 || args[i+1] != "apple" {
		t.Errorf("launch args %q do not select Metal with --gpu apple", args)
	}
	for what, got := range map[string]func() (string, []string){
		"device listing": func() (string, []string) { return EnumerateCommand(artifact, BackendMetal, "darwin") },
		"link probe":     func() (string, []string) { return LinkProbeCommand(artifact, BackendMetal, "darwin") },
	} {
		if name, args := got(); name != loader || args[0] != artifact {
			t.Errorf("%s = %q %q, want the loader with the engine first", what, name, args)
		}
	}
}

// The runtime directory of a Mac install is inside the signed app bundle, and
// a user-owned bundle is writable: the engine's HOME must not be put there.
func TestTheEnginesHomeIsNeverInsideTheMacAppBundle(t *testing.T) {
	const lib = "/Applications/xOllama.app/Contents/Resources"
	if got := payloadRoots("darwin", lib, "/Users/u"); len(got) != 1 || got[0] != "/Users/u/.ollama/engines/payload" {
		t.Errorf("payloadRoots on macOS = %q, want only the home directory's", got)
	}
	if got := payloadRoots("linux", "/usr/local/lib/ollama", "/home/u"); len(got) != 2 {
		t.Errorf("payloadRoots on Linux = %q, want the runtime directory and the home directory's", got)
	}
}

// The committed pin serves Apple silicon on Metal and nothing on an Intel Mac.
func TestTheCommittedPinServesAppleSiliconOnly(t *testing.T) {
	arm, intel := Platform{OS: "darwin", Arch: "arm64"}, Platform{OS: "darwin", Arch: "amd64"}
	if why := pinUncovered(arm, BackendMetal); why != "" {
		t.Errorf("Metal on Apple silicon is refused: %s", why)
	}
	if !Supports(arm, BackendMetal) || Supports(intel, BackendMetal) || Supports(intel, BackendCPU) {
		t.Error("the tested matrix must have darwin/arm64 and must not have darwin/amd64")
	}
	p, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, s := range p.Sidecars(ArchMacOS) {
		have[s.Role] = true
	}
	for _, kind := range []string{SidecarAPE, SidecarMetal, SidecarCodec, SidecarAudioCpp, "espeak"} {
		if !have[kind] {
			t.Errorf("the pin has no %q sidecar row for %s", kind, ArchMacOS)
		}
	}
}
