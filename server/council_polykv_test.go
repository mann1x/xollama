package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/xollama"
)

// fakeKV is an engine with PolyKV: it records the pool tree a council builds
// and answers /kv from what it was told.
type fakeKV struct {
	*councilRunner

	mu       sync.Mutex
	pools    []fakePool
	released []int
	closed   []string
	resized  []string
	grant    int
	most     int // the most a resize grants; 0 is anything asked
	used     int
	pressure *llm.KVPressure
	// recurrent answers /kv with an rs block, as a hybrid model does.
	recurrent bool
	// session, when set, is the owner's id and sessPressure its raw
	// pressure, readable before the first request (the test states the
	// session).
	session      string
	sessPressure float64
	// unowned advertises pool_unowned_v1.
	unowned bool
	// liveAfter is the owner's window once the council has made its first
	// call, as on the engine: the owner books on the planner's first request.
	liveAfter int
	// full refuses this many new roots (pools with no parent) as the engine
	// does when the owner's allocation is full.
	full int
}

type fakePool struct {
	id      int
	parent  *int
	session string
	text    string
}

func (f *fakeKV) PolyKV(context.Context) bool { return true }
func (f *fakeKV) Features(context.Context) map[string]bool {
	return map[string]bool{"polykv_subpools_v1": true, "kv_status_v1": true, "pool_unowned_v1": f.unowned}
}

func (f *fakeKV) CreatePool(_ context.Context, session string, parent *int, prompt string) (llm.PoolInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if parent == nil && f.full > 0 {
		f.full--
		return llm.PoolInfo{}, fmt.Errorf("%w: session allocation full, compact the session", llm.ErrSessionFull)
	}
	id := len(f.pools) // the first pool is 0, as on the engine
	f.pools = append(f.pools, fakePool{id: id, parent: parent, session: session, text: prompt})
	return llm.PoolInfo{ID: id, Len: len(prompt)}, nil
}

func (f *fakeKV) ReleasePool(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, id)
	return nil
}

func (f *fakeKV) CloseSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	return nil
}

func (f *fakeKV) KV(context.Context) (llm.KVStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.liveAfter > 0 && f.grant == 0 && len(f.e.roles) > 0 {
		f.grant = f.liveAfter
	}
	k := llm.KVStatus{Pressure: f.pressure}
	if f.recurrent {
		k.RS = &llm.KVRecurrent{CellsCommitted: 4, CellsCap: 8}
	}
	if f.grant > 0 {
		id := f.session
		if id == "" {
			id = f.ownerLocked()
		}
		k.Allocations = []llm.KVAllocation{{SessionID: id, Window: f.grant, Used: f.used, Pressure: f.sessPressure}}
	}
	return k, nil
}

func (f *fakeKV) ownerLocked() string {
	for i, r := range f.e.roles {
		if r == "route" {
			return f.e.sessions[i]
		}
	}
	return ""
}

func (f *fakeKV) Resize(_ context.Context, id string, numCtx int, deferred bool) (llm.ResizeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resized = append(f.resized, fmt.Sprintf("%d deferred=%v", numCtx, deferred))
	if deferred {
		return llm.ResizeResult{Queued: true}, nil
	}
	if f.most > 0 && numCtx > f.most {
		numCtx = f.most
	}
	f.grant = numCtx
	return llm.ResizeResult{Applied: numCtx}, nil
}

func polykvCouncil(t *testing.T, e *councilEngine, kv *fakeKV, c *xollama.Council) *Server {
	t.Helper()
	kv.councilRunner = &councilRunner{mockRunner: &mockRunner{contextLength: 32768}, e: e}
	return councilServerOn(t, kv, c, nil)
}

var polykvReq = api.ChatRequest{
	Model:    "council",
	Options:  map[string]any{"num_ctx": 16384},
	Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
}

func TestACouncilOnPolyKVBuildsItsTreeOnce(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{}
	s := polykvCouncil(t, e, kv, councilOn())
	_, content := joined(chatChunks(t, s, polykvReq))
	if !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("answer %q", content)
	}

	kv.mu.Lock()
	defer kv.mu.Unlock()
	// P1 (the conversation), then P2r, P2f, P3s, each forking the last.
	if len(kv.pools) != 4 {
		t.Fatalf("pools built = %d, want 4 (the two researchers share one): %+v", len(kv.pools), kv.pools)
	}
	if kv.pools[0].parent != nil {
		t.Error("the conversation's layer is the root")
	}
	for i := 1; i < 4; i++ {
		p := kv.pools[i]
		if p.parent == nil || *p.parent != i-1 || !strings.HasPrefix(p.text, kv.pools[i-1].text) {
			t.Errorf("pool %d must fork pool %d and extend it", i, i-1)
		}
	}
	owner := kv.ownerLocked()
	for _, p := range kv.pools {
		if p.session != owner {
			t.Errorf("pool %d owned by %q, want the council's owner %q", p.id, p.session, owner)
		}
	}

	// Workers attach to their layer; the planner runs on the owner, booking
	// the window; nothing else states one.
	want := map[string]int{"researcher": 1, "critic": 2, "synthesizer": 3}
	for i, role := range e.roles {
		pl := e.placements[i]
		switch role {
		case "route", "planner":
			if e.sessions[i] != owner || pl == nil || pl.NumCtx != 16384 || pl.PoolID != nil {
				t.Errorf("%s: session %q placement %+v, want the owner booking 16384", role, e.sessions[i], pl)
			}
		default:
			if pl == nil || pl.PoolID == nil || *pl.PoolID != want[role] || pl.NumCtx != 0 {
				t.Errorf("%s: placement %+v, want pool %d and no window", role, pl, want[role])
			}
		}
	}

	// Every worker session closed, the owner kept; the pools released
	// newest first.
	if len(kv.closed) != 5 || slices.Contains(kv.closed, owner) {
		t.Errorf("closed %v: want the 5 workers, never the owner", kv.closed)
	}
	if !slices.Equal(kv.released, []int{3, 2, 1, 0}) {
		t.Errorf("released %v, want [3 2 1 0]", kv.released)
	}
}

func TestARecurrentModelReleasesEachStageItHasFinished(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{recurrent: true}
	s := polykvCouncil(t, e, kv, councilOn())
	_, content := joined(chatChunks(t, s, polykvReq))
	if !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("answer %q", content)
	}

	kv.mu.Lock()
	defer kv.mu.Unlock()
	if len(kv.pools) != 4 {
		t.Fatalf("pools built = %d, want 4: %+v", len(kv.pools), kv.pools)
	}
	// Each stage forks the conversation, not the stage before it, which it
	// released once that stage's members were done: every pool holds one of
	// the engine's few state cells.
	for i := 1; i < 4; i++ {
		if p := kv.pools[i]; p.parent == nil || *p.parent != 0 {
			t.Errorf("pool %d parent %v, want the conversation's pool 0", i, p.parent)
		}
	}
	// P2r goes when P2f is built, P2f when P3s is; the turn's end frees the
	// rest, newest first, and nothing twice.
	if !slices.Equal(kv.released, []int{1, 2, 3, 0}) {
		t.Errorf("released %v, want [1 2 3 0]", kv.released)
	}
}

func TestADirectTurnOnPolyKVBuildsNothing(t *testing.T) {
	e := &councilEngine{route: `{"route":"direct"}`}
	kv := &fakeKV{}
	s := polykvCouncil(t, e, kv, councilOn())
	chatChunks(t, s, polykvReq)
	if len(kv.pools) != 0 || len(kv.closed) != 0 {
		t.Errorf("a direct turn built %d pools and closed %v", len(kv.pools), kv.closed)
	}
}

func TestPolyKVOffRunsTheMembersUnpooled(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{}
	c := councilOn()
	c.PolyKV = xollama.CouncilPolyKVOff
	s := polykvCouncil(t, e, kv, c)
	chatChunks(t, s, polykvReq)
	if len(kv.pools) != 0 {
		t.Errorf("polykv off built %d pools", len(kv.pools))
	}
	for i, pl := range e.placements {
		if pl != nil {
			t.Errorf("%s carried a placement with polykv off: %+v", e.roles[i], pl)
		}
	}
}

// Under pressure the idle owner gives back what it does not use, deferred;
// once the pressure clears, the next turn grows it back to the ask.
func TestTheOwnerWindowFollowsThePressure(t *testing.T) {
	e := &councilEngine{route: `{"route":"direct"}`}
	kv := &fakeKV{grant: 16384, used: 900, pressure: &llm.KVPressure{WindowS: 60, Refused60s: 2, LastRefusalAgeS: 3}}
	s := polykvCouncil(t, e, kv, &xollama.Council{
		Enabled: councilOn().Enabled,
		Context: &xollama.CouncilContext{Window: 16384, Floor: 4096},
	})
	chatChunks(t, s, polykvReq)
	kv.mu.Lock()
	shrunk := slices.Clone(kv.resized)
	kv.grant, kv.pressure, kv.resized = 8192, nil, nil
	kv.mu.Unlock()
	if len(shrunk) != 1 || !strings.HasSuffix(shrunk[0], "deferred=true") {
		t.Fatalf("under pressure: resizes %v, want one deferred shrink", shrunk)
	}

	chatChunks(t, s, polykvReq)
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if len(kv.resized) != 1 || kv.resized[0] != "16384 deferred=false" {
		t.Errorf("pressure clear: resizes %v, want the owner grown back to 16384", kv.resized)
	}
}

func TestCouncilSeatsFollowTheRounds(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		c    *xollama.Council
		want int
	}{
		{nil, 0},
		{&xollama.Council{Enabled: &yes}, 6},
		{&xollama.Council{Enabled: &yes, MaxRounds: 3}, 10},
		{&xollama.Council{Enabled: &yes, PolyKV: xollama.CouncilPolyKVOff}, 0},
	} {
		m := &Model{Xollama: &xollama.Config{Version: 4, Council: tc.c}}
		if got := councilPoolSeats(m); got != tc.want {
			t.Errorf("%+v: seats %d, want %d", tc.c, got, tc.want)
		}
	}
}

// summaries counts the compaction writer's calls, and whether any council
// member was sent the compacted conversation.
func (e *councilEngine) summaries() (calls int, compacted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, p := range e.prompts {
		if e.roles[i] == "compaction-writer" {
			calls++
		}
		if !strings.HasPrefix(e.roles[i], "compaction-") && strings.Contains(p, compactionSummaryHeading) {
			compacted = true
		}
	}
	return calls, compacted
}

// words is n words of w.
func words(n int, w string) string { return strings.TrimSpace(strings.Repeat(w+" ", n)) }

// A conversation long enough to fold, short of its size trigger at num_ctx
// 4096 (2048 tokens): two earlier exchanges of 300 words a message and a new
// question. Only pressure compacts it, and a fold of its first exchange
// shrinks it.
func longCouncilReq(session, last string) api.ChatRequest {
	return api.ChatRequest{
		Model: "council", SessionID: session,
		Options: map[string]any{"num_ctx": 4096},
		Messages: []api.Message{
			{Role: "user", Content: "What is Rayleigh scattering? " + words(300, "q1")},
			{Role: "assistant", Content: "Light scattered by small particles. " + words(300, "a1")},
			{Role: "user", Content: "Does it depend on wavelength? " + words(300, "q2")},
			{Role: "assistant", Content: "Yes, strongly: the fourth power. " + words(300, "a2")},
			{Role: "user", Content: last},
		},
	}
}

func TestATurnCompactsOnTheOwnersPressure(t *testing.T) {
	for _, tt := range []struct {
		pressure float64
		want     bool
	}{{0.9, true}, {0.85, true}, {0.5, false}} {
		councilCompactions.reset()
		e := &councilEngine{route: `{"route":"council"}`}
		kv := &fakeKV{grant: 16384, used: 900, session: "conv-1", sessPressure: tt.pressure}
		s := polykvCouncil(t, e, kv, councilOn())
		chatChunks(t, s, longCouncilReq("conv-1", "Why is the sky blue?"))
		councilIdle.Wait()
		calls, compacted := e.summaries()
		if compacted != tt.want || (calls > 0) != tt.want {
			t.Errorf("pressure %v: %d writer calls, compacted %v; want compacted %v", tt.pressure, calls, compacted, tt.want)
		}
	}
}

func TestAnIdleCouncilSummarisesForTheNextMessage(t *testing.T) {
	councilCompactions.reset()
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	// Past idle_compact_at (0.75), below compact_at (0.85): this turn does not
	// compact, but the council folds once it has answered.
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1", sessPressure: 0.8}
	s := polykvCouncil(t, e, kv, councilOn())
	req := longCouncilReq("conv-1", "Why is the sky blue?")
	_, answer := joined(chatChunks(t, s, req))
	if _, compacted := e.summaries(); compacted {
		t.Fatal("the turn compacted below compact_at")
	}
	councilIdle.Wait()
	if calls, _ := e.summaries(); calls != 1 {
		t.Fatalf("writer calls after the answer = %d, want the idle council's one", calls)
	}
	e.mu.Lock()
	for i, r := range e.roles {
		switch r {
		case "compaction-writer":
			// The next turn of the conversation, on the owner, attached to the
			// root the turn kept: the conversation is not held twice.
			if pl := e.placements[i]; e.sessions[i] != "conv-1" || pl == nil || pl.PoolID == nil || *pl.PoolID != 0 {
				t.Errorf("the writer: session %q placement %+v, want the owner on the kept root 0", e.sessions[i], pl)
			}
		case "compaction-critic-1", "compaction-critic-2", "compaction-synthesizer":
			if pl := e.placements[i]; pl == nil || pl.PoolID == nil || *pl.PoolID == 0 {
				t.Errorf("%s: placement %+v, want P′, the root forked after the writer", r, pl)
			}
		}
	}
	idleRoles := len(e.roles)
	e.mu.Unlock()

	// The next message applies the record: no fold before it, and its members
	// read the summary instead of the folded turns.
	next := req
	next.Messages = append(slices.Clone(req.Messages),
		api.Message{Role: "assistant", Content: answer},
		api.Message{Role: "user", Content: "And why are sunsets red?"})
	chatChunks(t, s, next)
	councilIdle.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.roles[idleRoles]; r != "route" {
		t.Errorf("the next message began with %s, want the council's own decision", r)
	}
	p := e.prompts[idleRoles]
	if !strings.Contains(p, compactionSummaryHeading) || !strings.Contains(p, fakeMerged) {
		t.Error("the next message did not start from the idle summary")
	}
}

func wholePoolReq() api.ChatRequest {
	r := polykvReq
	r.Options = map[string]any{"num_ctx": 0}
	return r
}

// num_ctx 0 on an engine with unowned pools: the planner books no window and
// no pool is anyone's.
func TestNumCtxZeroBuildsUnownedPools(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	// A per-request booking smaller than the load: an owned tree would grow it.
	kv := &fakeKV{unowned: true, grant: 2, session: "conv-1"}
	s := polykvCouncil(t, e, kv, councilOn())
	req := wholePoolReq()
	req.SessionID = "conv-1"
	chatChunks(t, s, req)
	if len(kv.pools) == 0 {
		t.Fatal("no pools were built")
	}
	for _, p := range kv.pools {
		if p.session != "" {
			t.Errorf("pool %d is owned by %q", p.id, p.session)
		}
	}
	for i, r := range e.roles {
		pl := e.placements[i]
		if r == "route" && (pl == nil || pl.NumCtx != 0 || pl.NumCtxMin != 0 || pl.PoolID == nil || *pl.PoolID != 0) {
			t.Errorf("the planner: placement %+v, want the root pool 0 and no window", pl)
		}
	}
	if len(kv.resized) != 0 {
		t.Errorf("an unowned tree resized the owner: %v", kv.resized)
	}
}

// Under pressure, an owned idle council gives back what it does not use; an
// unowned one has no window of its own to give.
func TestAnUnownedCouncilShrinksNothingUnderPressure(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{
		unowned: true, grant: 65536, used: 900, session: "conv-1",
		pressure: &llm.KVPressure{WindowS: 60, Refused60s: 2, LastRefusalAgeS: 3},
	}
	s := polykvCouncil(t, e, kv, councilOn())
	req := wholePoolReq()
	req.SessionID = "conv-1"
	chatChunks(t, s, req)
	councilIdle.Wait()
	if len(kv.resized) != 0 {
		t.Errorf("an unowned tree resized the owner: %v", kv.resized)
	}
}

// Without pool_unowned_v1, or with a stated window, the pools stay the owner's.
func TestTheOwnerKeepsItsPoolsWithoutUnownedPools(t *testing.T) {
	for name, tc := range map[string]struct {
		unowned bool
		req     api.ChatRequest
	}{
		"no feature":    {false, wholePoolReq()},
		"stated window": {true, polykvReq},
	} {
		t.Run(name, func(t *testing.T) {
			e := &councilEngine{route: `{"route":"council"}`}
			kv := &fakeKV{unowned: tc.unowned}
			s := polykvCouncil(t, e, kv, councilOn())
			chatChunks(t, s, tc.req)
			if len(kv.pools) == 0 {
				t.Fatal("no pools were built")
			}
			for _, p := range kv.pools {
				// With the feature, the first turn's conversation root is
				// built before the owner holds an allocation: it has no owner
				// and goes with the turn. Every layer after it is the owner's.
				if p.session == "" && (!tc.unowned || p.id != 0) {
					t.Errorf("pool %d has no owner", p.id)
				}
			}
		})
	}
}

func TestCouncilWholePoolReadsTheRequestFirst(t *testing.T) {
	for _, tc := range []struct {
		model, req map[string]any
		want       bool
	}{
		{nil, nil, false},
		{map[string]any{"num_ctx": 0}, nil, true},
		{map[string]any{"num_ctx": 0}, map[string]any{"num_ctx": 8192}, false},
		{map[string]any{"num_ctx": 8192}, map[string]any{"num_ctx": float64(0)}, true},
		{nil, map[string]any{"num_ctx": int64(0)}, true},
		{nil, map[string]any{"num_ctx": "0"}, false},
	} {
		if got := councilWholePool(tc.model, tc.req); got != tc.want {
			t.Errorf("councilWholePool(%v, %v) = %v, want %v", tc.model, tc.req, got, tc.want)
		}
	}
}

// On the first turn the owner holds no allocation yet: the conversation's root
// is built unowned, the planner attaches to it and asks only for its own part,
// and the root goes with the turn.
func TestThePlannerAttachesTheConversationRoot(t *testing.T) {
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{unowned: true}
	s := polykvCouncil(t, e, kv, councilOn())
	chatChunks(t, s, polykvReq)

	kv.mu.Lock()
	defer kv.mu.Unlock()
	if len(kv.pools) != 4 || kv.pools[0].parent != nil || kv.pools[0].session != "" {
		t.Fatalf("pools %+v: want an unowned root and three layers", kv.pools)
	}
	if p := kv.pools[1]; p.parent == nil || *p.parent != 0 {
		t.Errorf("the researchers' layer forks %v, want the root", p.parent)
	}
	reserve := councilReserve(council.FromModel(councilOn(), 0.7))
	for i, r := range e.roles {
		pl := e.placements[i]
		if r != "route" && r != "planner" {
			continue
		}
		if pl == nil || pl.PoolID == nil || *pl.PoolID != 0 || pl.NumCtx != 16384 || pl.NumCtxMin != max(4096, roundUp(reserve, 256)) {
			t.Errorf("%s: placement %+v, want the root, a 16384 window and its own part as the floor", r, pl)
		}
	}
	if !slices.Equal(kv.released, []int{3, 2, 1, 0}) {
		t.Errorf("released %v, want [3 2 1 0]", kv.released)
	}
}

// nextTurn is req followed by its answer and a new question.
func nextTurn(req api.ChatRequest, q string) api.ChatRequest {
	req.Messages = append(slices.Clone(req.Messages),
		api.Message{Role: "assistant", Content: "The sky is blue because air scatters blue light most."},
		api.Message{Role: "user", Content: q})
	return req
}

// A live owner keeps its conversation's root between turns; the next turn
// forks it and prefills only what is new, until the chain is as deep as
// councilRootChain, when the root is built afresh and the old ones let go.
func TestTheNextTurnExtendsTheKeptRoot(t *testing.T) {
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1"}
	s := polykvCouncil(t, e, kv, councilOn())
	req := polykvReq
	req.SessionID = "conv-1"

	roots := []int{}
	for turn, q := range []string{"", "And sunsets?", "And the moon?", "And Mars?"} {
		if q != "" {
			req = nextTurn(req, q)
		}
		kv.mu.Lock()
		first, released := len(kv.pools), len(kv.released)
		kv.mu.Unlock()
		chatChunks(t, s, req)
		councilIdle.Wait()

		kv.mu.Lock()
		root := kv.pools[first]
		if root.session != "conv-1" {
			t.Errorf("turn %d: root owned by %q, want the owner", turn, root.session)
		}
		rel := kv.released[released:]
		switch turn {
		case 0, 3:
			if root.parent != nil {
				t.Errorf("turn %d: root forks %v, want a fresh one", turn, *root.parent)
			}
		default:
			if root.parent == nil || *root.parent != roots[turn-1] {
				t.Errorf("turn %d: root parent %v, want the kept root %d", turn, root.parent, roots[turn-1])
			}
		}
		if turn == 3 {
			// The old chain goes first, newest first, then this turn's layers.
			if want := []int{roots[2], roots[1], roots[0]}; len(rel) < 3 || !slices.Equal(rel[:3], want) {
				t.Errorf("turn 3 released %v, want the old roots %v first", rel, want)
			}
			rel = rel[3:]
		}
		if slices.Contains(rel, root.id) {
			t.Errorf("turn %d released its root %d: %v", turn, root.id, rel)
		}
		roots = append(roots, root.id)
		kv.mu.Unlock()
	}
	for i, r := range e.roles {
		if pl := e.placements[i]; r == "route" && (pl == nil || pl.PoolID == nil || pl.NumCtx != 16384 || pl.NumCtxMin != 16384) {
			t.Errorf("the planner: placement %+v, want its grant, attached to the root", pl)
		}
	}
}

// On a recurrent-state model every pool holds a state cell: a kept root is
// not stood on, but let go before this turn's is built.
func TestARecurrentModelRebuildsTheRootEachTurn(t *testing.T) {
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1", recurrent: true}
	s := polykvCouncil(t, e, kv, councilOn())
	req := polykvReq
	req.SessionID = "conv-1"
	chatChunks(t, s, req)
	kv.mu.Lock()
	first, released := len(kv.pools), len(kv.released)
	kv.mu.Unlock()
	chatChunks(t, s, nextTurn(req, "And sunsets?"))
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.pools[first].parent != nil {
		t.Errorf("the second root forks %v, want a fresh one", *kv.pools[first].parent)
	}
	if rel := kv.released[released:]; len(rel) == 0 || rel[0] != 0 {
		t.Errorf("second turn released %v, want the kept root 0 first", rel)
	}
}

// A kept root is the conversation's only while its owner's allocation lives:
// once it is gone, so are its pools, and the id may name another's. Nothing
// the next turn asks attaches to it -- not even the compaction's writer, which
// runs before the turn builds its own root.
func TestAStaleRootIsNeitherUsedNorReleased(t *testing.T) {
	councilRoots.reset()
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1"}
	s := polykvCouncil(t, e, kv, councilOn())
	req := longCouncilReq("conv-1", "Why is the sky blue?")
	chatChunks(t, s, req)
	councilIdle.Wait()
	kv.mu.Lock()
	kv.grant = 0 // the owner's allocation closed between turns
	first, released := len(kv.pools), len(kv.released)
	kv.mu.Unlock()
	e.mu.Lock()
	calls := len(e.placements)
	e.mu.Unlock()

	// A long new question takes the conversation past its size trigger.
	chatChunks(t, s, nextTurn(req, "And sunsets? "+words(1200, "q3")))
	councilIdle.Wait()
	if n, _ := e.summaries(); n == 0 {
		t.Fatal("the second turn did not compact")
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if slices.Contains(kv.released[released:], 0) {
		t.Errorf("released the stale root 0: %v", kv.released[released:])
	}
	for _, p := range kv.pools[first:] {
		if p.parent != nil && *p.parent == 0 {
			t.Errorf("pool %d forks the stale root", p.id)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := calls; i < len(e.placements); i++ {
		if pl := e.placements[i]; pl != nil && pl.PoolID != nil && *pl.PoolID == 0 {
			t.Errorf("%s attached the stale root", e.roles[i])
		}
	}
}

// An engine that refuses the root with "compact the session" gets exactly
// that: the conversation is folded and the root built on the short one.
func TestARefusedRootCompactsAndRetries(t *testing.T) {
	councilRoots.reset()
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1", full: 1}
	s := polykvCouncil(t, e, kv, councilOn())
	chatChunks(t, s, longCouncilReq("conv-1", "Why is the sky blue?"))
	councilIdle.Wait()
	if _, compacted := e.summaries(); !compacted {
		t.Fatal("the turn did not compact after the refusal")
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if len(kv.pools) == 0 || kv.pools[0].parent != nil || !strings.Contains(kv.pools[0].text, compactionSummaryHeading) {
		t.Errorf("the root was not built on the compacted conversation: %+v", kv.pools)
	}
}

// A refusal that no fold can relieve is not retried: the planner runs on its
// own copy, as without pools.
func TestARefusedRootThatCannotFoldIsNotRetried(t *testing.T) {
	councilRoots.reset()
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1", full: 1}
	s := polykvCouncil(t, e, kv, councilOn())
	req := polykvReq
	req.SessionID = "conv-1"
	chatChunks(t, s, req)
	councilIdle.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.roles {
		if r == "route" && e.placements[i] != nil && e.placements[i].PoolID != nil {
			t.Errorf("the planner attached pool %d after a refusal nothing relieved", *e.placements[i].PoolID)
		}
	}
}

// A first turn's root has no owner and goes with the turn. Once the council
// is idle the owner has an allocation, and gets its own root: an idle fold is
// written on it, and the next turn forks it -- or, after a fold, the root the
// idle council built from the compacted conversation.
func TestTheIdleCouncilGivesTheOwnerItsRoot(t *testing.T) {
	for _, pressure := range []float64{0.8, 0} {
		councilRoots.reset()
		councilCompactions.reset()
		e := &councilEngine{route: `{"route":"council"}`}
		kv := &fakeKV{unowned: true, liveAfter: 16384, used: 900, session: "conv-1", sessPressure: pressure}
		s := polykvCouncil(t, e, kv, councilOn())
		req := longCouncilReq("conv-1", "Why is the sky blue?")
		chatChunks(t, s, req)
		councilIdle.Wait()

		kv.mu.Lock()
		if kv.pools[0].session != "" || !slices.Contains(kv.released, 0) {
			t.Fatalf("the first root %+v released %v: want it unowned and gone with the turn", kv.pools[0], kv.released)
		}
		promoted, rebuilt := -1, -1
		for _, p := range kv.pools[1:] {
			switch {
			case p.parent == nil && p.session == "conv-1" && p.text == kv.pools[0].text:
				promoted = p.id
			case p.parent == nil && p.session == "conv-1" && strings.Contains(p.text, compactionSummaryHeading):
				rebuilt = p.id
			}
		}
		first := len(kv.pools)
		kv.mu.Unlock()
		if promoted < 0 {
			t.Fatalf("pressure %v: the owner was not given its root", pressure)
		}
		e.mu.Lock()
		for i, r := range e.roles {
			if r == "compaction-writer" {
				if pl := e.placements[i]; pl == nil || pl.PoolID == nil || *pl.PoolID != promoted {
					t.Errorf("the idle writer's placement %+v, want the owner's root %d", pl, promoted)
				}
			}
		}
		e.mu.Unlock()
		if (rebuilt >= 0) != (pressure > 0) {
			t.Fatalf("pressure %v: root rebuilt from the compacted conversation = %d", pressure, rebuilt)
		}

		chatChunks(t, s, nextTurn(req, "And sunsets?"))
		councilIdle.Wait()
		kv.mu.Lock()
		want := promoted
		if pressure > 0 {
			want = rebuilt
		}
		if r := kv.pools[first]; r.parent == nil || *r.parent != want {
			t.Errorf("pressure %v: the second turn's root forks %v, want %d", pressure, r.parent, want)
		}
		kv.mu.Unlock()
	}
}

func TestARootRegistryLetsGoOfWhatItDisplaces(t *testing.T) {
	r := &rootRegistry{m: map[string]councilRoot{}}
	kv := &fakeKV{}
	if _, had := r.put("a", councilRoot{id: 1, kv: kv}); had {
		t.Error("an empty registry displaced a root")
	}
	if _, had := r.put("a", councilRoot{id: 1, kv: kv}); had {
		t.Error("the same root displaced itself")
	}
	if old, had := r.put("a", councilRoot{id: 2, kv: kv}); !had || old.id != 1 {
		t.Errorf("put over root 1 returned %v %v", old, had)
	}
}

// A promotion that lands after the next turn kept its own root displaces
// that one, which is let go rather than left holding a pool seat.
func TestKeepingARootReleasesTheOneItDisplaces(t *testing.T) {
	councilRoots.reset()
	defer councilRoots.reset()
	kv := &fakeKV{}
	councilRoots.put("conv-1", councilRoot{id: 7, kv: kv, chain: []int{5}})
	tr := &councilTree{kv: kv, owner: "conv-1", kept: &councilRoot{id: 9, kv: kv}}
	tr.mu.Lock()
	tr.keepRootLocked(t.Context())
	tr.mu.Unlock()
	if !slices.Equal(kv.released, []int{7, 5}) {
		t.Errorf("released %v, want the displaced root and its chain, newest first", kv.released)
	}
	if r, ok := councilRoots.take("conv-1"); !ok || r.id != 9 {
		t.Errorf("kept %v, want root 9", r)
	}
}

// A next message that arrives while the idle council is still building the
// owner's root waits for it, rather than build a second copy beside it.
func TestTheNextTurnWaitsForTheOwnersRoot(t *testing.T) {
	councilRoots.reset()
	kv := &fakeKV{councilRunner: &councilRunner{e: &councilEngine{}}}
	done := councilRoots.promotion("conv-1")
	tr := &councilTree{kv: kv, owner: "conv-1", layers: map[string]*councilLayer{}}
	began := make(chan struct{})
	go func() {
		tr.begin(t.Context())
		close(began)
	}()
	select {
	case <-began:
		t.Fatal("the turn began while the owner's root was being built")
	case <-time.After(50 * time.Millisecond):
	}
	done()
	select {
	case <-began:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not begin once the root was built")
	}
}
