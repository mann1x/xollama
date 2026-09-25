// Package stub is a model that costs a fixed time per token and nothing else,
// so a benchmark over it measures the orchestration around it.
package stub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"councileval/council"
)

// Model is the stub. The zero value answers instantly with 8 tokens.
type Model struct {
	Tokens     int           // tokens per member reply (default 8)
	TokenBytes int           // bytes per token (default 4)
	Prefill    time.Duration // before the first token
	PerToken   time.Duration // between tokens
	// SlowFirst delays member index 0 of every parallel role, so it finishes
	// last and an orchestration that orders by completion is caught.
	SlowFirst time.Duration
	// Revise makes every critic in round < Revise end with council.Revise.
	Revise int
	// FailRole/FailIndex make that member's call fail with ErrInjected.
	FailRole  council.Role
	FailIndex int

	mu       sync.Mutex
	calls    []council.Request
	roleNow  map[council.Role]int
	rolePeak map[council.Role]int
	inflight atomic.Int64
	peak     atomic.Int64
	aborted  atomic.Int64
}

// ErrInjected is the failure FailRole produces.
var ErrInjected = fmt.Errorf("stub: injected failure")

// Stream implements council.Model.
func (m *Model) Stream(ctx context.Context, req council.Request, onToken func(string)) (string, error) {
	n := m.inflight.Add(1)
	defer m.inflight.Add(-1)
	for {
		p := m.peak.Load()
		if n <= p || m.peak.CompareAndSwap(p, n) {
			break
		}
	}
	m.mu.Lock()
	m.calls = append(m.calls, req)
	if m.roleNow == nil {
		m.roleNow, m.rolePeak = map[council.Role]int{}, map[council.Role]int{}
	}
	m.roleNow[req.Role]++
	m.rolePeak[req.Role] = max(m.rolePeak[req.Role], m.roleNow[req.Role])
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.roleNow[req.Role]--; m.mu.Unlock() }()

	if req.Role == m.FailRole && req.Index == m.FailIndex && m.FailRole != "" && !req.RouteOnly {
		return "", ErrInjected
	}
	pre := m.Prefill
	if req.Index == 0 && (req.Role == council.Researcher || req.Role == council.Critic) {
		pre += m.SlowFirst
	}
	if err := m.sleep(ctx, pre); err != nil {
		return "", err
	}

	switch {
	case req.RouteOnly:
		route := "council"
		if last := lastUser(req.Messages, 2); len(last) < 24 {
			route = "direct"
		}
		return m.emitAll(ctx, []string{`{"route":"` + route + `"}`}, onToken)
	case req.WantBriefs > 0:
		p := council.Plan{Plan: "Split the question."}
		for i := range req.WantBriefs {
			p.Briefs = append(p.Briefs, fmt.Sprintf("angle %d", i+1))
		}
		b, _ := json.Marshal(p)
		return m.emitAll(ctx, []string{string(b)}, onToken)
	}

	toks := max(m.Tokens, 1)
	tb := max(m.TokenBytes, 1)
	tag := fmt.Sprintf("<%s%d.%d>", req.Role, req.Index+1, req.Round)
	pieces := make([]string, 0, toks+1)
	pieces = append(pieces, tag)
	for i := range toks {
		pieces = append(pieces, strings.Repeat(string(rune('a'+i%26)), tb))
	}
	if req.Role == council.Critic && req.Round < m.Revise {
		pieces = append(pieces, " "+council.Revise)
	}
	return m.emitAll(ctx, pieces, onToken)
}

func (m *Model) emitAll(ctx context.Context, pieces []string, onToken func(string)) (string, error) {
	var b strings.Builder
	for i, p := range pieces {
		if i > 0 {
			if err := m.sleep(ctx, m.PerToken); err != nil {
				return "", err
			}
		} else if err := ctx.Err(); err != nil {
			m.aborted.Add(1)
			return "", err
		}
		onToken(p)
		b.WriteString(p)
	}
	return b.String(), nil
}

func (m *Model) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if err := ctx.Err(); err != nil {
			m.aborted.Add(1)
			return err
		}
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		m.aborted.Add(1)
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// lastUser is the n-th user message from the end (1 = last).
func lastUser(msgs []council.Message, n int) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			if n--; n == 0 {
				return msgs[i].Content
			}
		}
	}
	return ""
}

// Calls returns every request made so far.
func (m *Model) Calls() []council.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]council.Request(nil), m.calls...)
}

// Inflight is the number of calls running now; Peak the most at once;
// Aborted how many calls stopped on a done context.
func (m *Model) Inflight() int64 { return m.inflight.Load() }
func (m *Model) Peak() int64     { return m.peak.Load() }
func (m *Model) Aborted() int64  { return m.aborted.Load() }

// PeakOf is the most calls of one role that ran at once.
func (m *Model) PeakOf(r council.Role) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rolePeak[r]
}

// ResetPeak clears the peak between phases.
func (m *Model) ResetPeak() { m.peak.Store(0) }
