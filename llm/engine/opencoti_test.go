package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func write(t *testing.T, dir, name string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindPrefersAnExplicitPath(t *testing.T) {
	dir := t.TempDir()
	explicit := write(t, dir, "somewhere-else.llamafile", time.Now())
	// An artifact that discovery would otherwise pick.
	write(t, dir, "opencoti-llamafile-0.10.5-c7-x86_64.llamafile", time.Now())

	got, err := Find(explicit, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Errorf("Find = %q, want the explicit path %q", got, explicit)
	}
}

func TestFindFailsLoudlyOnABadExplicitPath(t *testing.T) {
	dir := t.TempDir()
	// Discovery would succeed here — an explicit path must not fall through to
	// it, or you debug the wrong binary.
	write(t, dir, "opencoti-llamafile-0.10.5-c7-x86_64.llamafile", time.Now())

	if _, err := Find(filepath.Join(dir, "nope.llamafile"), []string{dir}); err == nil {
		t.Fatal("Find succeeded on a missing explicit path, want an error")
	}
}

func TestFindTakesTheNewestAcrossDirs(t *testing.T) {
	older, newer := t.TempDir(), t.TempDir()
	write(t, older, "opencoti-llamafile-0.10.3-c6-x86_64.llamafile", time.Now().Add(-48*time.Hour))
	want := write(t, newer, "opencoti-llamafile-0.10.5-c7-x86_64.llamafile", time.Now())

	got, err := Find("", []string{older, newer})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Find = %q, want the newest artifact %q", got, want)
	}
}

func TestFindIgnoresWhatIsNotAnArtifact(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "llama-server", time.Now())
	write(t, dir, "opencoti-llamafile-0.10.5-c7-x86_64.llamafile.MANIFEST.json", time.Now())
	write(t, dir, "README.md", time.Now())

	_, err := Find("", []string{dir})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Find error = %v, want ErrNotFound", err)
	}
}

func TestFindSurvivesAMissingDir(t *testing.T) {
	dir := t.TempDir()
	want := write(t, dir, "opencoti-llamafile-0.10.5-c7-x86_64.llamafile", time.Now())

	got, err := Find("", []string{filepath.Join(dir, "does-not-exist"), dir})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Find = %q, want %q", got, want)
	}
}

func TestCommandPassesOllamaArgvThroughUntouched(t *testing.T) {
	params := []string{"--model", "/blobs/sha256-abc", "--port", "1234", "--no-webui", "-c", "32768"}
	_, args := Command("/e/oc.llamafile", params, []Device{ada()}, "linux")

	// Everything ollama built must still be there, in order.
	for _, p := range params {
		if !slices.Contains(args, p) {
			t.Errorf("Command dropped %q from ollama's argv: %v", p, args)
		}
	}
	if i, j := slices.Index(args, "--model"), slices.Index(args, "--port"); i > j {
		t.Errorf("Command reordered ollama's argv: %v", args)
	}
}

func TestCommandAddsServerAndLaunchesThroughSh(t *testing.T) {
	name, args := Command("/e/oc.llamafile", []string{"--model", "m"}, []Device{ada()}, "linux")
	if name != "sh" {
		t.Errorf("name = %q, want sh — the artifact is an APE and may not be directly executable", name)
	}
	if len(args) == 0 || args[0] != "/e/oc.llamafile" {
		t.Fatalf("args = %v, want the artifact first", args)
	}
	if args[1] != "--server" {
		t.Errorf("args[1] = %q, want --server — without it the artifact is a chat CLI", args[1])
	}
}

func TestCommandRunsTheArtifactDirectlyOnWindows(t *testing.T) {
	name, args := Command(`C:\e\oc.exe`, []string{"--model", "m"}, []Device{ada()}, "windows")
	if name != `C:\e\oc.exe` {
		t.Errorf("name = %q, want the artifact itself", name)
	}
	if args[0] != "--server" {
		t.Errorf("args[0] = %q, want --server", args[0])
	}
}

func TestGpuFlag(t *testing.T) {
	cases := []struct {
		name    string
		devices []Device
		want    string
	}{
		{"cuda", []Device{ada()}, "nvidia"},
		{"vulkan", []Device{{Backend: BackendVulkan}}, "vulkan"},
		{"cpu only", []Device{{Backend: BackendCPU}}, "disable"},
		{"no devices is cpu only", nil, "disable"},
		{"cuda wins over a cpu entry", []Device{{Backend: BackendCPU}, ada()}, "nvidia"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := gpuFlag(tt.devices); got != tt.want {
				t.Errorf("gpuFlag(%v) = %q, want %q", tt.devices, got, tt.want)
			}
		})
	}
}

func TestLaunchFallsBackToStockWhenNothingIsInstalled(t *testing.T) {
	t.Setenv(EnvSelector, "opencoti")
	t.Setenv(EnvPath, "")

	params := []string{"--model", "m"}
	name, args := Launch("/usr/local/lib/ollama/llama-server", params, []Device{ada()}, t.TempDir())

	if name != "/usr/local/lib/ollama/llama-server" {
		t.Errorf("name = %q, want the stock binary — a missing artifact must not fail the load", name)
	}
	if !slices.Equal(args, params) {
		t.Errorf("args = %v, want ollama's argv untouched %v", args, params)
	}
}

func TestLaunchIsByteIdenticalToUpstreamWhenOff(t *testing.T) {
	dir := t.TempDir()
	// An artifact is installed and would otherwise be chosen.
	write(t, dir, "opencoti-llamafile-0.10.5-c7-x86_64.llamafile", time.Now())
	t.Setenv(EnvSelector, "llamacpp")
	t.Setenv(EnvPath, "")

	params := []string{"--model", "m", "--port", "1"}
	name, args := Launch("/stock/llama-server", params, []Device{ada()}, dir)

	if name != "/stock/llama-server" {
		t.Errorf("name = %q, want the stock binary", name)
	}
	if !slices.Equal(args, params) {
		t.Errorf("args = %v, want ollama's argv untouched %v", args, params)
	}
}
