package server

import (
	"slices"
	"testing"

	"github.com/ollama/ollama/api"
)

// A harness's directive brings tools to the council without a state of its
// own, and the turn sends one back; a stated mode is never routed.
func TestADirectiveServesToolsAndHoldsItsMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want []string
	}{
		{api.CouncilModeAnswer, []string{"front"}},
		{api.CouncilModeDeliberate, []string{"builder", "planner"}},
	} {
		councilStateKeyIn(t, t.TempDir())
		e := &councilEngine{route: `{"route":"council"}`}
		s := councilToolServer(t, e)
		req := api.ChatRequest{
			Model: "council", Tools: councilTestTools, SessionID: "conv-directive",
			Council:  &api.CouncilDirective{Mode: tc.mode},
			Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
		}
		state := false
		for _, c := range toolChat(t, s, req) {
			state = state || c.CouncilChatState != ""
		}
		if !state {
			t.Errorf("%s: no council_chat_state came back", tc.mode)
		}
		if e.count("route") != 0 || len(e.roles) < len(tc.want) || !slices.Equal(e.roles[:len(tc.want)], tc.want) {
			t.Errorf("%s: roles %v, want %v first and no route", tc.mode, e.roles, tc.want)
		}
		if tc.mode == api.CouncilModeAnswer && len(e.roles) != 1 {
			t.Errorf("answer: %d calls %v, want the synthesizer alone", len(e.roles), e.roles)
		}
	}
}

// A mode the council does not know is a 400, never a silent auto.
func TestAnUnknownModeIsRefused(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilToolServer(t, e)
	w := createRequest(t, s.ChatHandler, api.ChatRequest{
		Model: "council", Council: &api.CouncilDirective{Mode: "fast"},
		Messages: []api.Message{{Role: "user", Content: "hi"}},
	})
	if w.Code != 400 {
		t.Errorf("status %d, want 400", w.Code)
	}
}
