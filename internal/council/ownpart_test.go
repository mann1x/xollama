package council

import (
	"testing"

	"github.com/ollama/ollama/api"
)

// A member's own part starts at its instruction after the plan, whatever
// follows it: its mates' notes, the user's system prompt, its tool turns and
// the council's nudges. Before 569747788's hard run it was the last user
// message, and each researcher's layer held its own instruction.
func TestOwnPartIsTheMembersInstruction(t *testing.T) {
	conv := []api.Message{{Role: "user", Content: "fix it"}}
	cfg := Config{Researchers: 2}
	p := Plan{Plan: "look", Briefs: []string{"a", "b"}}
	stage := planned(cfg, conv, p)
	role := user("ROLE: RESEARCHER 1. Your brief: a")
	notes := sourced(notesSource, "- r2: it is line 3")
	for _, tc := range []struct {
		name string
		msgs []api.Message
		want int
	}{
		{"alone", append(clone(stage), role), len(stage)},
		{"notes after", append(clone(stage), role, notes), len(stage)},
		{"system after", append(clone(stage), sourced(critiqueSource, "c"), role, sourced(systemSource, "be brief")), len(stage) + 1},
		{"tool turns and a nudge", append(clone(stage), role, api.Message{Role: "assistant", Content: "reading"}, api.Message{Role: "tool", Content: "x"}, user(narratedNudge)), len(stage)},
		{"no plan", append(clone(conv), user("ROLE: PLANNER. decide")), -1},
		{"plan, no instruction", clone(stage), -1},
	} {
		if got := OwnPart(tc.msgs); got != tc.want {
			t.Errorf("%s: OwnPart = %d, want %d", tc.name, got, tc.want)
		}
	}
	// A re-plan's own planner instruction comes before the plan it answered.
	replan := append(append(clone(stage), user("ROLE: PLANNER. again")), planReply(p), role, notes)
	if got := OwnPart(replan); got != len(replan)-2 {
		t.Errorf("after a re-plan: OwnPart = %d, want %d", got, len(replan)-2)
	}
}
