package systel

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeSidecar struct {
	mu      sync.Mutex
	fresh   int // ensure(true): a poll after an idle gap
	again   int // ensure(false): every reading
	stopped int
}

func (f *fakeSidecar) ensure(fresh bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fresh {
		f.fresh++
	} else {
		f.again++
	}
}

func (f *fakeSidecar) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
}

func (f *fakeSidecar) counts() (fresh, again, stopped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fresh, f.again, f.stopped
}

// The sidecar runs only while the snapshot is polled: started by the first
// poll after an idle gap, stopped when the polling stops, started again by the
// next poll. A server nobody watches runs nothing for it.
func TestTheSidecarRunsOnlyWhilePolled(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(0, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	proc := &fakeSidecar{}
	r := &sidecarReader{path: filepath.Join(t.TempDir(), "gpu-metrics.json"), proc: proc}
	s := newSampler(5*time.Millisecond, time.Minute, []reader{r}, clock)
	defer s.Close()

	s.Get()
	if fresh, again, stopped := proc.counts(); fresh != 1 || again < 1 || stopped != 0 {
		t.Fatalf("after the first poll: fresh %d, again %d, stopped %d; want 1, >=1, 0", fresh, again, stopped)
	}
	advance(2 * time.Minute) // past idle
	deadline := time.Now().Add(5 * time.Second)
	for _, _, stopped := proc.counts(); stopped == 0; _, _, stopped = proc.counts() {
		if time.Now().After(deadline) {
			t.Fatal("the sidecar was not stopped when the polling stopped")
		}
		time.Sleep(time.Millisecond)
	}
	s.Get()
	if fresh, _, stopped := proc.counts(); fresh != 2 || stopped != 1 {
		t.Fatalf("after the next poll: fresh %d, stopped %d; want 2, 1", fresh, stopped)
	}
}

// Closing the sampler stops the sidecar too, and only a sampler that was
// running has one to stop.
func TestClosingStopsTheSidecar(t *testing.T) {
	proc := &fakeSidecar{}
	r := &sidecarReader{path: filepath.Join(t.TempDir(), "gpu-metrics.json"), proc: proc}
	s := newSampler(time.Hour, time.Hour, []reader{r}, time.Now)
	s.Close()
	if _, _, stopped := proc.counts(); stopped != 0 {
		t.Fatalf("a sampler never polled stopped the sidecar %d times", stopped)
	}
	s.Get()
	s.Close()
	if _, _, stopped := proc.counts(); stopped != 1 {
		t.Fatalf("stopped %d times, want 1", stopped)
	}
}

// Without a process (a platform that starts none, or a sidecar run by hand),
// the reader reads the file and nothing else.
func TestAReaderWithoutAProcessOnlyReads(t *testing.T) {
	r := &sidecarReader{path: filepath.Join(t.TempDir(), "absent.json")}
	r.reset()
	r.read(&Snapshot{})
	r.stop()
}
