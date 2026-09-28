package server

// xollama: how many cloud members run at once -- plans/agentic-council-chat.md
// 11.8. Additive; reached from councilMembers.stream.
//
// A member on a cloud model takes no engine slot on this server: the local
// members follow the engine's parallel slots (llm/engine_council_slots.go),
// and the cloud members are counted apart, council.cloud_parallel at a time
// (3 by default). One count per council model, shared by every turn and every
// background review of it, so two conversations on one council do not double
// what the operator allowed.

import (
	"context"
	"sync"

	"github.com/ollama/ollama/types/xollama"
)

type cloudSlots struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

var councilCloud = &cloudSlots{m: map[string]chan struct{}{}}

// councilCloudParallel is the council's cloud_parallel, or its default.
func councilCloudParallel(m *Model) int {
	if m != nil && m.Xollama != nil && m.Xollama.Council != nil && m.Xollama.Council.CloudParallel > 0 {
		return m.Xollama.Council.CloudParallel
	}
	return xollama.DefaultCouncilCloudParallel
}

// slots is the council model's count, made at its size the first time and
// remade when the size changes (a tweak between turns).
func (c *cloudSlots) slots(name string, n int) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.m[name]; ok && cap(s) == n {
		return s
	}
	s := make(chan struct{}, n)
	c.m[name] = s
	return s
}

// acquire takes one of the council's cloud slots, or returns ctx's error.
func acquireCloud(ctx context.Context, s chan struct{}) (func(), error) {
	select {
	case s <- struct{}{}:
		return func() { <-s }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// takeCloud takes a cloud slot for a member on model when model is a cloud
// model -- it takes no engine slot, so it waits for one of the council's cloud
// slots instead -- and nothing for a local one.
func (cm *councilMembers) takeCloud(ctx context.Context, model string) (func(), error) {
	if cm.cloud == nil || !councilModelIsCloud(model) {
		return func() {}, nil
	}
	return acquireCloud(ctx, cm.cloud)
}
