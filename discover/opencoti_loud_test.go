package discover

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/llm/engine"
)

// captureWarnings routes the default logger to a buffer that keeps Warn and
// above, so a test can tell a fault that was named from one left at Debug.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := slog.Default()
	t.Cleanup(func() { slog.SetDefault(restore) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return &buf
}

// TestAFailedDeviceListingIsNamed: an engine that cannot list its devices (a
// missing dlopen helper, say) leaves placement on llama.cpp's view. That is
// said at Warn, with the engine's own last words, not left at Debug.
func TestAFailedDeviceListingIsNamed(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withOpencoti(t, nil)
	opencotiListDevices = func(context.Context, string, engine.Backend) (string, error) {
		return "", errors.New("exit status 1; engine output: dlopen() isn't supported")
	}
	logs := captureWarnings(t)

	overlayOpencotiDevices(context.Background(), solidPCLlamaCppDevices())

	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "could not list its devices") {
		t.Fatalf("a failed listing must be logged at Warn, got %q", out)
	}
	if !strings.Contains(out, "dlopen() isn't supported") {
		t.Errorf("the warning must carry the engine's reason, got %q", out)
	}
}

// TestARefreshCooldownIsNamed: a refresh that found nothing leaves placement on
// stale free memory for the whole cooldown; that is said once, at Warn.
func TestARefreshCooldownIsNamed(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withRefreshClock(t, time.Unix(0, 0))
	withOpencoti(t, nil)
	opencotiListDevices = func(context.Context, string, engine.Backend) (string, error) {
		return "", context.DeadlineExceeded
	}
	logs := captureWarnings(t)

	for range 3 {
		devices := refreshDevices()
		forkRefresh(context.Background(), devices, make([]bool, len(devices)))
	}

	out := logs.String()
	for _, b := range []string{"backend=CUDA", "backend=Vulkan"} {
		lines := 0
		for l := range strings.SplitSeq(out, "\n") {
			if strings.Contains(l, "free memory stays stale") && strings.Contains(l, b) {
				lines++
			}
		}
		if lines != 1 {
			t.Fatalf("the cooldown must be named once per entry, got %d lines for %s: %q", lines, b, out)
		}
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, context.DeadlineExceeded.Error()) {
		t.Errorf("the warning must be at Warn and carry the reason, got %q", out)
	}
}

func TestOutputTailKeepsTheEngineLastWords(t *testing.T) {
	out := "line 1\n\nline 2\nline 3\nline 4\nline 5\nline 6\n  error: no GPU library  \n"
	if got, want := outputTail(out), "line 3 | line 4 | line 5 | line 6 | error: no GPU library"; got != want {
		t.Errorf("outputTail = %q, want %q", got, want)
	}
	if got := outputTail(" \n"); got != "(none)" {
		t.Errorf("outputTail of nothing = %q, want (none)", got)
	}
	if got := outputTail(strings.Repeat("x", 2000)); len(got) != 515 || !strings.HasPrefix(got, "...") {
		t.Errorf("outputTail must cap a long line at 512 bytes plus an ellipsis, got %d bytes", len(got))
	}
}
