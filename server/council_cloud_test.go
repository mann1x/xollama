package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/types/xollama"
)

// A council's local width counts only the members this engine serves: a role
// on another server or on a cloud model takes no slot here, and the critics
// reviewing the synthesizer's checks run beside it.
func TestACouncilsWidthCountsOnlyLocalMembers(t *testing.T) {
	defer func(f func(string) bool) { councilModelIsCloud = f }(councilModelIsCloud)
	councilModelIsCloud = func(name string) bool { return name == "big:cloud" }
	yes := true
	for name, tc := range map[string]struct {
		c    *xollama.Council
		want int
	}{
		"not a council":               {nil, 0},
		"defaults: 2 researchers":     {&xollama.Council{Enabled: &yes}, 2},
		"3 critics beside the synth":  {&xollama.Council{Enabled: &yes, Critic: &xollama.CouncilRole{Count: 3}}, 4},
		"researchers on the cloud":    {&xollama.Council{Enabled: &yes, Researcher: &xollama.CouncilRole{Count: 5, Model: "big:cloud"}}, 2},
		"researchers on another host": {&xollama.Council{Enabled: &yes, Researcher: &xollama.CouncilRole{Count: 5, Host: "http://gpu2:11434"}}, 2},
		"everyone on the cloud": {&xollama.Council{
			Enabled:    &yes,
			Researcher: &xollama.CouncilRole{Count: 5, Model: "big:cloud"},
			Critic:     &xollama.CouncilRole{Count: 3, Model: "big:cloud"},
		}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := councilSlots(&Model{Xollama: &xollama.Config{Version: 4, Council: tc.c}}); got != tc.want {
				t.Errorf("width %d, want %d", got, tc.want)
			}
		})
	}
}

// Cloud members run council.cloud_parallel at a time, 3 by default.
func TestCloudMembersRunCloudParallelAtATime(t *testing.T) {
	yes := true
	if n := councilCloudParallel(&Model{Xollama: &xollama.Config{Council: &xollama.Council{Enabled: &yes}}}); n != 3 {
		t.Fatalf("default %d", n)
	}
	if n := councilCloudParallel(&Model{Xollama: &xollama.Config{Council: &xollama.Council{Enabled: &yes, CloudParallel: 2}}}); n != 2 {
		t.Fatalf("stated %d", n)
	}
	s := (&cloudSlots{m: map[string]chan struct{}{}}).slots("c", 2)
	var running, most atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := acquireCloud(t.Context(), s)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			n := running.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
		}()
	}
	wg.Wait()
	if most.Load() != 2 {
		t.Errorf("%d ran at once, want 2", most.Load())
	}
	// A member whose client left stops waiting.
	full := make(chan struct{}, 1)
	full <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := acquireCloud(ctx, full); err == nil {
		t.Error("a cancelled member took a slot")
	}
}

// Only a member on a cloud model takes a cloud slot.
func TestOnlyCloudMembersTakeACloudSlot(t *testing.T) {
	defer func(f func(string) bool) { councilModelIsCloud = f }(councilModelIsCloud)
	councilModelIsCloud = func(name string) bool { return name == "big:cloud" }
	cm := &councilMembers{cloud: make(chan struct{}, 1)}
	release, err := cm.takeCloud(t.Context(), "local:8b")
	if err != nil || len(cm.cloud) != 0 {
		t.Fatalf("a local member took a cloud slot: %v", err)
	}
	release()
	release, err = cm.takeCloud(t.Context(), "big:cloud")
	if err != nil || len(cm.cloud) != 1 {
		t.Fatalf("a cloud member took no cloud slot: %v", err)
	}
	release()
	if len(cm.cloud) != 0 {
		t.Error("the slot was not given back")
	}
}
