package engine

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// The loader is the one sidecar that is run rather than loaded. Staged like a
// library it is 0644, and the launch fails with "permission denied".
func TestCMakeStagesTheMacLoaderExecutable(t *testing.T) {
	cmake, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("cmake is not installed")
	}
	script, err := filepath.Abs("../../cmake/opencoti-fetch.cmake")
	if err != nil {
		t.Fatal(err)
	}
	src, dest := t.TempDir(), t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	}
	pin := sidecarPinHead +
		"bin " + ArchMacOS + " builds/v1/engine " + write("engine", "the engine") + "\n" +
		"#! sidecar " + ArchMacOS + " ape   builds/v1/" + MacLoader + " " + write(MacLoader, "the loader") + "\n" +
		"#! sidecar " + ArchMacOS + " metal builds/v1/ggml-metal-aarch64.dylib " + write("ggml-metal-aarch64.dylib", "metal") + "\n"
	if _, err := ParsePin(pin); err != nil {
		t.Fatalf("the Go parser refuses the pin CMake is given: %v", err)
	}
	pinFile := filepath.Join(src, "pin.txt")
	if err := os.WriteFile(pinFile, []byte(pin), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(cmake, "-DPIN_FILE="+pinFile, "-DARCH="+ArchMacOS, "-DDEST_DIR="+dest,
		"-DLOCAL_FILE="+filepath.Join(src, "engine"), "-DLOCAL_SIDECAR_DIR="+src, "-P", script).CombinedOutput()
	if err != nil {
		t.Fatalf("cmake: %v\n%s", err, out)
	}
	for name, executable := range map[string]bool{MacLoader: true, "ggml-metal-aarch64.dylib": false} {
		fi, err := os.Stat(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("%s was not staged: %v", name, err)
		}
		if got := fi.Mode()&0o111 != 0; got != executable {
			t.Errorf("%s staged with mode %v, executable = %v, want %v", name, fi.Mode(), got, executable)
		}
	}
}
