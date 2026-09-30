package server

// xollama: a council member refused because its owner's window is full --
// part of the `council` hook.
//
// On eleven2go (a0968aea, hard, council run 4) the conversation's root grew
// to about 120k tokens and the synthesizer's stage to 39k over 118 trips,
// inside a 196608-cell owner. The next worker needed 39662 cells with 37121
// free. The engine said "compact the session"; the member waited out
// admission for two minutes as a busy server is waited out, nothing ran to
// give cells back, and the turn failed. Compaction had not fired: the owner
// stood at 81%, under its 85% trigger.
//
// Now a member holding the owner's cells is refused at once (ErrOwnerFull).
// While another such member runs it waits for one to finish, which gives
// cells back, and asks again. When none runs, waiting cures nothing: the turn
// folds the conversation, rebuilds its root from the fold, and resumes from
// the members that had settled (councilChat).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/llm"
)

// ownerFullWaits bounds how many times a refused member waits for another to
// finish before its refusal stands.
const ownerFullWaits = 30

// ownerFullError is a member's error that is the owner's full window.
type ownerFullError struct{ msg string }

func (e ownerFullError) Error() string { return e.msg }
func (e ownerFullError) Unwrap() error { return llm.ErrOwnerFull }

// memberError is the error a member's stream ended with.
func memberError(role council.Role, msg string) error {
	m := fmt.Sprintf("council %s: %s", role, msg)
	if strings.Contains(msg, llm.ErrOwnerFull.Error()) {
		return ownerFullError{m}
	}
	return errors.New(m)
}

// ownerBound reports whether a member's request holds the owner's cells: it
// is attached to one of the owner's pools, or runs on the owner itself. The
// compaction's own calls are left to wait: they run when the turn has let its
// layers go.
func (cm *councilMembers) ownerBound(r council.Request, session, worker string) bool {
	t := cm.tree
	if t == nil || t.unowned || strings.HasPrefix(string(r.Role), "compaction") {
		return false
	}
	return worker != "" || session == t.owner
}

// takeoff and land count the owner-bound members in flight; landed is
// closed, and replaced, each time one lands.
func (t *councilTree) takeoff() {
	t.mu.Lock()
	t.flying++
	t.mu.Unlock()
}

func (t *councilTree) land() {
	t.mu.Lock()
	t.flying--
	t.landings++
	if t.landed != nil {
		close(t.landed)
	}
	t.landed = make(chan struct{})
	t.mu.Unlock()
}

// landingCount is how many owner-bound members have landed so far.
func (t *councilTree) landingCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.landings
}

// waitLanding waits for an owner-bound member to finish. It reports false at
// once when none is in flight, and when ctx ends.
func (t *councilTree) waitLanding(ctx context.Context) bool {
	t.mu.Lock()
	if t.flying == 0 {
		t.mu.Unlock()
		return false
	}
	if t.landed == nil {
		t.landed = make(chan struct{})
	}
	ch := t.landed
	t.mu.Unlock()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

// retryOwnerFull runs call again while the owner is full and another member
// may give cells back: one landed since call began, or one is in flight.
func (cm *councilMembers) retryOwnerFull(ctx context.Context, r council.Request, call func() (string, []api.ToolCall, bool, error)) (string, []api.ToolCall, bool, error) {
	for waits := 0; ; waits++ {
		var began int
		if cm.tree != nil {
			began = cm.tree.landingCount()
		}
		out, calls, cut, err := call()
		if err == nil || cm.tree == nil || !errors.Is(err, llm.ErrOwnerFull) || waits >= ownerFullWaits {
			return out, calls, cut, err
		}
		// The refused call was owner-bound (only such a call is refused this
		// way) and has landed itself: one landing is its own.
		if cm.tree.landingCount() <= began+1 && !cm.tree.waitLanding(ctx) {
			return out, calls, cut, err // nothing will give cells back: the turn compacts
		}
		slog.Debug("council: owner full; a member finished, asking again", "role", r.Role, "index", r.Index, "wait", waits+1)
	}
}

// dropForCompaction lets every pool of the turn go, newest first, the root
// and the roots it was forked from last: the fold that follows reads the
// conversation on the owner, and the rebuilt root replaces them. Called only
// when no owner-bound member is in flight.
func (t *councilTree) dropForCompaction(ctx context.Context) {
	t.mu.Lock()
	var ls []*councilLayer
	for _, l := range t.order {
		if !l.released && l.err == nil {
			l.released = true
			ls = append(ls, l)
		}
	}
	t.order, t.layers, t.root, t.kept, t.adopted, t.workers = nil, map[string]*councilLayer{}, nil, nil, nil, nil
	t.mu.Unlock()
	for i := len(ls) - 1; i >= 0; i-- {
		if err := t.kv.ReleasePool(ctx, ls[i].id); err != nil {
			slog.Debug("council: could not release a pool before compacting", "pool", ls[i].id, "error", err)
		}
		for j := len(ls[i].chain) - 1; j >= 0; j-- {
			if err := t.kv.ReleasePool(ctx, ls[i].chain[j]); err != nil {
				slog.Debug("council: could not release an older root before compacting", "pool", ls[i].chain[j], "error", err)
			}
		}
	}
	slog.Info("council: owner full; the turn's pools let go to compact", "session", t.owner, "pools", len(ls))
}
