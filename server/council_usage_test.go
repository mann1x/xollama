package server

import (
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
)

// The done chunk says what each role spent, so a client can tell which role
// is worth a bigger or a cloud model (council_usage_v1).
func TestACouncilTurnReportsWhatEachRoleSpent(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	chunks := chatChunks(t, s, api.ChatRequest{
		Model:    "council",
		Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
	})
	last := chunks[len(chunks)-1]
	got := map[string]api.CouncilUsage{}
	for _, u := range last.CouncilUsage {
		got[u.Role] = u
	}
	// The fake engine answers every call with 10 prompt and 5 eval tokens;
	// the route decision is the planner's call too.
	for role, calls := range map[string]int{"planner": 2, "builder": 1, "researcher": 2, "critic": 2, "synthesizer": 1} {
		u := got[role]
		if u.Calls != calls || u.PromptTokens != 10*calls || u.EvalTokens != 5*calls || u.Wall <= 0 {
			t.Errorf("%s: %+v, want %d calls of 10 prompt and 5 eval tokens", role, u, calls)
		}
	}
	if len(got) != 5 {
		t.Errorf("roles %+v", last.CouncilUsage)
	}
	for _, c := range chunks[:len(chunks)-1] {
		if c.CouncilUsage != nil {
			t.Fatalf("usage on a chunk before the last: %+v", c)
		}
	}
}

// A cached part of the prompt was still sent: it counts in the prompt, and
// apart as cached.
func TestAUsageBookCountsTheCachedPromptAsSent(t *testing.T) {
	var b usageBook
	b.add(council.Request{Role: council.Researcher}, api.Metrics{PromptEvalCount: 30, EvalCount: 7}, 70, 1)
	b.add(council.Request{Role: council.Researcher}, api.Metrics{PromptEvalCount: 5, EvalCount: 3}, 95, 1)
	u := b.take()
	if len(u) != 1 || u[0].Calls != 2 || u[0].PromptTokens != 200 || u[0].CachedTokens != 165 || u[0].EvalTokens != 10 {
		t.Fatalf("usage %+v", u)
	}
	if again := b.take(); again != nil {
		t.Errorf("a second take reported %+v again", again)
	}
}
