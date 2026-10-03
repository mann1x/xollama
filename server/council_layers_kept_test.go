package server

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

// keptLayersCouncil is a live owner (so the conversation's root is kept)
// whose researchers read before they answer: its first request ends with
// their calls.
func keptLayersCouncil(t *testing.T) (*Server, *fakeKV, api.ChatRequest) {
	t.Helper()
	councilStateKeyIn(t, t.TempDir())
	councilRoots.reset()
	councilStashes.reset()
	t.Cleanup(councilStashes.reset)
	e := &councilEngine{route: `{"route":"council"}`, tools: map[string]string{"researcher": "read_files"}}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-1"}
	s := polykvCouncil(t, e, kv, councilOn())
	empty := ""
	req := polykvReq
	req.SessionID = "conv-1"
	req.Tools, req.CouncilChatState = councilTestTools, &empty
	return s, kv, req
}

// suspendedTrip runs req, which must end with the members' calls, and
// returns the request that brings their results back.
func suspendedTrip(t *testing.T, s *Server, req api.ChatRequest) api.ChatRequest {
	t.Helper()
	chunks := toolChat(t, s, req)
	councilIdle.Wait()
	var calls []api.ToolCall
	for _, c := range chunks {
		calls = append(calls, c.Message.ToolCalls...)
	}
	if len(calls) != 2 {
		t.Fatalf("calls %+v, want both researchers'", calls)
	}
	state := chunks[len(chunks)-1].CouncilChatState
	req.CouncilChatState = &state
	req.Messages = append(slices.Clone(req.Messages), api.Message{Role: "assistant", ToolCalls: calls},
		api.Message{Role: "tool", ToolCallID: calls[0].ID, Content: "R1-RESULT"},
		api.Message{Role: "tool", ToolCallID: calls[1].ID, Content: "R2-RESULT"})
	return req
}

// snapshot is what the engine has built and released so far.
func (f *fakeKV) snapshot() (pools []fakePool, released []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pools), slices.Clone(f.released)
}

func (f *fakeKV) live() (ids []int) {
	pools, released := f.snapshot()
	for _, p := range pools {
		if !slices.Contains(released, p.id) {
			ids = append(ids, p.id)
		}
	}
	return ids
}

// A turn's tool round trip keeps the layers its members attached to, and the
// next request of the same turn attaches to them again instead of prefilling
// them anew; once the turn has its answer, they go. Measured on eleven2go
// (hard, 5ce5f7e7): 265334 of the 582509 tokens its builds prefilled were
// layers the previous round trip had just released.
func TestARoundTripKeepsItsStageLayers(t *testing.T) {
	s, kv, req := keptLayersCouncil(t)
	next := suspendedTrip(t, s, req)

	pools, _ := kv.snapshot()
	if len(pools) < 2 {
		t.Fatalf("pools %+v: want the root and the researchers' layer", pools)
	}
	stage := pools[len(pools)-1]
	if live := kv.live(); !slices.Contains(live, stage.id) {
		t.Fatalf("the researchers' layer %d was released at the round trip; live %v", stage.id, live)
	}

	if _, content := joined(toolChat(t, s, next)); !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("answer %q", content)
	}
	councilIdle.Wait()
	after, released := kv.snapshot()
	for _, p := range after[len(pools):] {
		if p.text == stage.text {
			t.Errorf("pool %d rebuilt the kept layer %d", p.id, stage.id)
		}
	}
	if !slices.Contains(released, stage.id) {
		t.Errorf("the answered turn kept layer %d: released %v", stage.id, released)
	}
	// The root is kept for the next turn; everything else went, newest first.
	if live := kv.live(); len(live) != 1 || live[0] != pools[0].id {
		t.Errorf("live after the answer %v, want only the root %d", live, pools[0].id)
	}
	for i := 1; i < len(released); i++ {
		if released[i] > released[i-1] {
			t.Errorf("released %v: a parent went before its child", released)
			break
		}
	}
}

// Layers kept for one turn are never another's: a new user message releases them.
func TestAnotherTurnReleasesTheKeptLayers(t *testing.T) {
	s, kv, req := keptLayersCouncil(t)
	suspendedTrip(t, s, req)
	pools, _ := kv.snapshot()
	stage := pools[len(pools)-1]

	other := req
	other.Messages = append(slices.Clone(req.Messages), api.Message{Role: "assistant", Content: "Let me look."},
		api.Message{Role: "user", Content: "Never mind: why is grass green?"})
	toolChat(t, s, other)
	councilIdle.Wait()
	_, released := kv.snapshot()
	if !slices.Contains(released, stage.id) {
		t.Errorf("another turn left layer %d live: released %v", stage.id, released)
	}
}

// A client that does not come back does not hold the owner's cells forever.
func TestKeptLayersGoWhenTheClientDoesNotComeBack(t *testing.T) {
	was := councilStashIdle
	councilStashIdle = 20 * time.Millisecond
	t.Cleanup(func() { councilStashIdle = was })
	s, kv, req := keptLayersCouncil(t)
	suspendedTrip(t, s, req)
	pools, _ := kv.snapshot()
	stage := pools[len(pools)-1]
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, released := kv.snapshot()
		if slices.Contains(released, stage.id) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("layer %d still live after the idle wait: released %v", stage.id, released)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := councilStashes.take("conv-1"); st != nil {
		t.Errorf("the stash is still registered: %+v", st)
	}
}

// Only what the round trip asked for is kept, and what it stands on: a layer
// adopted and not used again is a stage the turn has moved past.
func TestAStashKeepsOnlyTheLayersInUse(t *testing.T) {
	root := &councilLayer{text: "R", keep: true, id: 0}
	a := &councilLayer{text: "RA", id: 1}
	ab := &councilLayer{text: "RAB", id: 2, used: true}
	stale := &councilLayer{text: "RC", id: 3}
	councilStashes.reset()
	t.Cleanup(councilStashes.reset)
	tr := &councilTree{kv: &fakeKV{}, owner: "conv-1", stashTurn: "t1"}
	rest := tr.stashLocked([]*councilLayer{a, ab, stale}, root)
	if len(rest) != 1 || rest[0] != stale {
		t.Fatalf("released %v, want only the unused layer", rest)
	}
	st := councilStashes.take("conv-1")
	if st == nil || len(st.layers) != 2 || st.layers[0] != a || st.layers[1] != ab || st.root != 0 || st.turn != "t1" {
		t.Fatalf("stash %+v, want the used layer and its parent on root 0", st)
	}
	if ab.used {
		t.Error("a kept layer must start the next round trip unused")
	}

	// Nothing is kept without a kept root, nor a turn that ended.
	for _, tc := range []struct {
		turn string
		keep *councilLayer
	}{{"", root}, {"t1", nil}} {
		tr := &councilTree{kv: &fakeKV{}, owner: "conv-2", stashTurn: tc.turn}
		l := &councilLayer{text: "RA", id: 1, used: true}
		if rest := tr.stashLocked([]*councilLayer{l}, tc.keep); len(rest) != 1 || councilStashes.take("conv-2") != nil {
			t.Errorf("turn %q keep %v: kept %v", tc.turn, tc.keep != nil, rest)
		}
	}
}

// A stash is adopted only on its runner, under the root it stands on, in its
// own turn; any other is released.
func TestAStashIsAdoptedOnlyWhereItStands(t *testing.T) {
	for _, tc := range []struct {
		name          string
		root          *councilRoot
		turn          string
		other         bool
		adopted, lost bool
	}{
		{name: "same", root: &councilRoot{id: 0}, turn: "t1", adopted: true},
		{name: "no root", root: nil, turn: "t1", lost: true},
		{name: "another root", root: &councilRoot{id: 7}, turn: "t1", lost: true},
		{name: "another turn", root: &councilRoot{id: 0}, turn: "t2", lost: true},
		{name: "another runner", root: &councilRoot{id: 0}, turn: "t1", other: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			councilStashes.reset()
			t.Cleanup(councilStashes.reset)
			kv := &fakeKV{}
			l := &councilLayer{text: "RA", id: 1}
			councilStashes.put("conv-1", &councilStash{kv: kv, turn: "t1", root: 0, layers: []*councilLayer{l}})
			tr := &councilTree{kv: kv, owner: "conv-1", layers: map[string]*councilLayer{}}
			if tc.other {
				tr.kv = &fakeKV{}
			}
			tr.takeStash(t.Context(), tc.root)
			tr.adopt(t.Context(), tc.turn)
			_, released := kv.snapshot()
			if got := tr.layers[layerKey("RA")] == l; got != tc.adopted {
				t.Errorf("adopted %v, want %v", got, tc.adopted)
			}
			if got := slices.Contains(released, 1); got != tc.lost {
				t.Errorf("released %v, want released=%v", released, tc.lost)
			}
			// Another runner's pools are not this one's to release.
			if _, rel := tr.kv.(*fakeKV).snapshot(); tc.other && len(rel) != 0 {
				t.Errorf("released %v on the wrong runner", rel)
			}
		})
	}
}

// A root rebuilt under adopted layers lets them go first: the engine releases
// no pool with a child.
func TestARebuiltRootDropsTheAdoptedLayersFirst(t *testing.T) {
	kv := &fakeKV{}
	a := &councilLayer{text: "RA", id: 1}
	ab := &councilLayer{text: "RAB", id: 2}
	tr := &councilTree{kv: kv, owner: "conv-1", layers: map[string]*councilLayer{layerKey("RA"): a, layerKey("RAB"): ab}, adopted: []*councilLayer{a, ab}}
	tr.dropAdopted(t.Context())
	_, released := kv.snapshot()
	if !slices.Equal(released, []int{2, 1}) || len(tr.layers) != 0 || !a.released || !ab.released {
		t.Errorf("released %v, layers %d: want [2 1], none left", released, len(tr.layers))
	}
}
