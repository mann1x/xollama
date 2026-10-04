package server

import (
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// Two researchers of one step attach to one layer, though each one's
// messages end with its own instruction and its mates' notes (broadcast).
// On 569747788's hard run the layer was cut before the notes, so it held each
// researcher's instruction and the step built one layer per researcher
// (pools 13 and 14).
func TestResearchersWithNotesShareTheirStage(t *testing.T) {
	kv := &fakeKV{}
	render := func(_ context.Context, msgs []api.Message) (string, error) {
		var b strings.Builder
		for _, m := range msgs {
			b.WriteString("<" + m.Role + ">" + m.Content + "\n")
		}
		return b.String(), nil
	}
	tr := &councilTree{kv: kv, owner: "conv-1", layers: map[string]*councilLayer{}, render: render}
	stage := []api.Message{
		{Role: "user", Content: "fix it"},
		{Role: "user", Content: "[COUNCIL · INSTRUCTIONS FOR YOU]\nROLE: PLANNER. Plan the work."},
		{Role: "assistant", Content: `[COUNCIL · THE PLANNER'S PLAN]` + "\n" + `{"plan":"look","briefs":["a","b"]}`},
	}
	member := func(i, note string) []api.Message {
		return append(append([]api.Message(nil), stage...),
			api.Message{Role: "user", Content: "[COUNCIL · INSTRUCTIONS FOR YOU]\nROLE: RESEARCHER " + i + ". Your brief: " + i},
			api.Message{Role: "user", Content: "[COUNCIL · NOTES FROM YOUR MATES]\n- " + note})
	}
	p1 := tr.workerPlacement(t.Context(), member("1", "r2: line 3"), "conv-1~researcher-1")
	p2 := tr.workerPlacement(t.Context(), member("2", "r1: line 9"), "conv-1~researcher-2")
	if p1 == nil || p2 == nil || p1.PoolID == nil || p2.PoolID == nil {
		t.Fatalf("placements %+v %+v: both researchers must be pooled", p1, p2)
	}
	pools, _ := kv.snapshot()
	// The conversation's own layer, then the stage both attach to.
	if *p1.PoolID != *p2.PoolID || len(pools) != 2 || *p1.PoolID != pools[1].id {
		t.Fatalf("pools %d and %d, %d built: want one shared stage on the conversation", *p1.PoolID, *p2.PoolID, len(pools))
	}
	for _, p := range pools {
		if strings.Contains(p.text, "ROLE: RESEARCHER") || strings.Contains(p.text, "NOTES FROM YOUR MATES") {
			t.Errorf("pool %d holds a member's own part: %q", p.id, p.text)
		}
	}
}
