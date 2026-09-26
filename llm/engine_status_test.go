package llm

import (
	"io"
	"testing"
)

const reserveLine = "ggml_backend_sched_alloc_splits: failed to allocate graph, reserving (backend_ids_changed = 1)\n"

// TestAGraphReserveIsNotAnOutOfMemoryOnOpencoti is bug-118: the line made
// every later engine error read as an out-of-memory.
func TestAGraphReserveIsNotAnOutOfMemoryOnOpencoti(t *testing.T) {
	w := NewStatusWriter(io.Discard)
	w.opencoti.Store(true)
	_, _ = w.Write([]byte(reserveLine))
	if msg := w.LastError(); msg != "" {
		t.Errorf("opencoti's graph re-reserve was kept as an error: %q", msg)
	}

	_, _ = w.Write([]byte("CUDA error: out of memory\n"))
	if msg := w.LastError(); !IsOutOfMemoryMessage(msg) {
		t.Errorf("a real out-of-memory must still be kept, got %q", msg)
	}
}

// TestOffMeansOffForTheStatusLines: on llama.cpp the line is classified as
// upstream classifies it.
func TestOffMeansOffForTheStatusLines(t *testing.T) {
	w := NewStatusWriter(io.Discard)
	_, _ = w.Write([]byte(reserveLine))
	if msg := w.LastError(); msg == "" {
		t.Error("on llama.cpp the line must be kept exactly as upstream keeps it")
	}
}
