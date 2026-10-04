package council

import (
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// frontStub's front answers, forwards or rebuilds then forwards, by route.
type frontStub struct {
	toolStub
}

func (s *frontStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	if req.Role != Front {
		return s.toolStub.StreamTools(ctx, req, onToken)
	}
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	last := req.Messages[len(req.Messages)-1]
	call := func(name string) (Reply, error) {
		return Reply{Content: "handing over", Calls: []api.ToolCall{{Function: api.ToolCallFunction{Name: name, Arguments: api.NewToolCallFunctionArguments()}}}}, nil
	}
	switch {
	case strings.Contains(s.route, "direct"):
		onToken("front answers")
		return Reply{Content: "front answers"}, nil
	case strings.Contains(s.route, "rebuild") && last.Role != "tool":
		return call(RebuildTool)
	}
	return call(ForwardTool)
}

func frontCfg() Config {
	cfg := toolCfg()
	cfg.Tools = WithRouting(cfg.Tools)
	return cfg
}

// On a tool turn the synthesizer takes the request: a quick one is its answer
// in one call, with no route decision; one it forwards is built for and
// answered by the council.
func TestTheSynthesizerTakesTheRequestFirst(t *testing.T) {
	s := &frontStub{toolStub{stub: stub{route: `{"route":"direct"}`}}}
	res, err := Run(t.Context(), frontCfg(), s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "front answers" || res.Route != "direct" || len(s.calls) != 1 || s.calls[0].Role != Front {
		t.Fatalf("direct: answer %q route %q calls %d", res.Answer, res.Route, len(s.calls))
	}
	if last := s.calls[0].Messages[len(s.calls[0].Messages)-1].Content; !strings.Contains(last, frontMsg) || !strings.Contains(last, frontNoBuild) {
		t.Errorf("the front's instruction %q", last)
	}

	s = &frontStub{toolStub{stub: stub{route: `{"route":"council"}`}}}
	res, err = Run(t.Context(), frontCfg(), s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" || res.Route != RouteCouncil || s.count(Builder) != 1 || s.count(Synthesizer) != 1 {
		t.Fatalf("forwarded: answer %q route %q, %d builders", res.Answer, res.Route, s.count(Builder))
	}
	for _, c := range s.calls {
		if c.Format != nil && strings.Contains(string(c.Format), "route") {
			t.Error("a forwarded request was routed again by the planner")
		}
	}
	if res.Kept == nil || res.Kept.Build == nil || res.Kept.Build.Target != "t" {
		t.Errorf("kept %+v", res.Kept)
	}
}

// With a council built, the front reads its target; new work it does not fit
// is rebuilt, and the front is told the new setup before it forwards.
func TestTheSynthesizerRebuildsTheCouncilForNewWork(t *testing.T) {
	cfg := frontCfg()
	cfg.Previous = &Progress{Route: RouteCouncil, Build: &Build{Target: "Proof-reading an essay.", MaxTests: 1}}
	s := &frontStub{toolStub{stub: stub{route: `{"route":"rebuild"}`, build: codingBuild}}}
	res, err := Run(t.Context(), cfg, s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if s.count(Builder) != 1 || res.Kept == nil || res.Kept.Build.Target != "Fixing a bug in a game." {
		t.Fatalf("%d builders, kept %+v", s.count(Builder), res.Kept)
	}
	var fronts []Request
	for _, c := range s.calls {
		if c.Role == Front {
			fronts = append(fronts, c)
		}
	}
	if len(fronts) != 2 {
		t.Fatalf("%d front calls, want 2", len(fronts))
	}
	first := fronts[0].Messages[len(fronts[0].Messages)-1].Content
	if !strings.Contains(first, "The council is set up for: Proof-reading an essay.") || !strings.Contains(first, "council_rebuild") {
		t.Errorf("the front was not given the target to judge: %q", first)
	}
	told := fronts[1].Messages[len(fronts[1].Messages)-1]
	if told.Role != "tool" || !strings.Contains(told.Content, "set up again for: Fixing a bug in a game.") || !strings.Contains(told.Content, "one edit at a time") {
		t.Errorf("the front was not told the new setup: %+v", told)
	}
	for _, c := range s.calls {
		if c.Role == Researcher && !strings.Contains(c.Messages[len(c.Messages)-1].Content, "read before concluding") {
			t.Error("a researcher ran without the new build's instruction")
		}
	}
}

// Only the front routes: a member already working on the request is refused.
func TestOnlyTheFrontRoutes(t *testing.T) {
	cfg := frontCfg()
	for _, name := range []string{ForwardTool, RebuildTool} {
		c := api.ToolCall{Function: api.ToolCallFunction{Name: name}}
		for _, r := range []Role{Planner, Researcher, Critic, Synthesizer} {
			if ok, _ := cfg.may(r, c); ok {
				t.Errorf("%s may call %s", r, name)
			}
		}
		if ok, why := cfg.may(Front, c); !ok {
			t.Errorf("the front may not call %s: %s", name, why)
		}
	}
	// Nor are they tools a researcher is told it may read with, or reasons
	// to propose a change.
	if note := cfg.toolNote(Researcher); strings.Contains(note, ForwardTool) {
		t.Errorf("researcher note %q", note)
	}
}

// A front that changed something forwards its attempt from the first change
// on; of its reads only those after its last change are current research.
func TestAFrontsReadsAreResearchAndItsChangesAnAttempt(t *testing.T) {
	cfg := frontCfg()
	key := MemberKey(Front, 0, 0)
	write := api.ToolCall{ID: "w", Function: api.ToolCallFunction{Name: "write_file", Arguments: api.NewToolCallFunctionArguments()}}
	turns := []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{readCall("r0", "before.go")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{write}},
		{Role: "assistant", ToolCalls: []api.ToolCall{readCall("r2", "after.go")}},
	}
	cfg.Results = map[string]string{
		ForwardedID(key, "r0"): "old text", ForwardedID(key, "w"): "written", ForwardedID(key, "r2"): "new text",
	}
	report, read := cfg.frontReport(turns), cfg.frontRead(turns)
	if !strings.Contains(report, "write_file") || !strings.Contains(report, "after.go") || strings.Contains(report, "before.go") {
		t.Errorf("the attempt is not the change and what followed it:\n%s", report)
	}
	if !strings.Contains(read, "after.go") || strings.Contains(read, "before.go") || strings.Contains(read, "write_file") {
		t.Errorf("the reads are not the current ones:\n%s", read)
	}
	if got := cfg.frontReport(turns[:1]); got != "" {
		t.Errorf("a front that only read made an attempt: %q", got)
	}
}
