package council

import (
	"context"
	"testing"

	"github.com/ollama/ollama/api"
)

// planStub answers the planner's first call with nothing, as the reasoning
// that took the whole reply on 20260930-085958 left it.
type planStub struct{ reqs []Request }

func (s *planStub) Stream(_ context.Context, req Request, _ func(string)) (string, error) {
	s.reqs = append(s.reqs, req)
	if len(s.reqs) == 1 {
		return "", nil
	}
	return `{"plan":"read the file","briefs":["the top half"]}`, nil
}

// An empty plan is asked for once more without thinking, and a brief the plan
// left out is never the same for two researchers.
func TestAnEmptyPlanIsAskedAgainWithoutThinking(t *testing.T) {
	s := &planStub{}
	cfg := FromModel(nil, 0.7)
	cfg.Researchers = 3
	cfg.Think = map[Role]string{Planner: "medium"}
	p, err := MakePlan(t.Context(), s, cfg, Draws{}, []api.Message{{Role: "user", Content: "fix it"}}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.reqs) != 2 || s.reqs[0].Think != "medium" || s.reqs[1].Think != "" {
		t.Fatalf("asked %d times: %+v", len(s.reqs), s.reqs)
	}
	if p.Plan != "read the file" || len(p.Briefs) != 3 || p.Briefs[1] == p.Briefs[2] {
		t.Fatalf("%+v", p)
	}
}
