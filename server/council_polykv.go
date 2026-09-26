package server

// xollama: a council turn on PolyKV -- plans/agentic-council-chat.md ("PolyKV
// layout for this flow", "Context, pressure and compaction"). Additive; reached
// from server/council.go when the runner can carry a pool tree.
//
// The planner runs on the council's OWNER session, which books the window,
// attached to the conversation's ROOT pool (guide §6.2, arm C), so the
// conversation is held once: in the root, charged to the owner, and never
// again in the planner's own cells. Every other member is a WORKER: it
// attaches to a pool holding everything it shares with the others and
// prefills only its own instruction, charged to the owner. The tree is not
// spelled out here; it falls out of the members' messages. A worker's layer is
// its conversation up to the opener of its last turn, and each layer forks the
// longest layer already built that it extends:
//
//	P1  the conversation (the root)       the planner
//	 └ P2r  + the plan                    researchers
//	    └ P2f  + the findings             critics
//	       └ P3s  + the critiques         synthesizer
//
// The layers live for one turn. The owner stays open between turns and keeps
// its root, which the idle council summarises on and the next turn forks, so
// only what is new is prefilled. The first turn's root has no owner yet (the
// owner books its window on the planner's first call): it needs
// pool_unowned_v1, and goes with the turn. The owner's booking follows the
// engine's pressure: shrunk while others are refused, grown back when they
// are not.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// councilSentinel stands in for the next turn when a layer is rendered, so the
// layer ends exactly where the shared template stops: at the opener of the
// turn that differs.
const councilSentinel = "\u2063COUNCIL-SENTINEL\u2063"

// The owner pressures at which the conversation is compacted: before a turn
// (Cerebriline's threshold, guide §6.5), and after an answer while the
// council waits for the next message.
const (
	defaultCompactAt     = xollama.DefaultCouncilCompactAt
	defaultIdleCompactAt = xollama.DefaultCouncilIdleCompactAt
)

// councilReserve is the room a turn needs on top of the conversation, in
// tokens, for the plan and the members' replies to live in the owner's window.
func councilReserve(cfg council.Config) int {
	n := cfg.MaxTokens[council.Planner] + cfg.MaxTokens[council.Synthesizer]
	n += cfg.Researchers*cfg.MaxTokens[council.Researcher] + cfg.Critics*cfg.MaxTokens[council.Critic]
	return n + 1024 // the role instructions and the templates around them
}

// councilTree places one council turn's calls on the engine.
type councilTree struct {
	kv       llm.PolyKV
	render   func(ctx context.Context, msgs []api.Message) (string, error)
	tokenize func(ctx context.Context, s string) ([]int, error)
	owner    string
	// window and floor are the ask; grant is what the engine gave, once known.
	window, floor int
	compactAt     float64
	idleCompactAt float64
	// unowned is the whole-pool council (num_ctx 0, pool_unowned_v1): the
	// planner books no window, and the layers belong to no session.
	unowned bool
	// canUnown is pool_unowned_v1: a pool may belong to no session. Without
	// it the first turn, whose owner holds no allocation yet, builds no root.
	canUnown bool
	// reserve is the turn's room on top of the conversation (councilReserve).
	reserve int
	// numCtx is the conversation's num_ctx, the loaded context for num_ctx 0.
	numCtx int
	// clientPool is the pool the client named (client_placement_v1). The
	// conversation's root forks from it when the council's prompt starts
	// with the pool's tokens; it is never released here.
	clientPool *int
	// refusing is set when /kv reports the engine refusing others.
	refusing bool

	// root is this turn's conversation pool, which the planner runs attached
	// to, so the conversation is held once: in the pool, never again in the
	// planner's own session. kept is the previous turn's root, still owned by
	// the owner's allocation, which this turn extends instead of prefilling
	// the conversation again.
	root *councilLayer
	kept *councilRoot
	// promote is the text of a first turn's unowned root, which the idle
	// council builds again for the owner (promoteRoot).
	promote  string
	promoted func() // ends the promotion's mark in councilRoots

	mu     sync.Mutex
	grant  int
	layers map[string]*councilLayer
	order  []*councilLayer // creation order, for release and for parents
	// recurrent is set when the engine keeps a recurrent state per sequence
	// (/kv reports an rs block). Every pool then holds one of a handful of
	// state cells, so a stage's layer is released once its members are done.
	recurrent bool
	workers   map[string]*councilLayer // worker session -> the layer it attached to
}

type councilLayer struct {
	text  string
	ready chan struct{}
	id    int
	err   error
	// Guarded by the tree's mu.
	users    int  // workers attached and not yet closed
	root     bool // the conversation's own layer, kept for the whole turn
	released bool
	// keep outlives the turn: the conversation's root, owned by the owner,
	// for the next turn to extend. chain is the older roots it was forked
	// from, oldest first.
	keep  bool
	chain []int
	// clientPool is the pool the client named when this root was built
	// (client_placement_v1), whether or not the root could stand on it.
	clientPool *int
}

// councilRoot is a conversation's root pool, kept between turns. kv is the
// runner it lives on: a new runner has none of the old one's pools.
type councilRoot struct {
	id    int
	text  string
	kv    llm.PolyKV
	chain []int
	// clientPool is the pool the client named when the root was built. The
	// root is never extended under another: a root forked from a client's
	// pool is its child, and the engine releases no pool with a child, so
	// holding it would keep the client from letting its old pool go.
	clientPool *int
}

// samePool reports whether two named pools are the same, none being one.
func samePool(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// councilRoots holds each conversation's kept root, by owner session.
var councilRoots = &rootRegistry{m: map[string]councilRoot{}}

type rootRegistry struct {
	mu sync.Mutex
	m  map[string]councilRoot
	// promoting is the owners whose idle council is building their root; the
	// next turn waits for it rather than build a second copy beside it.
	promoting map[string]chan struct{}
}

// promotion marks owner's root as being built. done must be called.
func (r *rootRegistry) promotion(owner string) (done func()) {
	ch := make(chan struct{})
	r.mu.Lock()
	if r.promoting == nil {
		r.promoting = map[string]chan struct{}{}
	}
	r.promoting[owner] = ch
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		if r.promoting[owner] == ch {
			delete(r.promoting, owner)
		}
		r.mu.Unlock()
		close(ch)
	}
}

// wait returns once no root is being built for owner, or ctx ends.
func (r *rootRegistry) wait(ctx context.Context, owner string) {
	r.mu.Lock()
	ch := r.promoting[owner]
	r.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

func (r *rootRegistry) take(owner string) (councilRoot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.m[owner]
	delete(r.m, owner)
	return v, ok
}

// put keeps v for owner and returns the root it displaced, if any, which is
// the caller's to let go.
func (r *rootRegistry) put(owner string, v councilRoot) (councilRoot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, had := r.m[owner]
	r.m[owner] = v
	return old, had && (old.id != v.id || old.kv != v.kv)
}

func (r *rootRegistry) reset() {
	r.mu.Lock()
	r.m, r.promoting = map[string]councilRoot{}, nil
	r.mu.Unlock()
}

// ownerPlacement is the planner's: the owner session and its window, attached
// to the conversation's root when its prompt extends it. Before the grant is
// known it asks [floor, window]; afterwards it states the grant, which is how
// a resumed booking continues rather than re-negotiating. A root with no
// owner yet sits outside the window (the first turn), so the window need only
// hold the planner's own tokens, and the floor comes down to that.
func (t *councilTree) ownerPlacement(ctx context.Context, msgs []api.Message) *llm.Placement {
	root := t.rootFor(ctx, msgs)
	t.mu.Lock()
	defer t.mu.Unlock()
	var p *llm.Placement
	switch {
	case t.unowned:
		if root == nil {
			return nil
		}
		p = &llm.Placement{}
	case t.grant > 0:
		p = &llm.Placement{NumCtx: t.grant, NumCtxMin: t.grant}
	default:
		p = &llm.Placement{NumCtx: t.window, NumCtxMin: t.floor}
		if root != nil && !root.keep {
			p.NumCtxMin = min(t.floor, max(4096, roundUp(t.reserve, 256)))
		}
	}
	if root != nil {
		id := root.id
		p.PoolID = &id
	}
	return p
}

// ownerWindow is the owner's booking with no pool: what a call on the owner
// states when it does not read the conversation's root.
func (t *councilTree) ownerWindow() *llm.Placement {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.grant > 0 {
		return &llm.Placement{NumCtx: t.grant, NumCtxMin: t.grant}
	}
	return &llm.Placement{NumCtx: t.window, NumCtxMin: t.floor}
}

// rootFor is the root pool msgs can attach to: this turn's, else the one kept
// from the last turn (the summary of the old turns is asked on it, before this
// turn builds its own). Nil when the prompt does not extend it.
func (t *councilTree) rootFor(ctx context.Context, msgs []api.Message) *councilLayer {
	t.mu.Lock()
	root := t.root
	if root == nil && t.kept != nil {
		root = &councilLayer{text: t.kept.text, id: t.kept.id, keep: true}
	}
	t.mu.Unlock()
	if root == nil {
		return nil
	}
	full, err := t.render(ctx, msgs)
	if err != nil || !strings.HasPrefix(full, root.text) {
		return nil
	}
	return root
}

// rootText is the conversation's root: what every planner prompt starts with.
// A decision and a plan continue the conversation with a user turn, a direct
// answer with the assistant's, so the root ends where those two part: after
// the next turn's opener token, before its role.
func (t *councilTree) rootText(ctx context.Context, conv []api.Message) (string, error) {
	user, err := t.cut(ctx, conv)
	if err != nil {
		return "", err
	}
	answer, err := t.render(ctx, conv)
	if err != nil {
		return "", err
	}
	n := 0
	for n < len(user) && n < len(answer) && user[n] == answer[n] {
		n++
	}
	return user[:n], nil
}

// buildRoot makes this turn's root pool before the planner's first call. It
// extends the root kept from the last turn when the conversation still starts
// with it -- prefilling only what is new -- and otherwise builds it afresh
// and lets the old one go. It belongs to the owner when the owner holds an
// allocation (so it counts in the owner's pressure, and outlives the turn);
// on the first turn there is none yet, so the root is unowned and released
// with the turn.
//
// The error is the engine's refusal of a new root, which the caller answers by
// compacting when it is llm.ErrSessionFull.
func (t *councilTree) buildRoot(ctx context.Context, conv []api.Message) error {
	text, err := t.rootText(ctx, conv)
	if err != nil || text == "" {
		slog.Debug("council: no conversation root", "error", err)
		return nil
	}
	t.mu.Lock()
	live := t.grant > 0 && !t.unowned
	prev := t.kept
	t.kept = nil
	t.mu.Unlock()
	if !live && !t.canUnown {
		return nil
	}
	owner := ""
	if live {
		owner = t.owner
	}

	var p llm.PoolInfo
	var chain []int
	built := false
	switch {
	case prev != nil && !samePool(prev.clientPool, t.clientPool):
		// The client named another pool: the old root goes (below), so the
		// client can let its old pool go.
	case prev != nil && prev.text == text:
		p, chain, built = llm.PoolInfo{ID: prev.id, Len: len(text)}, prev.chain, true
		prev = nil // the same pool: nothing to let go
	case prev != nil && !t.recurrent && len(prev.chain) < councilRootChain && strings.HasPrefix(text, prev.text):
		// The engine never releases a pool with a child, so the old root
		// stays as the new one's parent; each is charged only its own part.
		pid := prev.id
		if p, err = t.kv.CreatePool(ctx, owner, &pid, text); err == nil {
			chain, built = append(slices.Clone(prev.chain), prev.id), true
			prev = nil
		} else {
			slog.Debug("council: could not extend the conversation root", "pool", pid, "error", err)
		}
	}
	if prev != nil {
		// Let the old root go before building afresh: it counts against the
		// same allocation. Still the owner's: begin found its allocation live,
		// and nobody else books this session between turns.
		t.releaseRoot(ctx, prev)
	}
	var refused error
	if !built {
		if p, refused = t.createRoot(ctx, owner, text); refused != nil {
			slog.Info("council: conversation root not built; the planner holds its own copy", "error", refused)
			return refused
		}
	}
	l := &councilLayer{text: text, ready: make(chan struct{}), id: p.ID, root: true, keep: live, chain: chain, clientPool: t.clientPool}
	close(l.ready)
	t.mu.Lock()
	if !live && !t.unowned && t.promoted == nil {
		// The idle council will build the owner its own (promoteRoot). Marked
		// now, while the turn runs, so a next message that arrives the moment
		// this answer ends waits for it.
		t.promoted = councilRoots.promotion(t.owner)
	}
	t.layers[layerKey(text)] = l
	t.order = append(t.order, l)
	t.root = l
	t.mu.Unlock()
	slog.Debug("council: conversation root", "pool", p.ID, "len", p.Len, "own", p.OwnLen, "kept", live)
	return nil
}

// createRoot makes a conversation root. It forks the client's pool when the
// client named one and the conversation starts with it -- the engine checks
// that token for token, and refuses a fork that does not -- and otherwise
// builds the root from nothing, beside the client's pool.
func (t *councilTree) createRoot(ctx context.Context, owner, text string) (llm.PoolInfo, error) {
	if t.clientPool != nil {
		p, err := t.kv.CreatePool(ctx, owner, t.clientPool, text)
		if err == nil {
			return p, nil
		}
		slog.Info("council: the conversation root could not stand on the client's pool; building its own", "pool", *t.clientPool, "error", err)
	}
	return t.kv.CreatePool(ctx, owner, nil, text)
}

// releaseRoot lets a kept root go, and the older roots it was forked from,
// newest first: the engine releases only a pool with no child.
func (t *councilTree) releaseRoot(ctx context.Context, r *councilRoot) {
	ids := append(slices.Clone(r.chain), r.id)
	for i := len(ids) - 1; i >= 0; i-- {
		if err := t.kv.ReleasePool(ctx, ids[i]); err != nil {
			slog.Debug("council: could not release the last turn's root", "pool", ids[i], "error", err)
			return
		}
	}
}

// dropKept lets the kept root go before the conversation is compacted from
// text on the owner. The fold replaces what the root holds, and the text path
// does not read it; kept, it filled the owner's window beside the text pieces
// (measured on b133: "its pools and workers hold 12197, the prompt needs 9962
// private"). The idle fold builds the root again from the compacted
// conversation; a fold that fails costs the next turn a rebuild.
func (t *councilTree) dropKept(ctx context.Context) {
	t.mu.Lock()
	r := t.kept
	t.kept = nil
	t.mu.Unlock()
	if r == nil {
		if v, ok := councilRoots.take(t.owner); ok && v.kv == t.kv {
			r = &v
		}
	} else if v, ok := councilRoots.take(t.owner); ok && v.id != r.id {
		councilRoots.put(t.owner, v)
	}
	if r != nil {
		slog.Debug("council: kept root released for a fold from text", "session", t.owner, "pool", r.id)
		t.releaseRoot(ctx, r)
	}
}

// councilRootChain is how many older roots a kept root may stand on before
// the next turn builds it afresh: each is a pool seat, and a pool with a
// child can never be released.
const councilRootChain = 2

// learnGrant reads the owner's booking from a /kv answer.
func (t *councilTree) learnGrant(k llm.KVStatus) (llm.KVAllocation, bool) {
	a, ok := k.Session(t.owner)
	if ok && a.Window > 0 {
		t.mu.Lock()
		t.grant = a.Window
		t.mu.Unlock()
	}
	return a, ok
}

// workerPlacement attaches a worker to the pool of its layer. Nil means the
// worker could not be pooled and runs on its own, sized booking.
func (t *councilTree) workerPlacement(ctx context.Context, msgs []api.Message, session string) *llm.Placement {
	if len(msgs) < 2 {
		return nil
	}
	layerMsgs := msgs[:len(msgs)-1]
	text, err := t.cut(ctx, layerMsgs)
	if err != nil || text == "" {
		slog.Debug("council: no pool layer for a member", "error", err)
		return nil
	}
	full, err := t.render(ctx, msgs)
	if err != nil || !strings.HasPrefix(full, text) {
		// Rule 5 of the guide: attach only to a byte-prefix of what is sent.
		slog.Debug("council: a member's prompt does not extend its layer; not pooled")
		return nil
	}
	l, err := t.layer(ctx, text, layerMsgs)
	if err != nil {
		slog.Info("council: pool not built, member runs unpooled", "error", err)
		return nil
	}
	t.mu.Lock()
	l.users++
	if t.workers == nil {
		t.workers = map[string]*councilLayer{}
	}
	t.workers[session] = l
	t.mu.Unlock()
	id := l.id
	return &llm.Placement{PoolID: &id}
}

// cut renders msgs followed by a sentinel turn and keeps what precedes it.
func (t *councilTree) cut(ctx context.Context, msgs []api.Message) (string, error) {
	full, err := t.render(ctx, append(slices.Clone(msgs), api.Message{Role: "user", Content: councilSentinel}))
	if err != nil {
		return "", err
	}
	i := strings.Index(full, councilSentinel)
	if i <= 0 {
		return "", errors.New("the template does not carry the sentinel through")
	}
	return full[:i], nil
}

// layer returns the pool for text, building it once however many members ask.
// It forks the longest built layer that text extends; with none, it builds the
// conversation's own layer first when text extends that, so the conversation
// is prefilled once per turn rather than once per branch.
func (t *councilTree) layer(ctx context.Context, text string, msgs []api.Message) (*councilLayer, error) {
	key := layerKey(text)
	t.mu.Lock()
	if l, ok := t.layers[key]; ok {
		t.mu.Unlock()
		select {
		case <-l.ready:
			return l, l.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	l := &councilLayer{text: text, ready: make(chan struct{})}
	t.layers[key] = l
	idle := t.idleLeavesLocked(text)
	t.mu.Unlock()
	t.releaseLayers(ctx, idle)
	t.mu.Lock()
	parent := t.parentLocked(text)
	t.mu.Unlock()

	if parent == nil {
		if root := t.conversationLayer(ctx, msgs, text); root != nil {
			parent = root
		}
	}
	var pid *int
	if parent != nil {
		pid = &parent.id
	}
	owner := t.owner
	if t.unowned {
		owner = ""
	}
	p, err := t.kv.CreatePool(ctx, owner, pid, text)
	l.id, l.err = p.ID, err
	if err == nil {
		t.mu.Lock()
		t.order = append(t.order, l)
		t.mu.Unlock()
		slog.Debug("council: pool built", "pool", p.ID, "parent", pid, "len", p.Len, "own", p.OwnLen, "warning", p.Warn)
	}
	close(l.ready)
	return l, err
}

// conversationLayer builds the layer of the conversation alone -- everything
// up to the first message the council itself added -- when text extends it.
func (t *councilTree) conversationLayer(ctx context.Context, msgs []api.Message, text string) *councilLayer {
	n := conversationEnd(msgs)
	if n <= 0 || n >= len(msgs) {
		return nil
	}
	root, err := t.cut(ctx, msgs[:n])
	if err != nil || root == "" || root == text || !strings.HasPrefix(text, root) {
		return nil
	}
	l, err := t.layer(ctx, root, msgs[:n])
	if err != nil {
		return nil
	}
	t.mu.Lock()
	l.root = true
	t.mu.Unlock()
	return l
}

// conversationEnd is the index of the first message the council added: the
// planner's instruction, which every council message sequence carries.
func conversationEnd(msgs []api.Message) int {
	for i, m := range msgs {
		if m.Role == "user" && council.IsPlannerRequest(m.Content) {
			return i
		}
	}
	return -1
}

// idleLeavesLocked is, on a recurrent-state engine, every layer a new one
// makes redundant: built, not the conversation's, no worker on it, and no
// child (the engine refuses to release a parent). Its members are done, and
// holding it would keep one of the engine's few state cells from the next
// stage: at 131k on the 3090 the elastic cache stayed at 4 cells, and a turn
// that kept all four layers could never seat its synthesizer (bug-118).
func (t *councilTree) idleLeavesLocked(text string) []*councilLayer {
	if !t.recurrent {
		return nil
	}
	var out []*councilLayer
	for _, l := range t.order {
		if l.err != nil || l.released || l.root || l.users > 0 || l.text == text {
			continue
		}
		if t.hasChildLocked(l) {
			continue
		}
		l.released = true
		out = append(out, l)
	}
	return out
}

func (t *councilTree) hasChildLocked(p *councilLayer) bool {
	for _, l := range t.order {
		if l != p && !l.released && len(l.text) > len(p.text) && strings.HasPrefix(l.text, p.text) {
			return true
		}
	}
	return false
}

func (t *councilTree) releaseLayers(ctx context.Context, ls []*councilLayer) {
	for _, l := range ls {
		if err := t.kv.ReleasePool(ctx, l.id); err != nil {
			slog.Debug("council: could not release a finished layer", "pool", l.id, "error", err)
			continue
		}
		slog.Debug("council: released a finished layer", "pool", l.id)
	}
}

func (t *councilTree) parentLocked(text string) *councilLayer {
	var best *councilLayer
	for _, l := range t.order {
		if l.err == nil && !l.released && len(l.text) < len(text) && strings.HasPrefix(text, l.text) {
			if best == nil || len(l.text) > len(best.text) {
				best = l
			}
		}
	}
	return best
}

func layerKey(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:8])
}

// release frees the turn's pools, newest first, so no child outlives its parent.
func (t *councilTree) release() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.mu.Lock()
	var order []*councilLayer
	var keep *councilLayer
	for _, l := range t.order {
		switch {
		case l.released:
		case l.keep:
			keep = l
		default:
			order = append(order, l)
		}
	}
	t.order, t.layers = nil, map[string]*councilLayer{}
	if keep != nil {
		// The idle council summarises on it; the next turn extends it.
		t.kept = &councilRoot{id: keep.id, text: keep.text, kv: t.kv, chain: keep.chain, clientPool: keep.clientPool}
		t.keepRootLocked(ctx)
	} else if t.root != nil && !t.unowned {
		// A first turn's root had no owner and goes with the turn; promote
		// builds the owner its own once the council is idle.
		t.promote = t.root.text
	}
	t.root = nil
	t.mu.Unlock()
	for i := len(order) - 1; i >= 0; i-- {
		if err := t.kv.ReleasePool(ctx, order[i].id); err != nil {
			slog.Debug("council: could not release a pool", "pool", order[i].id, "error", err)
		}
	}
}

// keepRootLocked registers t.kept for the next turn, letting go of any root
// it displaces: a promotion that landed after the next turn began.
func (t *councilTree) keepRootLocked(ctx context.Context) {
	if old, had := councilRoots.put(t.owner, *t.kept); had && old.kv == t.kv {
		t.releaseRoot(ctx, &old)
	}
}

// promoteRoot runs while the council is idle after a first turn. That turn's
// root was built before the owner held an allocation, so it had none and went
// with the turn; now the owner has one, it gets its own root, charged to it.
// The idle council's pressure then counts the conversation, and the next turn
// forks the root instead of prefilling the conversation again.
func (t *councilTree) promoteRoot(ctx context.Context) {
	t.mu.Lock()
	text, done := t.promote, t.promoted
	t.promote, t.promoted = "", nil
	t.mu.Unlock()
	if done == nil {
		return
	}
	defer done()
	if text == "" {
		return
	}
	k, err := t.kv.KV(ctx)
	if err != nil {
		return
	}
	if a, ok := t.learnGrant(k); !ok || a.Window <= 0 {
		return
	}
	p, err := t.createRoot(ctx, t.owner, text)
	if err != nil {
		slog.Debug("council: could not give the owner its root", "error", err)
		return
	}
	t.mu.Lock()
	t.kept = &councilRoot{id: p.ID, text: text, kv: t.kv, clientPool: t.clientPool}
	t.keepRootLocked(ctx)
	t.mu.Unlock()
	slog.Debug("council: conversation root kept for the owner", "session", t.owner, "pool", p.ID, "len", p.Len)
}

// closeWorker ends a worker's session. The guide's rule: every session a
// council opens is closed when its member is done.
func (t *councilTree) closeWorker(id string) {
	t.mu.Lock()
	if l, ok := t.workers[id]; ok {
		l.users--
		delete(t.workers, id)
	}
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.kv.CloseSession(ctx, id); err != nil {
		slog.Debug("council: could not close a worker session", "session", id, "error", err)
	}
}

// begin runs before a turn. It learns the owner's standing booking, grows it
// back toward the ask when nobody is being refused, and reports the pressure
// the conversation is under, so the caller can compact.
func (t *councilTree) begin(ctx context.Context) (pressure float64) {
	// A root the idle council is still building for this owner is waited
	// for, before the pressure is read: built beside this turn's own, the two
	// filled the owner's window and the planner was refused (measured on b128).
	councilRoots.wait(ctx, t.owner)
	k, err := t.kv.KV(ctx)
	if err != nil {
		return 0
	}
	// Read before the booking check: the first turn has no booking yet, and on
	// a recurrent model it is the turn whose layers must be pruned.
	t.mu.Lock()
	t.recurrent = k.RS != nil && k.RS.CellsCap > 0
	t.refusing = k.Pressure.Active()
	t.mu.Unlock()
	a, ok := t.learnGrant(k)
	// The root kept from the last turn is this conversation's only while the
	// owner's allocation lives: closing it releases its pools, and the id can
	// then name another conversation's pool. So it is adopted only while live,
	// and only on the runner that made it.
	if r, had := councilRoots.take(t.owner); had && ok && !t.unowned && r.kv == t.kv {
		t.mu.Lock()
		t.kept = &r
		t.mu.Unlock()
	}
	if !ok {
		return 0
	}
	// An unowned tree's owner books per request; there is no window to grow.
	if !t.unowned && a.Window < t.window && !k.Pressure.Active() {
		r, err := t.kv.Resize(ctx, t.owner, t.window, false)
		if err == nil && r.Refusal != "" && r.LargestAdmissible > a.Window {
			r, err = t.kv.Resize(ctx, t.owner, r.LargestAdmissible, false)
		}
		if err == nil && r.Applied > a.Window {
			slog.Info("council: owner window grown back", "session", t.owner, "from", a.Window, "to", r.Applied)
			t.mu.Lock()
			t.grant = r.Applied
			t.mu.Unlock()
		}
	}
	return a.Pressure
}

// finish runs after a turn: the pools go, and if others are being refused the
// idle owner gives back what it is not using. The shrink is deferred when the
// engine can defer it, so it lands whenever the owner is next idle.
func (t *councilTree) finish(reserve int) {
	t.release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if t.unowned {
		return
	}
	k, err := t.kv.KV(ctx)
	if err != nil || !k.Pressure.Active() {
		return
	}
	a, ok := k.Session(t.owner)
	if !ok {
		return
	}
	target := roundUp(max(t.floor, a.Used+reserve), 256)
	if target >= a.Window || a.Window-target < max(4096, a.Window/10) {
		return
	}
	r, err := t.kv.Resize(ctx, t.owner, target, true)
	if err == nil && (r.Applied > 0 || r.Queued) {
		slog.Info("council: owner window shrunk under pressure", "session", t.owner, "from", a.Window, "to", target, "queued", r.Queued)
	}
}

func roundUp(n, to int) int { return (n + to - 1) / to * to }

// councilWindow resolves the owner's ask from the model's council settings:
// the window defaults to the request's context, the floor to the window (all
// or nothing), compaction to 0.85.
func councilWindow(cc *xollama.CouncilContext, numCtx int) (window, floor int, compactAt float64) {
	window, floor, compactAt = numCtx, 0, defaultCompactAt
	if cc != nil {
		if cc.Window > 0 {
			window = cc.Window
		}
		floor = cc.Floor
		if cc.CompactAt > 0 {
			compactAt = cc.CompactAt
		}
	}
	if floor <= 0 || floor > window {
		floor = window
	}
	return window, floor, compactAt
}

// councilWholePool reports a stated num_ctx 0, the request's over the model's.
func councilWholePool(model, request map[string]any) bool {
	for _, o := range []map[string]any{request, model} {
		if v, ok := o["num_ctx"]; ok {
			switch n := v.(type) {
			case int:
				return n == 0
			case int64:
				return n == 0
			case float64:
				return n == 0
			default:
				return false
			}
		}
	}
	return false
}

// councilIdleCompactAt is the idle threshold for a council's context.
func councilIdleCompactAt(cc *xollama.CouncilContext, compactAt float64) float64 {
	idle := defaultIdleCompactAt
	if cc != nil && cc.IdleCompactAt > 0 {
		idle = cc.IdleCompactAt
	}
	return min(idle, compactAt)
}

// councilTreeFor returns the pool tree for this turn, or nil when the members
// run unpooled: PolyKV off for this council, a runner that is not opencoti or
// has no seats, or an engine without the features. `polykv on` makes the last
// three an error rather than a quiet fallback.
func (s *Server) councilTreeFor(ctx context.Context, m *Model, req api.ChatRequest, session string) (*councilTree, error) {
	cc := m.Xollama.Council
	mode := cc.PolyKV
	if mode == xollama.CouncilPolyKVOff || session == "" {
		return nil, nil
	}
	r, m2, opts, err := s.scheduleRunner(ctx, m, []model.Capability{model.CapabilityCompletion}, req.Options, req.KeepAlive, req.Shift)
	if err != nil {
		return nil, err
	}
	kv, ok := r.(llm.PolyKV)
	if !ok || !kv.PolyKV(ctx) {
		if mode == xollama.CouncilPolyKVOn {
			return nil, errors.New("council.polykv is on, but this load has no PolyKV: it needs the opencoti engine with pools and session affinity")
		}
		return nil, nil
	}
	window, floor, compactAt := councilWindow(cc.Context, opts.NumCtx)
	// num_ctx 0 asks for the whole pool: no window for the planner, and pools
	// no session owns, where the engine can hold them. An engine without them
	// keeps the owner, booking the context the load took -- the whole pool.
	canUnown := kv.Features(ctx)["pool_unowned_v1"]
	unowned := councilWholePool(m.Options, req.Options) && canUnown
	var clientPool *int
	if req.Placement != nil && req.Placement.PoolID != nil && *req.Placement.PoolID >= 0 {
		clientPool = req.Placement.PoolID
	}
	return &councilTree{
		clientPool:    clientPool,
		unowned:       unowned,
		canUnown:      canUnown,
		numCtx:        opts.NumCtx,
		kv:            kv,
		render:        councilRenderer(m2, r, opts),
		tokenize:      r.Tokenize,
		owner:         session,
		window:        window,
		floor:         floor,
		compactAt:     compactAt,
		idleCompactAt: councilIdleCompactAt(cc.Context, compactAt),
		layers:        map[string]*councilLayer{},
	}, nil
}

// councilRenderer renders messages exactly as ChatHandler renders a member's:
// the model's own MESSAGE turns first, thinking off, on whichever path -- the
// engine's template or ollama's -- the model's chats take.
func councilRenderer(m *Model, r llm.LlamaServer, opts *api.Options) func(context.Context, []api.Message) (string, error) {
	off := &api.ThinkValue{Value: false}
	return func(ctx context.Context, msgs []api.Message) (string, error) {
		all := filterThinkTags(append(slices.Clone(m.Messages), msgs...), m)
		if chatModeForModel(m) == chatExecutionModeNative {
			return r.ApplyChatTemplate(ctx, llm.ChatRequest{Messages: all, Options: opts, Think: off})
		}
		p, _, err := chatPrompt(ctx, m, r.Tokenize, opts, all, nil, off, false)
		return p, err
	}
}

// tokens counts what the members would send for msgs.
func (t *councilTree) tokens(ctx context.Context, msgs []api.Message) (int, error) {
	p, err := t.render(ctx, msgs)
	if err != nil {
		return 0, err
	}
	toks, err := t.tokenize(ctx, p)
	return len(toks), err
}

// place decides where a member call runs and names its session. The planner
// runs on the owner; every other member on the council's model is a worker on
// its own session, pooled when its layer could be built and otherwise booked
// at its own size. A role on another model is left alone: it shares nothing.
func (cm *councilMembers) place(ctx context.Context, r council.Request, req *api.ChatRequest) (*llm.Placement, string) {
	t := cm.tree
	if t == nil || r.Model != "" {
		return nil, ""
	}
	if r.Role == council.Planner || r.Role == roleCompactWriter {
		req.SessionID = t.owner
		return t.ownerPlacement(ctx, r.Messages), ""
	}
	if compactionUnpooled(r.Role) && !t.unowned {
		// A compaction call that shares nothing with the tree still belongs to
		// the conversation: it runs on the owner, inside the owner's window.
		// On a session of its own it is booked beside the owner, and once the
		// owner has grown to the whole pool it is never admitted (measured on
		// b133: a 4,608-cell text piece waited out admission beside a
		// 16,384-cell owner).
		req.SessionID = t.owner
		return t.ownerWindow(), ""
	}
	req.SessionID = cm.memberSession(r)
	if !compactionUnpooled(r.Role) {
		if p := t.workerPlacement(ctx, r.Messages, req.SessionID); p != nil {
			return p, req.SessionID
		}
	}
	// Unpooled: state a window sized to this member, never the engine's
	// default, which books the whole session_ctx_max per request.
	n, err := t.tokens(ctx, r.Messages)
	if err != nil {
		n = t.window / 2
	}
	size := min(roundUp(n+r.MaxTokens+512, 256), t.window)
	return &llm.Placement{NumCtx: size, NumCtxMin: size}, req.SessionID
}

// councilPoolSeats is the engine pool seats a council's tree needs: the
// conversation and the synthesizer's layer once, the researchers' and the
// critics' layers once per round, and the older roots a kept conversation
// stands on (councilRootChain). Zero for a model that is not a council or
// whose council does not use PolyKV, so its launch is unchanged.
func councilPoolSeats(m *Model) int {
	if m == nil || m.Xollama == nil || !m.Xollama.Council.On() || m.Xollama.Council.PolyKV == xollama.CouncilPolyKVOff {
		return 0
	}
	return 2 + 2*min(max(m.Xollama.Council.MaxRounds, 1), xollama.MaxCouncilRounds) + councilRootChain
}
