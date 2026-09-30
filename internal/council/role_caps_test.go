package council

import (
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// A role's reply cap follows the model its request runs on. On the council's
// own model an unstated cap is that model's num_predict, else the role's
// built-in one, because the owner's window is booked for it. On another model
// it is 0: the council sends none and that model's own template decides.
func TestAReplyCapFollowsTheModelItRunsOn(t *testing.T) {
	glm := "glm-5.3-flash:cloud"
	c := &xollama.Council{
		Researcher:  &xollama.CouncilRole{Model: glm},
		Critic:      &xollama.CouncilRole{Model: glm, MaxTokens: 131072},
		Synthesizer: &xollama.CouncilRole{Model: glm, MaxTokens: 131072},
		Planner:     &xollama.CouncilRole{MaxTokens: 4096},
	}
	cfg := FromModel(c, 0.7)
	for _, tc := range []struct {
		name        string
		role        Role
		model, host string
		want        int
	}{
		{"a stated cap on the role's own model", Planner, "", "", 4096},
		{"a stated cap on another model", Critic, glm, "", 131072},
		{"an unstated cap on another model", Researcher, glm, "", 0},
		{"the front on the council's model borrows no cloud cap", Synthesizer, "", "", xollama.DefaultCouncilMaxTokens},
		{"an unstated builder on the planner's model", Builder, "", "", xollama.DefaultCouncilMaxTokens},
	} {
		if got := maxTok(cfg, tc.role, tc.model, tc.host); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	cfg.LeadMaxTokens = 8192
	if got := maxTok(cfg, Critic, "", ""); got != 8192 {
		t.Errorf("an unstated cap on the council's model with num_predict 8192: %d, want 8192", got)
	}
	if got := maxTok(cfg, Researcher, glm, ""); got != 0 {
		t.Errorf("the council's num_predict reached a role on another model: %d, want 0", got)
	}
}

// OnLead and Cap answer for a role's own model, the builder's falling back to
// the planner's and a reviewer's being a critic's.
func TestARoleIsOnTheLeadOnlyWithoutAModelOfItsOwn(t *testing.T) {
	glm := "glm-5.3-flash:cloud"
	cfg := FromModel(&xollama.Council{
		Planner: &xollama.CouncilRole{Model: glm, MaxTokens: 131072, NumCtx: 262144},
		Critic:  &xollama.CouncilRole{Model: glm},
	}, 0.7)
	for _, tc := range []struct {
		role Role
		lead bool
		cap  int
		ctx  int
	}{
		{Planner, false, 131072, 262144},
		{Builder, false, 0, 262144}, // on the planner's model, with no cap of its own
		{Researcher, true, xollama.DefaultCouncilMaxTokens, 0},
		{Critic, false, 0, 0},
		{Reviewer, false, 0, 0},
		{Synthesizer, true, xollama.DefaultCouncilMaxTokens, 0},
	} {
		if got := cfg.OnLead(tc.role); got != tc.lead {
			t.Errorf("%s: on the lead %v, want %v", tc.role, got, tc.lead)
		}
		if got := cfg.Cap(tc.role); got != tc.cap {
			t.Errorf("%s: cap %d, want %d", tc.role, got, tc.cap)
		}
		if got := numCtx(cfg, tc.role); got != tc.ctx {
			t.Errorf("%s: num_ctx %d, want %d", tc.role, got, tc.ctx)
		}
	}
}
