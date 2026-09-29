package server

// xollama: room for a member booked beside the owner -- plans/agentic-council-chat.md
// (11.18). Reached from councilMembers.place, part of the `council` hook.

import (
	"context"
	"log/slog"
)

// roomFor makes room for a member booked on a session of its own beside the
// owner (the builder, a reviewer): need is its window. The owner books the
// whole window by default and grows back to it before every turn (begin), so
// such a member waited out admission, two minutes, and the turn failed
// (eleven2go, 2026-09-29: "base 0/196608 free need 5632"). The owner gives
// back what it is not using, down to its used cells plus the turn's reserve:
// at once when the engine allows it, else deferred to the owner's next idle
// moment, and the member's admission wait seats it. The next turn's begin grows the owner
// back when nobody is refused.
func (t *councilTree) roomFor(ctx context.Context, need int) {
	if t == nil || t.unowned || t.kv == nil || need <= 0 {
		return
	}
	t.roomMu.Lock()
	defer t.roomMu.Unlock()
	k, err := t.kv.KV(ctx)
	if err != nil || k.LargestAdmissible == nil {
		return
	}
	free := *k.LargestAdmissible
	a, ok := k.Session(t.owner)
	if !ok || free >= need {
		return
	}
	window := a.Window
	if a.ResizePending > 0 && a.ResizePending < window {
		// A shrink already queued frees its cells once it lands.
		free += window - a.ResizePending
		window = a.ResizePending
		if free >= need {
			return
		}
	}
	target := (window - (need - free)) / 256 * 256
	if target < roundUp(a.Used+t.reserve, 256) {
		slog.Info("council: the owner has no room to give a member", "session", t.owner, "need", need, "free", free, "window", window, "used", a.Used)
		return
	}
	// At once where the engine allows it: a reviewer is booked while the
	// synthesizer works on the owner's pools, and a deferred shrink would
	// land only after it.
	r, err := t.kv.Resize(ctx, t.owner, target, false)
	if err == nil && r.Applied == 0 && !r.Queued {
		r, err = t.kv.Resize(ctx, t.owner, target, true)
	}
	if err != nil || (r.Applied == 0 && !r.Queued) {
		slog.Info("council: the owner could not make room for a member", "session", t.owner, "need", need, "target", target, "refusal", r.Refusal, "error", err)
		return
	}
	t.mu.Lock()
	t.grant = target
	t.mu.Unlock()
	slog.Info("council: owner window shrunk to make room for a member", "session", t.owner, "from", window, "to", target, "need", need, "queued", r.Queued)
}
