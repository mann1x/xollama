package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	name, args, _ := Launch("/usr/local/lib/ollama/llama-server", params, []Device{ada()}, t.TempDir())

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
	name, args, _ := Launch("/stock/llama-server", params, []Device{ada()}, dir)

	if name != "/stock/llama-server" {
		t.Errorf("name = %q, want the stock binary", name)
	}
	if !slices.Equal(args, params) {
		t.Errorf("args = %v, want ollama's argv untouched %v", args, params)
	}
}

// TestCommandRaisesVerbosityForTheMemoryScrapers guards a flag that looks like
// noise and is not. llamafile filters LLAMA_LOG_INFO at default verbosity, and
// the "<component>: <device> <kind> buffer size = N MiB" lines ollama's
// memoryParsingWriter scrapes are INFO. Without the threshold the engine boots
// perfectly and reports no memory at all, so the scheduler plans against zero.
//
// Measured with the argv this builds: no flag -> 0 buffer lines; threshold 5 ->
// 20. See the logVerbosity comment for why 4 is not enough.
func TestCommandRaisesVerbosityForTheMemoryScrapers(t *testing.T) {
	_, args := Command("/e/oc.llamafile", []string{"--model", "m"}, []Device{ada()}, "linux")

	i := slices.Index(args, "--log-verbosity")
	if i < 0 {
		t.Fatalf("argv has no --log-verbosity; ollama would scrape no memory at all: %v", args)
	}
	if i+1 >= len(args) || args[i+1] != logVerbosity {
		t.Fatalf("--log-verbosity value = %v, want %q", args[i+1:], logVerbosity)
	}
	// It must come *after* ollama's argv. ollama passes --log-verbosity 4 of
	// its own and llama.cpp takes the last occurrence, so a leading flag is
	// inert -- see TestCommandLogVerbosityWinsOverOllamas.
	if j := slices.Index(args, "--model"); j > i {
		t.Errorf("--log-verbosity at %d precedes ollama's argv at %d, so ollama's value would win", i, j)
	}
}

// Regression: ollama appends --log-verbosity 4 of its own, and llama.cpp's
// parser takes the last occurrence. Prepending ours left it inert, which is
// the bug-007 failure returning by a side door: the scheduler plans against
// buffer-size lines that verbosity 4 filters out.
func TestCommandLogVerbosityWinsOverOllamas(t *testing.T) {
	stock := []string{
		"--model", "/blobs/sha256-abc",
		"--port", "5991",
		"--log-verbosity", "4",
		"--no-log-prefix",
	}

	_, args := Command("/opt/engine.llamafile", stock, []Device{{Backend: BackendCUDA}}, "linux")

	var seen []int
	for i, a := range args {
		if a == "--log-verbosity" {
			if i+1 >= len(args) {
				t.Fatalf("--log-verbosity has no value: %v", args)
			}
			seen = append(seen, i)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("argv carries %d --log-verbosity flags, want exactly 1: %v", len(seen), args)
	}
	if got := args[seen[0]+1]; got != logVerbosity {
		t.Errorf("--log-verbosity = %q, want %q", got, logVerbosity)
	}
	// Everything else ollama asked for must survive.
	for _, want := range []string{"--model", "/blobs/sha256-abc", "--port", "5991", "--no-log-prefix"} {
		if !slices.Contains(args, want) {
			t.Errorf("argv dropped %q: %v", want, args)
		}
	}
}

func TestCommandHandlesJoinedLogVerbosity(t *testing.T) {
	_, args := Command("/opt/e.llamafile", []string{"--log-verbosity=4", "--model", "m"}, nil, "linux")
	for _, a := range args {
		if strings.HasPrefix(a, "--log-verbosity=") {
			t.Fatalf("joined form survived: %v", args)
		}
	}
	if !slices.Contains(args, logVerbosity) {
		t.Errorf("our verbosity missing: %v", args)
	}
}

func TestFallbackOnLoadFailureIsOffByDefault(t *testing.T) {
	t.Setenv(EnvFallback, "")
	if FallbackOnLoadFailure() {
		t.Error("fallback is on by default; a failed opencoti load would silently downgrade to llama.cpp")
	}
}

func TestFallbackOnLoadFailureOptIn(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(EnvFallback, v)
			if !FallbackOnLoadFailure() {
				t.Errorf("%s=%q did not enable the fallback", EnvFallback, v)
			}
		})
	}
}

func TestLaunchReportsWhichEngineItChose(t *testing.T) {
	t.Setenv(EnvSelector, "llamacpp")
	if _, _, used := Launch("/stock/llama-server", []string{"--model", "m"}, []Device{ada()}, t.TempDir()); used {
		t.Error("reported opencoti while forced to llama.cpp")
	}

	// Forced to opencoti but with no artifact anywhere: Launch still falls back
	// before spawning, and must not claim opencoti -- otherwise the opt-in
	// retry would restart a load that was already on stock.
	t.Setenv(EnvSelector, "opencoti")
	t.Setenv(EnvPath, "")
	if _, _, used := Launch("/stock/llama-server", []string{"--model", "m"}, []Device{ada()}, t.TempDir()); used {
		t.Error("reported opencoti when no artifact was found")
	}
}

// TestCommandTranslatesLoadMode covers the flag that made this engine unable to
// load anything at all. ollama disables mmap by default for a llama-server
// load, so "--load-mode none" is on essentially every argv; the engine's
// llama.cpp predates that flag and refused the whole command line with
// "error: invalid argument: --load-mode" (measured, opencoti-llamafile-0.10.5-c7
// on windows/amd64).
func TestCommandTranslatesLoadMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params []string
		want   []string
		absent []string
	}{
		{
			name:   "none becomes --no-mmap",
			params: []string{"--model", "m.gguf", "--load-mode", "none"},
			want:   []string{"--no-mmap"},
			absent: []string{"--load-mode", "none"},
		},
		{
			name:   "the joined spelling too",
			params: []string{"--model", "m.gguf", "--load-mode=none"},
			want:   []string{"--no-mmap"},
			absent: []string{"--load-mode=none"},
		},
		{
			// dio bypasses the page cache for an integrated CUDA/ROCm GPU on
			// Linux. This engine has no equivalent and is never routed to on
			// that hardware, so it goes rather than becoming something it is not.
			name:   "dio is dropped, not guessed at",
			params: []string{"--model", "m.gguf", "--load-mode", "dio"},
			absent: []string{"--load-mode", "dio", "--no-mmap"},
		},
		{
			name:   "an argv without it is untouched",
			params: []string{"--model", "m.gguf", "--flash-attn", "on"},
			want:   []string{"--model", "m.gguf", "--flash-attn", "on"},
			absent: []string{"--no-mmap"},
		},
		{
			// A truncated argv must not make the value be read as a flag.
			name:   "a trailing --load-mode takes nothing with it",
			params: []string{"--model", "m.gguf", "--load-mode"},
			want:   []string{"--model", "m.gguf"},
			absent: []string{"--load-mode", "--no-mmap"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, args := Command("/engines/a.llamafile", tc.params, nil, "windows")
			for _, w := range tc.want {
				if !slices.Contains(args, w) {
					t.Errorf("argv is missing %q: %v", w, args)
				}
			}
			for _, a := range tc.absent {
				if slices.Contains(args, a) {
					t.Errorf("argv still carries %q: %v", a, args)
				}
			}
		})
	}
}
