// Package systel reads the machine a server runs on — host, the server's own
// process tree, and every GPU its vendor's driver reports — for
// /api/xollama/system.
//
// Everything is read in process: /proc and /sys, Win32 and D3DKMT, sysctl and
// IOKit, and the vendor's management library (NVML) loaded at run time. No
// tool is ever run, and nothing here starts an engine, a discovery or a probe.
// The one exception is the Windows GPU metrics sidecar xollama ships, run only
// while the snapshot is polled (sidecar_run_windows.go). A value a platform
// cannot read cheaply stays nil; nothing is estimated.
//
// Readings are cached. A Sampler refreshes its snapshot on its own timer only
// while it is being polled, so any number of pollers cost one refresh per
// interval, and a server nobody watches pays nothing.
package systel

import (
	"os"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
)

// Snapshot is one reading of the machine.
type Snapshot struct {
	At      time.Time
	Host    api.SystemHost
	Server  api.SystemServer
	Devices []api.SystemDevice
}

// reader is one source of readings. It keeps whatever it needs between reads
// to turn counters into rates, and forgets it on reset, so a rate never spans
// an idle gap.
type reader interface {
	read(s *Snapshot)
	reset()
}

// stopper is a reader that keeps something running while the snapshot is
// polled (the Windows GPU metrics sidecar) and lets it go when the polling
// stops, so a server nobody watches runs nothing for it.
type stopper interface {
	stop()
}

// Sampler holds the cached snapshot.
type Sampler struct {
	// Interval is how often the snapshot is refreshed while it is polled.
	Interval time.Duration
	// Idle is how long after the last poll the refreshing stops.
	Idle time.Duration

	readers []reader
	now     func() time.Time

	mu       sync.Mutex
	last     Snapshot
	lastPoll time.Time
	running  bool
	// refresh is how the loop learns to stop early, for tests.
	stop chan struct{}
}

// New returns a Sampler over this platform's readers, rooted at this
// process.
func New(interval, idle time.Duration) *Sampler {
	return newSampler(interval, idle, platformReaders(os.Getpid()), time.Now)
}

func newSampler(interval, idle time.Duration, readers []reader, now func() time.Time) *Sampler {
	return &Sampler{Interval: interval, Idle: idle, readers: readers, now: now}
}

// Get returns the latest snapshot. The first call after an idle period reads
// at once, so its caller waits for one reading, and starts the timer; every
// other call returns the cached copy.
func (s *Sampler) Get() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPoll = s.now()
	if !s.running {
		for _, r := range s.readers {
			r.reset()
		}
		s.last = s.readLocked()
		s.running = true
		s.stop = make(chan struct{})
		go s.loop(s.stop)
	}
	return s.last
}

func (s *Sampler) readLocked() Snapshot {
	snap := Snapshot{At: s.now(), Devices: []api.SystemDevice{}}
	for _, r := range s.readers {
		r.read(&snap)
	}
	return snap
}

func (s *Sampler) loop(stop chan struct{}) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		s.mu.Lock()
		if s.now().Sub(s.lastPoll) > s.Idle {
			s.running = false
			s.mu.Unlock()
			s.stopReaders()
			return
		}
		s.mu.Unlock()
		// Read outside the lock, so a slow reading never holds up a poll; one
		// loop is the only writer.
		snap := Snapshot{At: s.now(), Devices: []api.SystemDevice{}}
		for _, r := range s.readers {
			r.read(&snap)
		}
		s.mu.Lock()
		if s.running {
			s.last = snap
		}
		s.mu.Unlock()
	}
}

// Close stops the refreshing.
func (s *Sampler) Close() {
	s.mu.Lock()
	wasRunning := s.running
	if s.running {
		close(s.stop)
		s.running = false
	}
	s.mu.Unlock()
	if wasRunning {
		s.stopReaders()
	}
}

// stopReaders lets go of what the readers keep running. It is called without
// the lock: stopping a process waits for it to exit, and a poll must not.
func (s *Sampler) stopReaders() {
	for _, r := range s.readers {
		if st, ok := r.(stopper); ok {
			st.stop()
		}
	}
}

// pct is a share of a counter's movement between two readings, or nil when
// there is no earlier reading or nothing moved on the denominator.
func pct(prevNum, num, prevDen, den float64, ok bool) *float64 {
	if !ok || den <= prevDen || num < prevNum {
		return nil
	}
	v := 100 * (num - prevNum) / (den - prevDen)
	return &v
}

func ptr[T any](v T) *T { return &v }
