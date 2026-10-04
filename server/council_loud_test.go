package server

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a bytes.Buffer the council's goroutines may log into at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureWarnings routes the default logger to a buffer that keeps Warn and
// above, so a test can tell a fault that was named from one left at Debug.
func captureWarnings(t *testing.T) *syncBuffer {
	t.Helper()
	var buf syncBuffer
	restore := slog.Default()
	t.Cleanup(func() { slog.SetDefault(restore) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return &buf
}

// TestARootNothingRelievedIsNamed: the engine refuses the conversation's root
// and the conversation does not fold. The turn still runs, every member on its
// own copy, and that is said at Warn with the refusal, never swallowed.
func TestARootNothingRelievedIsNamed(t *testing.T) {
	councilRoots.reset()
	councilCompactions.reset()
	logs := captureWarnings(t)
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1", full: 1}
	s := polykvCouncil(t, e, kv, councilOn())
	req := polykvReq
	req.SessionID = "conv-1"
	chatChunks(t, s, req)
	councilIdle.Wait()

	out := logs.String()
	if !strings.Contains(out, "no conversation root for this turn") {
		t.Fatalf("a refused root nothing relieved must be named at Warn, got %q", out)
	}
	if !strings.Contains(out, "compact the session") || !strings.Contains(out, "did not fold") {
		t.Errorf("the warning must carry the engine's refusal and that the fold did not help, got %q", out)
	}
}

// TestARootRefusedAgainAfterTheFoldIsNamed is the line that was `_ =`: the
// conversation folds, the engine refuses the rebuilt root as well, and that
// second refusal is said, not dropped.
func TestARootRefusedAgainAfterTheFoldIsNamed(t *testing.T) {
	councilRoots.reset()
	councilCompactions.reset()
	councilStateKeyIn(t, t.TempDir())
	logs := captureWarnings(t)
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1", full: 2}
	s := polykvCouncil(t, e, kv, councilOn())
	req := longCouncilReq("conv-1", "Why is the sky blue?")
	chatChunks(t, s, req)
	councilIdle.Wait()

	if councilCompactions.get("conv-1") == nil {
		t.Fatal("the refused root must have folded the conversation first")
	}
	out := logs.String()
	if !strings.Contains(out, "no conversation root for this turn") || !strings.Contains(out, "compact the session") {
		t.Fatalf("the rebuilt root's refusal must be named at Warn, got %q", out)
	}
	if strings.Contains(out, "did not fold") {
		t.Errorf("this conversation folded; the warning must not say it did not, got %q", out)
	}
}

// TestAPoolTheEngineKeptIsNamed: a release the engine refuses leaves cells
// booked in the owner (bug-118); it is said at Warn with the pool.
func TestAPoolTheEngineKeptIsNamed(t *testing.T) {
	logs := captureWarnings(t)
	unreleased("a finished layer", 7, errors.New("pool 7 has a child"))
	out := logs.String()
	for _, want := range []string{"level=WARN", "could not release a finished layer", "pool=7", "pool 7 has a child"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}
