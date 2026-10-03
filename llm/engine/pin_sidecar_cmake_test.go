package engine

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCMakeStagesTheSidecarsThePinNames runs the real fetch script, the one
// every package is built with (the CMake build, scripts/docker-assemble.sh and
// the Windows release), against a pin whose rows this parser reads too: the
// sidecars must land beside the engine under their published names, and bytes
// that do not match their row must fail the build.
func TestCMakeStagesTheSidecarsThePinNames(t *testing.T) {
	cmake, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("cmake is not installed")
	}
	script, err := filepath.Abs("../../cmake/opencoti-fetch.cmake")
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	}
	engine := write("engine", "the engine")
	codec := write("oc-codec-win-x86_64.dll", "the codec")
	audio := write("oc-audiocpp-win-x86_64.dll", "audio.cpp")
	espeak := write("oc-espeak-win-x86_64.dll", "a kind nobody has listed")
	write("oc-codec-linux-x86_64.so", "another platform's codec")

	pin := sidecarPinHead +
		"# prose, with a #! sidecar mention that is not a row\n" +
		"#! cuda-sass 86 120\n" +
		"bin win-x86_64-gpu builds/v1/engine " + engine + "\n" +
		"#! sidecar  x86_64      codec     builds/v1/oc-codec-linux-x86_64.so   " + strings.Repeat("0", 64) + "\n" +
		"#! sidecar  win-x86_64  codec     builds/v1/oc-codec-win-x86_64.dll    " + codec + "\n" +
		"#! sidecar  win-x86_64  audiocpp  builds/v1/oc-audiocpp-win-x86_64.dll " + audio + "\n" +
		"#! sidecar  win-x86_64  espeak    builds/v1/oc-espeak-win-x86_64.dll   " + espeak + "\n"
	p, err := ParsePin(pin)
	if err != nil {
		t.Fatalf("the Go parser refuses the pin CMake is given: %v", err)
	}
	pinFile := filepath.Join(src, "pin.txt")
	if err := os.WriteFile(pinFile, []byte(pin), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(dest string) (string, error) {
		out, err := exec.Command(cmake, "-DPIN_FILE="+pinFile, "-DARCH=win-x86_64-gpu", "-DDEST_DIR="+dest,
			"-DLOCAL_FILE="+filepath.Join(src, "engine"), "-DLOCAL_SIDECAR_DIR="+src, "-P", script).CombinedOutput()
		return string(out), err
	}

	dest := t.TempDir()
	if out, err := run(dest); err != nil {
		t.Fatalf("cmake: %v\n%s", err, out)
	}
	var staged []string
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		staged = append(staged, e.Name())
	}
	// What Go says the pin carries for this arch is what CMake staged, under
	// the published names, and no other platform's file.
	want := []string{"engine.exe"}
	for _, s := range p.Sidecars("win-x86_64-gpu") {
		want = append(want, filepath.Base(s.Path))
	}
	// Every row of the arch, whatever its kind.
	if len(want) != 4 {
		t.Fatalf("Go reads %v for win-x86_64-gpu", want)
	}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("%s was not staged beside the engine (staged: %v)", name, staged)
		}
	}
	if len(staged) != len(want) {
		t.Errorf("staged %v, want exactly %v", staged, want)
	}

	// The wrong bytes under the right name fail the build.
	if err := os.WriteFile(filepath.Join(src, "oc-audiocpp-win-x86_64.dll"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t.TempDir()); err == nil || !strings.Contains(out, "does not match the audiocpp sidecar pin") {
		t.Fatalf("cmake staged a sidecar that does not match its row: %v\n%s", err, out)
	}
}
