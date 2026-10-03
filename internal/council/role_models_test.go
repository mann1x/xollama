package council

import (
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// allOn seats every role on model.
func allOn(cfg Config, model string) Config {
	for _, r := range []Role{Planner, Researcher, Critic, Synthesizer, Builder} {
		cfg.Models[r] = model
		cfg.MaxTokens[r] = 131072
	}
	return cfg
}

// With every role on another model, no call of the council runs on the
// council's own: not the route decision nor the direct answer, which are the
// planner's, and not the front, which is the synthesizer taking a tool turn's
// request first (the first all-glm smoke run answered both trips on the lead).
func TestEveryCallRunsOnItsRolesModel(t *testing.T) {
	glm := "glm-5.3-flash:cloud"
	for _, tc := range []struct {
		name  string
		cfg   Config
		model Model
	}{
		{"direct", allOn(FromModel(&xollama.Council{}, 0.7), glm), &stub{route: `{"route":"direct"}`}},
		{"council", allOn(FromModel(&xollama.Council{}, 0.7), glm), &stub{route: `{"route":"council"}`}},
		{"front answers", allOn(frontCfg(), glm), &frontStub{toolStub{stub: stub{route: `{"route":"direct"}`}}}},
		{"front forwards", allOn(frontCfg(), glm), &frontStub{toolStub{stub: stub{route: `{"route":"council"}`}}}},
	} {
		if _, err := Run(t.Context(), tc.cfg, tc.model, conv, func(Event) {}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var calls []Request
		switch s := tc.model.(type) {
		case *stub:
			calls = s.calls
		case *frontStub:
			calls = s.calls
		}
		if len(calls) == 0 {
			t.Fatalf("%s: no calls", tc.name)
		}
		for _, c := range calls {
			if c.Model != glm {
				t.Errorf("%s: the %s call ran on %q, want %s", tc.name, c.Role, c.Model, glm)
			}
			if c.Role == Front && c.MaxTokens != 131072 {
				t.Errorf("%s: the front's cap %d, want the synthesizer's 131072", tc.name, c.MaxTokens)
			}
		}
	}
}

// On the council's own model the direct answer keeps the synthesizer's cap,
// as it always had; with the planner elsewhere it is the planner's own.
func TestTheDirectAnswerTakesItsModelsCap(t *testing.T) {
	cfg := FromModel(&xollama.Council{Synthesizer: &xollama.CouncilRole{MaxTokens: 3000}}, 0.7)
	if got := directTok(cfg); got != 3000 {
		t.Errorf("planner on the council's model: %d, want the synthesizer's 3000", got)
	}
	cfg.Models[Planner], cfg.MaxTokens[Planner] = "glm-5.3-flash:cloud", 131072
	if got := directTok(cfg); got != 131072 {
		t.Errorf("planner on glm: %d, want its own 131072", got)
	}
}
