package server

// xollama: a council turn's pool layers kept across its tool round trips --
// plans/agentic-council-chat.md (11.24). Part of the `council` hook.
//
// A council turn with tools runs as many requests as the client has round
// trips, and each request built its pool tree afresh and released it at its
// end, keeping only the conversation's root. The stage layers the members
// attach to -- the conversation, the plan, the findings -- were prefilled
// again on every trip: on eleven2go (hard, 5ce5f7e7) 68 builds prefilled
// 582509 tokens, 265334 of them for a layer the previous request had just
// released (one 6636-token layer five times in 78 s, 4.7 s each).
//
// Now a request that ends with the members' tool calls puts its layers aside
// for the owner, and the next request of the same turn adopts them. The rules
// are the kept root's (begin): the layers are the owner's, and the engine
// frees them when its allocation ends and may give their ids to another
// conversation, so they are adopted only while the allocation lives, on the
// runner that made them, under the same kept root, and within the same turn;
// anything else releases them, newest first, as does a client that does not
// come back within councilStashIdle.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/llm"
)

// councilStashIdle is how long a suspended turn's layers wait for the
// client's next round trip: tool calls run in seconds, one a person approves
// in minutes.
var councilStashIdle = 10 * time.Minute

// councilStash is a suspended turn's layers, oldest first.
type councilStash struct {
	kv     llm.PolyKV
	turn   string
	root   int // the kept root they stand on
	layers []*councilLayer
	timer  *time.Timer
}

// release lets the layers go, newest first: the engine releases only a pool
// with no child.
func (s *councilStash) release(ctx context.Context) {
	for i := len(s.layers) - 1; i >= 0; i-- {
		if err := s.kv.ReleasePool(ctx, s.layers[i].id); err != nil {
			slog.Debug("council: could not release a kept layer", "pool", s.layers[i].id, "error", err)
		}
	}
}

type stashRegistry struct {
	mu sync.Mutex
	m  map[string]*councilStash
}

var councilStashes = &stashRegistry{m: map[string]*councilStash{}}

// put keeps s for owner, releasing any stash it displaces and, after
// councilStashIdle, s itself if nobody took it.
func (r *stashRegistry) put(owner string, s *councilStash) {
	r.mu.Lock()
	old := r.m[owner]
	r.m[owner] = s
	s.timer = time.AfterFunc(councilStashIdle, func() {
		if r.drop(owner, s) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			slog.Debug("council: kept layers released, no round trip came back", "session", owner, "layers", len(s.layers))
			s.release(ctx)
		}
	})
	r.mu.Unlock()
	if old != nil {
		old.timer.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		old.release(ctx)
	}
}

// take removes owner's stash, if any.
func (r *stashRegistry) take(owner string) *councilStash {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.m[owner]
	if s != nil {
		delete(r.m, owner)
		s.timer.Stop()
	}
	return s
}

// drop removes s if it is still owner's stash.
func (r *stashRegistry) drop(owner string, s *councilStash) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[owner] != s {
		return false
	}
	delete(r.m, owner)
	return true
}

func (r *stashRegistry) reset() {
	r.mu.Lock()
	for _, s := range r.m {
		s.timer.Stop()
	}
	r.m = map[string]*councilStash{}
	r.mu.Unlock()
}

// suspend marks the request as ending with the members' tool calls for turn:
// its layers are kept for the next round trip (release).
func (t *councilTree) suspend(turn string) {
	t.mu.Lock()
	t.stashTurn = turn
	t.mu.Unlock()
}

// stashLocked keeps the layers of a suspended turn instead of releasing
// them, and returns those it did not keep. Only layers standing on a kept
// root: an unowned pool is not the owner's, and a first turn's root goes with
// the turn. Of the layers, only those this request used and the ones they
// stand on are kept: a layer adopted and not asked for again is a stage the
// turn has moved past, and kept it would hold the owner's cells trip after trip.
func (t *councilTree) stashLocked(order []*councilLayer, keep *councilLayer) (rest []*councilLayer) {
	if t.stashTurn == "" || keep == nil || len(order) == 0 {
		return order
	}
	for _, l := range order {
		if l.root || l.err != nil {
			return order
		}
	}
	var layers []*councilLayer
	for _, l := range order {
		if !l.used && !usedChildOf(order, l) {
			rest = append(rest, l)
			continue
		}
		l.users, l.used = 0, false
		layers = append(layers, l)
	}
	if len(layers) == 0 {
		return rest
	}
	councilStashes.put(t.owner, &councilStash{kv: t.kv, turn: t.stashTurn, root: keep.id, layers: layers})
	slog.Debug("council: layers kept for the next round trip", "session", t.owner, "layers", len(layers), "released", len(rest), "root", keep.id)
	return rest
}

// usedChildOf reports whether a layer this request used stands on p.
func usedChildOf(order []*councilLayer, p *councilLayer) bool {
	for _, l := range order {
		if l != p && l.used && len(l.text) > len(p.text) && strings.HasPrefix(l.text, p.text) {
			return true
		}
	}
	return false
}

// takeStash is begin's: the owner's stash, when it stands on the kept root
// begin adopted, on this runner; any other is released.
func (t *councilTree) takeStash(ctx context.Context, root *councilRoot) {
	s := councilStashes.take(t.owner)
	if s == nil {
		return
	}
	if root == nil || s.kv != t.kv || s.root != root.id {
		if s.kv == t.kv {
			s.release(ctx)
		}
		return
	}
	t.mu.Lock()
	t.stashed = s
	t.mu.Unlock()
}

// adopt makes the stash this turn's layers, once the turn is known; the
// layers of another turn are released.
func (t *councilTree) adopt(ctx context.Context, turn string) {
	t.mu.Lock()
	s := t.stashed
	t.stashed = nil
	if s != nil && s.turn == turn {
		for _, l := range s.layers {
			t.layers[layerKey(l.text)] = l
			t.order = append(t.order, l)
		}
		t.adopted = s.layers
	}
	t.mu.Unlock()
	if s == nil {
		return
	}
	if s.turn != turn {
		s.release(ctx)
		return
	}
	slog.Debug("council: layers adopted from the last round trip", "session", t.owner, "layers", len(s.layers))
}

// dropAdopted releases the adopted layers before their root is let go: the
// engine keeps a pool with a child. Called only before any member runs.
func (t *councilTree) dropAdopted(ctx context.Context) {
	t.mu.Lock()
	ls := t.adopted
	t.adopted = nil
	for _, l := range ls {
		l.released = true
		delete(t.layers, layerKey(l.text))
	}
	t.mu.Unlock()
	for i := len(ls) - 1; i >= 0; i-- {
		if err := t.kv.ReleasePool(ctx, ls[i].id); err != nil {
			slog.Debug("council: could not release an adopted layer", "pool", ls[i].id, "error", err)
		}
	}
}
