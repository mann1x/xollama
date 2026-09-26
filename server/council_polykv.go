package server

// xollama: a council turn on PolyKV -- plans/agentic-council-chat.md ("PolyKV
// layout for this flow", "Context, pressure and compaction"). Additive; reached
// from server/council.go when the runner can carry a pool tree.
//
// The planner runs on the council's OWNER session, which books the window.
// Every other member is a WORKER: it attaches to a pool holding everything it
// shares with the others and prefills only its own instruction, charged to the
// owner. The tree is not spelled out here; it falls out of the members'
// messages. A worker's layer is its conversation up to the opener of its last
// turn, and each layer forks the longest layer already built that it extends:
//
//	P1  the conversation                  (first worker layer's parent)
//	 └ P2r  + the plan                    researchers
//	    └ P2f  + the findings             critics
//	       └ P3s  + the critiques         synthesizer
//
// The pools live for one turn. The owner stays open between turns, holding the
// conversation for the next decision, and its booking follows the engine's
// pressure: shrunk while others are refused, grown back when they are not.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// defaultCompactAt is the owner pressure at which the conversation is
// compacted before a turn (Cerebriline's threshold, guide §6.5).
const defaultCompactAt = 0.85

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
}

// ownerPlacement is the planner's: the owner session and its window. Before
// the grant is known it asks [floor, window]; afterwards it states the grant,
// which is how a resumed booking continues rather than re-negotiating.
func (t *councilTree) ownerPlacement() *llm.Placement {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.grant > 0 {
		return &llm.Placement{NumCtx: t.grant, NumCtxMin: t.grant}
	}
	return &llm.Placement{NumCtx: t.window, NumCtxMin: t.floor}
}

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
	p, err := t.kv.CreatePool(ctx, t.owner, pid, text)
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
		if m.Role == "user" && strings.HasPrefix(m.Content, "ROLE: PLANNER.") {
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
	for _, l := range t.order {
		if !l.released {
			order = append(order, l)
		}
	}
	t.order, t.layers = nil, map[string]*councilLayer{}
	t.mu.Unlock()
	for i := len(order) - 1; i >= 0; i-- {
		if err := t.kv.ReleasePool(ctx, order[i].id); err != nil {
			slog.Debug("council: could not release a pool", "pool", order[i].id, "error", err)
		}
	}
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
	k, err := t.kv.KV(ctx)
	if err != nil {
		return 0
	}
	// Read before the booking check: the first turn has no booking yet, and on
	// a recurrent model it is the turn whose layers must be pruned.
	t.mu.Lock()
	t.recurrent = k.RS != nil && k.RS.CellsCap > 0
	t.mu.Unlock()
	a, ok := t.learnGrant(k)
	if !ok {
		return 0
	}
	if a.Window < t.window && !k.Pressure.Active() {
		r, err := t.kv.Resize(ctx, t.owner, t.window, false)
		if err == nil && r.Refusal != "" && r.LargestAdmissible > a.Window {
			r, err = t.kv.Resize(ctx, t.owner, r.LargestAdmissible, false)
		}
		if err == nil && r.Applied > 0 {
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

// compactConversation replaces the oldest turns with a summary when the
// conversation would not leave the turn its room in the owner's window. The
// last exchange and the new message stay verbatim; the summary joins the
// system message, so the turns still alternate.
func (s *Server) compactConversation(ctx context.Context, members council.Model, conv []api.Message, tokens func(context.Context, []api.Message) (int, error), budget int) []api.Message {
	n, err := tokens(ctx, conv)
	if err != nil || n <= budget || len(conv) < 5 {
		return conv
	}
	keep := 3 // the last exchange and the new message
	old := conv[1 : len(conv)-keep]
	if len(old) == 0 {
		return conv
	}
	summary, ok := councilSummaries.get(old)
	if !ok {
		var b strings.Builder
		for _, m := range old {
			fmt.Fprintf(&b, "%s: %s\n\n", strings.ToUpper(m.Role), m.Content)
		}
		out, err := members.Stream(ctx, council.Request{
			Role: council.Planner, Temperature: 0.2, MaxTokens: 1024,
			Messages: []api.Message{
				conv[0],
				{Role: "user", Content: "ROLE: PLANNER. Summarise this earlier part of the conversation for the council: keep every fact, decision, number and open question; drop pleasantries.\n\n" + b.String()},
			},
		}, func(string) {})
		if err != nil || strings.TrimSpace(out) == "" {
			return conv
		}
		summary = strings.TrimSpace(out)
		councilSummaries.put(old, summary)
	}
	sys := conv[0]
	sys.Content += "\n\nSummary of the earlier conversation:\n" + summary
	out := append([]api.Message{sys}, conv[len(conv)-keep:]...)
	slog.Info("council: conversation compacted", "tokens", n, "budget", budget, "turns_summarised", len(old))
	return out
}

// councilSummaries remembers summaries, so a conversation past its budget is
// summarised once rather than on every turn.
var councilSummaries = &summaryCache{m: map[string]string{}}

type summaryCache struct {
	mu    sync.Mutex
	m     map[string]string
	order []string
}

func (c *summaryCache) key(msgs []api.Message) string {
	h := sha256.New()
	for _, m := range msgs {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *summaryCache) get(msgs []api.Message) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[c.key(msgs)]
	return v, ok
}

func (c *summaryCache) put(msgs []api.Message, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.key(msgs)
	if _, ok := c.m[k]; !ok {
		c.order = append(c.order, k)
	}
	c.m[k] = v
	for len(c.order) > 64 {
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
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
	return &councilTree{
		kv:        kv,
		render:    councilRenderer(m2, r, opts),
		tokenize:  r.Tokenize,
		owner:     session,
		window:    window,
		floor:     floor,
		compactAt: compactAt,
		layers:    map[string]*councilLayer{},
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
	if r.Role == council.Planner {
		req.SessionID = t.owner
		p := t.ownerPlacement()
		return p, ""
	}
	req.SessionID = cm.memberSession(r)
	if p := t.workerPlacement(ctx, r.Messages, req.SessionID); p != nil {
		return p, req.SessionID
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
// critics' layers once per round. Zero for a model that is not a council or
// whose council does not use PolyKV, so its launch is unchanged.
func councilPoolSeats(m *Model) int {
	if m == nil || m.Xollama == nil || !m.Xollama.Council.On() || m.Xollama.Council.PolyKV == xollama.CouncilPolyKVOff {
		return 0
	}
	return 2 + 2*min(max(m.Xollama.Council.MaxRounds, 1), xollama.MaxCouncilRounds)
}
