package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ollama/ollama/api"
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
	used     int
	pressure *llm.KVPressure
}

type fakePool struct {
	id      int
	parent  *int
	session string
	text    string
}

func (f *fakeKV) PolyKV(context.Context) bool { return true }
func (f *fakeKV) Features(context.Context) map[string]bool {
	return map[string]bool{"polykv_subpools_v1": true, "kv_status_v1": true}
}

func (f *fakeKV) CreatePool(_ context.Context, session string, parent *int, prompt string) (llm.PoolInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	k := llm.KVStatus{Pressure: f.pressure}
	if f.grant > 0 {
		k.Allocations = []llm.KVAllocation{{SessionID: f.ownerLocked(), Window: f.grant, Used: f.used}}
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
		{&xollama.Council{Enabled: &yes}, 4},
		{&xollama.Council{Enabled: &yes, MaxRounds: 3}, 8},
		{&xollama.Council{Enabled: &yes, PolyKV: xollama.CouncilPolyKVOff}, 0},
	} {
		m := &Model{Xollama: &xollama.Config{Version: 4, Council: tc.c}}
		if got := councilPoolSeats(m); got != tc.want {
			t.Errorf("%+v: seats %d, want %d", tc.c, got, tc.want)
		}
	}
}
