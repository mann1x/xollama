package council

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// stepStub has the named roles call read_files on every request of the
// first cycle, and otherwise behaves as toolStub. A synthesizer that is not
// looping answers with verdict, or, while verdict is "", without one until
// it is nudged.
type stepStub struct {
	toolStub
	loop    map[Role]bool
	verdict string
}

func (s *stepStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	if !s.loop[req.Role] || req.Round > 0 {
		if req.Role != Synthesizer {
			return s.toolStub.StreamTools(ctx, req, onToken)
		}
		s.mu.Lock()
		s.calls = append(s.calls, req)
		s.mu.Unlock()
		out := "the fix is in"
		switch {
		case s.verdict != "":
			out += " " + s.verdict
		case req.Messages[len(req.Messages)-1].Content == user(verdictNudge).Content:
			out += " " + Done
		}
		onToken(out)
		return Reply{Content: out}, nil
	}
	s.mu.Lock()
	s.calls = append(s.calls, req)
	n := len(s.calls)
	s.mu.Unlock()
	args := api.NewToolCallFunctionArguments()
	args.Set("path", fmt.Sprintf("f%d.go", n))
	return Reply{Content: "I think it is in f.go", Calls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_files", Arguments: args}}}}, nil
}

// drive answers every call the turn sends out until it has an answer.
func drive(t *testing.T, cfg Config, m Model) Result {
	t.Helper()
	res, err := Run(t.Context(), cfg, m, conv, func(Event) {})
	for trip := 0; err == nil && len(res.Calls) > 0; trip++ {
		if trip > 40 {
			t.Fatal("the turn never ended")
		}
		cfg.Results = map[string]string{}
		for _, c := range res.Calls {
			cfg.Results[c.ID] = "DATA " + c.ID
		}
		res, err = RunFrom(t.Context(), cfg, m, conv, res.Progress, nil, func(Event) {})
	}
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func all(r Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		b.WriteString(m.Content + "\n")
	}
	return b.String()
}

func lastOf(calls []Request, role Role) Request {
	var out Request
	for _, c := range calls {
		if c.Role == role {
			out = c
		}
	}
	return out
}

// A synthesizer that ends a cycle without a verdict is asked for one, once.
func TestASynthesizerWithoutAVerdictIsAskedForOne(t *testing.T) {
	s := &stepStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}}
	res := drive(t, toolCfg(), s)
	if n := s.count(Synthesizer); n != 2 {
		t.Fatalf("%d synthesizer calls, want 2", n)
	}
	if last := lastOf(s.calls, Synthesizer); last.Messages[len(last.Messages)-1].Content != user(verdictNudge).Content {
		t.Errorf("the second call was not the nudge: %+v", last.Messages[len(last.Messages)-1])
	}
	if res.Answer != "the fix is in" {
		t.Errorf("answer %q", res.Answer)
	}

	// The user reads the reply once: the nudged one only adds the verdict.
	s = &stepStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}}
	var content strings.Builder
	if _, err := Run(t.Context(), toolCfg(), s, conv, func(e Event) {
		if e.Kind == Content {
			content.WriteString(e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if got := content.String(); got != "the fix is in" {
		t.Errorf("content %q", got)
	}
}

// A synthesizer that keeps investigating is told its budget at MaxSteps, and
// two steps later the cycle ends for it as a failed check that goes back to
// the council.
func TestASynthesizerThatKeepsInvestigatingRunsOutOfSteps(t *testing.T) {
	s := &stepStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, loop: map[Role]bool{Synthesizer: true}, verdict: Done}
	res := drive(t, toolCfg(), s)
	var first []Request
	for _, c := range s.calls {
		if c.Role == Synthesizer && c.Round == 0 {
			first = append(first, c)
		}
	}
	if len(first) != DefaultMaxSteps+2 {
		t.Fatalf("%d first-cycle synthesizer calls, want %d", len(first), DefaultMaxSteps+2)
	}
	for i, c := range first {
		if told := strings.Contains(all(c), budgetNote(DefaultMaxSteps)); told != (i >= DefaultMaxSteps) {
			t.Errorf("call %d: told the budget %v", i, told)
		}
	}
	if res.Answer != "the fix is in" || res.Kept == nil || len(res.Kept.Prior) != 1 || !strings.Contains(res.Kept.Prior[0], "tool steps ran out") {
		t.Fatalf("answer %q, kept %+v", res.Answer, res.Kept)
	}
	if r := lastOf(s.calls, Researcher); r.Round != 1 || !strings.Contains(all(r), "tool steps ran out") {
		t.Errorf("the next cycle's researchers did not read the spent cycle")
	}

	// The builder sets the budget.
	s = &stepStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`, build: `{"target":"t","planner":"","researcher":"","critic":"","synthesizer":"","max_steps":2}`}}, loop: map[Role]bool{Synthesizer: true}, verdict: Done}
	drive(t, toolCfg(), s)
	n := 0
	for _, c := range s.calls {
		if c.Role == Synthesizer && c.Round == 0 {
			n++
		}
	}
	if n != 4 {
		t.Errorf("max_steps 2: %d first-cycle synthesizer calls, want 4", n)
	}
}

// A front that investigates is told to forward after frontSteps, is forwarded
// two steps later, and the council reads what it tried as a failed check.
func TestAFrontThatInvestigatesIsForwardedWithItsAttempts(t *testing.T) {
	s := &stepStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, loop: map[Role]bool{Front: true}}
	cfg := frontCfg()
	res := drive(t, cfg, s)
	if n := s.count(Front); n != frontSteps+2 {
		t.Fatalf("%d front calls, want %d", n, frontSteps+2)
	}
	if !strings.Contains(all(lastOf(s.calls, Front)), frontBudgetNote) {
		t.Error("the front was not told to forward")
	}
	if res.Route != RouteCouncil || res.Kept == nil || len(res.Kept.Prior) != 1 {
		t.Fatalf("route %q, kept %+v", res.Route, res.Kept)
	}
	for _, role := range []Role{Planner, Researcher, Critic, Synthesizer} {
		got := all(lastOf(s.calls, role))
		if !strings.Contains(got, header(priorSource)+priorIntro) || !strings.Contains(got, "worked on the request itself") || !strings.Contains(got, `read_files {"path":"f`) {
			t.Errorf("%s did not read the front's attempts", role)
		}
		// Its calls and their results, never its theory.
		if strings.Contains(got, "I think it is in f.go") {
			t.Errorf("%s read the front's own conclusion", role)
		}
	}
}

// Failed checks outlive the turn while its work stands: the next turn's
// council reads them as earlier checks. New work, rebuilt, drops them.
func TestFailedChecksCarryToTheNextTurn(t *testing.T) {
	s := &retestStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, fails: 2}
	res, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept == nil || len(res.Kept.Prior) != 2 {
		t.Fatalf("kept %+v", res.Kept)
	}

	cfg := toolCfg()
	cfg.Previous = res.Kept
	s = &retestStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}}
	if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	got := all(lastOf(s.calls, Researcher))
	if !strings.Contains(got, priorIntro) || !strings.Contains(got, earlierMark+"Status 1") || !strings.Contains(got, "tried change 2") {
		t.Errorf("the next turn's researcher did not read the earlier checks:\n%s", got)
	}

	cfg = frontCfg()
	cfg.Previous = res.Kept
	f := &frontStub{toolStub{stub: stub{route: `{"route":"rebuild"}`, build: codingBuild}}}
	if _, err := Run(t.Context(), cfg, f, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := all(lastOf(f.calls, Researcher)); strings.Contains(got, "tried change") {
		t.Errorf("a rebuilt council read the old work's checks:\n%s", got)
	}
}

// A testing synthesizer makes the council's changes together and checks
// once, and fixes a next failure its check already shows: one change per
// check cost a whole cycle per fault on medium (1101 s against plain's 91).
func TestTheSynthesizerAppliesTheProposalsTogether(t *testing.T) {
	s := &retestStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}}
	if _, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	got := all(lastOf(s.calls, Synthesizer))
	for _, want := range []string{"in one reply -- several tool calls at once", "run the check, once", "fix that too and check again"} {
		if !strings.Contains(got, want) {
			t.Errorf("the synthesizer's instruction lacks %q", want)
		}
	}
	for _, p := range []string{got, builderPrompt} {
		if strings.Contains(p, "at a time") {
			t.Errorf("an instruction still asks for one change at a time")
		}
	}
	if !strings.Contains(builderPrompt, "report every fault found in its part") {
		t.Error("the builder's example does not ask researchers for every fault")
	}
}
