package council

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// The runtime keeps the list's rules, whatever the planner writes.
func TestTheTaskListKeepsItsRules(t *testing.T) {
	prev := []Task{
		{ID: 1, Task: "read the parser", Status: TaskAssigned, Researcher: 1},
		{ID: 2, Task: "the brace theory", Status: TaskRefuted, Outcome: "the check did not move"},
		{ID: 3, Task: "the loader", Status: TaskOpen},
	}
	next := []Task{
		{ID: 1, Task: "read the parser", Status: TaskDone},                     // closed without an outcome
		{ID: 2, Task: "the brace theory", Status: TaskAssigned, Researcher: 2}, // a refuted task again
		{ID: 0, Task: "rewrite the draw loop", Status: TaskAssigned, Researcher: 7},
		{ID: 9, Task: "", Status: TaskOpen}, // nothing to do
		// task 3 left out
	}
	got := mergeTasks(prev, next, 2)
	want := []Task{
		{ID: 1, Task: "read the parser", Status: TaskOpen},
		{ID: 2, Task: "the brace theory", Status: TaskRefuted, Outcome: "the check did not move"},
		{ID: 3, Task: "the loader", Status: TaskOpen},
		{ID: 4, Task: "rewrite the draw loop", Status: TaskOpen},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("list\n%+v\nwant\n%+v", got, want)
	}
	got = mergeTasks(got, []Task{{ID: 1, Status: TaskDone, Outcome: "the check passed", Researcher: 1}}, 2)
	if got[0].Status != TaskDone || got[0].Task != "read the parser" || got[0].Researcher != 0 {
		t.Errorf("a task closed with its outcome: %+v", got[0])
	}
}

// The planner keeps the list across cycles and turns: every member reads it,
// the re-plan updates it, the next turn's planner starts from it, and new work
// rebuilt starts without it.
func TestThePlannerKeepsTheTaskList(t *testing.T) {
	s := &retestStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, fails: 1}
	res, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept == nil || len(res.Kept.Tasks) != 2 || res.Kept.Tasks[0].Researcher != 1 {
		t.Fatalf("kept %+v", res.Kept)
	}
	var replan Request
	for _, c := range s.calls {
		if c.Role == Planner && c.Round > 0 {
			replan = c
		}
	}
	if got := all(replan); !strings.Contains(got, header(tasksSource)+"- #1 [assigned, researcher 1] a") || !strings.Contains(got, ledgerRules) {
		t.Errorf("the re-plan did not read the list and its rules:\n%s", got)
	}

	cfg := toolCfg()
	cfg.Previous = res.Kept
	s = &retestStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}}
	if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := all(lastOf(s.calls, Researcher)); !strings.Contains(got, header(tasksSource)) {
		t.Error("the next turn's council did not read the list it carried")
	}

	cfg = toolCfg()
	cfg.Previous = res.Kept
	s = &retestStub{toolStub: toolStub{stub: stub{route: `{"route":"rebuild"}`}}}
	if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := all(lastOf(s.calls, Researcher)); strings.Contains(got, "] a\n") {
		t.Errorf("a rebuilt council read the old work's list:\n%s", got)
	}

	cfg = frontCfg()
	cfg.Previous = res.Kept
	f := &frontStub{toolStub{stub: stub{route: `{"route":"rebuild"}`, build: codingBuild}}}
	if _, err := Run(t.Context(), cfg, f, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := all(lastOf(f.calls, Researcher)); strings.Contains(got, header(tasksSource)+"- #1") && strings.Contains(got, "] a\n") {
		t.Errorf("new work read the old work's list:\n%s", got)
	}
}

// checkStub's synthesizer runs one check per cycle and reports it failed.
type checkStub struct{ toolStub }

func (s *checkStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	if req.Role != Synthesizer {
		return s.toolStub.StreamTools(ctx, req, onToken)
	}
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	for _, m := range req.Messages[len(req.Messages)-1:] {
		if m.Role == "tool" {
			return Reply{Content: fmt.Sprintf("still failing\n\n%s the check failed.", Retest)}, nil
		}
	}
	return Reply{Calls: []api.ToolCall{readCall("", "check")}}, nil
}

func driveResults(t *testing.T, cfg Config, m Model, result func(trip int) string) Result {
	t.Helper()
	res, err := Run(t.Context(), cfg, m, conv, func(Event) {})
	for trip := 0; err == nil && len(res.Calls) > 0; trip++ {
		cfg.Results = map[string]string{}
		for _, c := range res.Calls {
			cfg.Results[c.ID] = result(trip)
		}
		res, err = RunFrom(t.Context(), cfg, m, conv, res.Progress, nil, func(Event) {})
	}
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Checks whose output does not move turn the council to another approach, in
// words that name no topic; an output that moves is a lead to follow.
func TestACouncilThatDoesNotMoveTheCheckChangesApproach(t *testing.T) {
	build := `{"target":"t","planner":"","researcher":"","critic":"","synthesizer":"","max_tests":3}`
	s := &checkStub{toolStub{stub: stub{route: `{"route":"council"}`, build: build}}}
	driveResults(t, toolCfg(), s, func(int) string { return "SyntaxError: missing ) after argument list" })
	var last Request
	for _, c := range s.calls {
		if c.Role == Planner && c.Round == 3 {
			last = c
		}
	}
	got := all(last)
	if !strings.Contains(got, sameNote) || !strings.Contains(got, fmt.Sprintf(stuckNote, 3)) {
		t.Fatalf("the council was not told the checks stopped moving:\n%s", got)
	}
	for _, topic := range []string{"code", "syntax", "brace", "file", "error"} {
		if strings.Contains(strings.ToLower(stuckNote+sameNote+movedNote), topic) {
			t.Errorf("the nudge names a topic: %q", topic)
		}
	}

	s = &checkStub{toolStub{stub: stub{route: `{"route":"council"}`, build: build}}}
	driveResults(t, toolCfg(), s, func(trip int) string { return fmt.Sprintf("failure %d", trip) })
	for _, c := range s.calls {
		if c.Role == Planner && c.Round == 3 {
			got = all(c)
		}
	}
	if !strings.Contains(got, movedNote) || strings.Contains(got, "checks returned the same output") {
		t.Errorf("moving checks were not taken as progress:\n%s", got)
	}
}

// A review without its verdict is sent back with the structure, once; a
// second miss is marked unclear.
func TestAReviewWithoutAVerdictIsSentBack(t *testing.T) {
	for name, tc := range map[string]struct {
		replies []string
		want    string
	}{
		"fixed on the second try": {[]string{"looks fine to me", "CHANGE: x\nCHECK: y\n" + ReviewConfirmed}, ReviewConfirmed},
		"never gives one":         {[]string{"looks fine", "still fine"}, ReviewUnclear + " (the critic gave no verdict)"},
		"right the first time":    {[]string{"CHANGE: x\nCHECK: y\n" + ReviewRefuted}, ReviewRefuted},
	} {
		t.Run(name, func(t *testing.T) {
			var asked []Request
			m := modelFunc(func(ctx context.Context, req Request, _ func(string)) (string, error) {
				asked = append(asked, req)
				return tc.replies[min(len(asked)-1, len(tc.replies)-1)], nil
			})
			d := NewDesk(t.Context(), m, toolCfg(), 1)
			d.Submit(ReviewJob{ID: "a", Turn: "t", N: 1})
			d.Wait(t.Context(), 0)
			for d.Out() > 0 {
				d.Wait(t.Context(), 1e9)
			}
			rs := d.Take("t")
			if len(rs) != 1 || !strings.HasSuffix(rs[0].Text, tc.want) {
				t.Fatalf("review %+v, want it to end %q", rs, tc.want)
			}
			if len(tc.replies) > 1 && (len(asked) != 2 || asked[1].Messages[len(asked[1].Messages)-1].Content != user(reviewFormatNudge).Content) {
				t.Errorf("the review was not sent back with the structure")
			}
			if !strings.Contains(all(asked[0]), "CHANGE: <") {
				t.Error("the reviewer was not given the structure up front")
			}
		})
	}
}

// The builder tells the planner to keep the council's record.
func TestTheBuilderMakesThePlannerTheCoordinator(t *testing.T) {
	for _, want := range []string{"coordinator", "task list", "what evidence closes a task", "reads the task list and every failed check"} {
		if !strings.Contains(builderPrompt, want) {
			t.Errorf("the builder's instruction lacks %q", want)
		}
	}
}
