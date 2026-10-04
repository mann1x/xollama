package server

import (
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
)

// The owner holding the whole window gives a member booked beside it the
// cells it needs, keeping its used cells and the turn's reserve; with room
// already there, or too little to give, it is left alone.
func TestTheOwnerMakesRoomForAMemberBookedBesideIt(t *testing.T) {
	zero, some := 0, 8192
	cases := []struct {
		name       string
		admissible *int
		used       int
		want       []string
		grant      int
	}{
		{"full", &zero, 2305, []string{"190976 deferred=false"}, 190976},
		{"room already", &some, 2305, nil, 196608},
		{"nothing to give", &zero, 190000, nil, 196608},
		{"engine does not say", nil, 2305, nil, 196608},
	}
	for _, tc := range cases {
		kv := &fakeKV{session: "conv-1", grant: 196608, used: tc.used, admissible: tc.admissible}
		tr := &councilTree{kv: kv, owner: "conv-1", window: 196608, floor: 196608, grant: 196608, reserve: 4096}
		tr.roomFor(t.Context(), 5632)
		if len(kv.resized) != len(tc.want) || (len(tc.want) > 0 && kv.resized[0] != tc.want[0]) {
			t.Errorf("%s: resized %v, want %v", tc.name, kv.resized, tc.want)
		}
		if tr.grant != tc.grant {
			t.Errorf("%s: grant %d, want %d", tc.name, tr.grant, tc.grant)
		}
	}
}

// The builder asks the owner for room before it is booked.
func TestTheBuilderIsGivenRoomBesideAFullOwner(t *testing.T) {
	zero := 0
	kv := &fakeKV{session: "conv-1", grant: 196608, used: 2305, admissible: &zero}
	cm := &councilMembers{session: "conv-1", tree: &councilTree{kv: kv, owner: "conv-1", window: 196608, floor: 196608, grant: 196608, reserve: 4096}}
	req := &api.ChatRequest{}
	p, _, done := cm.place(t.Context(), council.Request{Role: council.Builder, MaxTokens: 3072, Messages: []api.Message{{Role: "user", Content: "fix it"}}}, req)
	done()
	if p == nil || p.NumCtxMin == 0 {
		t.Fatalf("builder placement %+v", p)
	}
	if len(kv.resized) != 1 {
		t.Fatalf("the owner was not asked for room: %v", kv.resized)
	}
}

// A thinking member with a format has its schema converted by the engine in a
// window of its own first: the owner gives back the cells for it, and only on
// the council's own model.
func TestAThinkingMemberWithAFormatIsGivenRoomForItsGrammar(t *testing.T) {
	few := 185
	schema := []byte(`{"type":"object","properties":{"room_test_only":{"type":"string"}}}`)
	cases := []struct {
		name string
		r    council.Request
		want int
	}{
		{"format on the council's model", council.Request{Role: council.Planner, Format: schema}, 1},
		{"no format", council.Request{Role: council.Planner}, 0},
		{"another model", council.Request{Role: council.Planner, Format: schema, Model: "glm-5.3:cloud"}, 0},
		{"another host", council.Request{Role: council.Planner, Format: schema, Host: "http://10.0.0.2:22434"}, 0},
	}
	for _, tc := range cases {
		kv := &fakeKV{session: "conv-1", grant: 177920, used: 429, admissible: &few}
		cm := &councilMembers{session: "conv-1", tree: &councilTree{kv: kv, owner: "conv-1", window: 196608, floor: 196608, grant: 177920, reserve: 4096}}
		cm.grammarRoom(t.Context(), tc.r)
		if len(kv.resized) != tc.want {
			t.Errorf("%s: resized %v, want %d resize(s)", tc.name, kv.resized, tc.want)
		}
	}
	// Without a tree (no PolyKV) there is no owner to ask.
	(&councilMembers{}).grammarRoom(t.Context(), council.Request{Format: schema})
}
