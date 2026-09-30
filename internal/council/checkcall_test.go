package council

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

var shellTool = api.Tool{Type: "function", Function: api.ToolFunction{Name: "run_commands"}}

// checkCfg is a council whose harness states its check as a shell call.
func checkCfg(t *testing.T) Config {
	t.Helper()
	cfg := reportCfg()
	cfg.Tools = append(cfg.Tools, shellTool)
	cfg.MaxTests = 2
	args := api.NewToolCallFunctionArguments()
	args.Set("commands", []any{"./run_game game.html"})
	cfg, err := cfg.Direct(&api.CouncilDirective{CheckCall: &api.CouncilCheckCall{Tool: "run_commands", Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func checkRun() api.ToolCall {
	return tcall("run_commands", map[string]any{"commands": []any{"./run_game game.html"}})
}

// A synthesizer that changed something and ends its turn without the stated
// check has the council make it, and goes on from its result.
func TestAWriteWithoutTheStatedCheckHasItMade(t *testing.T) {
	cfg := checkCfg(t)
	s := &scriptStub{replies: []Reply{
		{Calls: []api.ToolCall{tcall("write_file", map[string]any{"path": "game.js", "content": "fixed"})}},
		{Content: "Fixed the brace. " + Done},
		{Content: "The check passes. " + Done},
	}}
	out := resume(t, s, cfg, Request{Role: Synthesizer, Messages: []api.Message{{Role: "user", Content: "go"}}}, "ok: true")
	if len(s.reqs) != 3 {
		t.Fatalf("%d synthesizer calls, want 3: the write, the reply the check follows, the reply after it", len(s.reqs))
	}
	last := s.reqs[2].Messages
	var ran []api.ToolCall
	for _, m := range last {
		for _, c := range m.ToolCalls {
			if cfg.isCheck(c) {
				ran = append(ran, c)
			}
		}
	}
	if len(ran) != 1 || !slices.ContainsFunc(last, func(m api.Message) bool { return m.Role == "tool" && m.Content == "ok: true" }) {
		t.Errorf("the check was not made once with its result read back: %d runs in %+v", len(ran), last)
	}
	if !strings.Contains(out, "The check passes.") {
		t.Errorf("the answer is not the reply after the check: %q", out)
	}
}

// A turn that checked after its last change, or changed nothing, is not
// checked again; a shell call that is not the check is still a change.
func TestOnlyTheStatedCheckCountsAsOne(t *testing.T) {
	cfg := checkCfg(t)
	write := api.Message{Role: "assistant", ToolCalls: []api.ToolCall{tcall("write_file", map[string]any{"path": "a"})}}
	checked := api.Message{Role: "assistant", ToolCalls: []api.ToolCall{checkRun()}}
	other := api.Message{Role: "assistant", ToolCalls: []api.ToolCall{tcall("run_commands", map[string]any{"commands": []any{"rm a"}})}}
	for _, tc := range []struct {
		name  string
		turns []api.Message
		want  bool
	}{
		{"a change, unchecked", []api.Message{write}, true},
		{"a change, then the check", []api.Message{write, checked}, false},
		{"the check, then a change", []api.Message{checked, write}, true},
		{"another shell call after the check", []api.Message{write, checked, other}, true},
		{"nothing changed", nil, false},
	} {
		if got := cfg.uncheckedWrite(tc.turns); got != tc.want {
			t.Errorf("%s: unchecked %v, want %v", tc.name, got, tc.want)
		}
	}
	if !cfg.readOnly(checkRun()) || cfg.readOnly(tcall("run_commands", map[string]any{"commands": []any{"rm a"}})) {
		t.Error("the stated check should read as a check and any other shell call as a change")
	}
	if cfg := reportCfg(); cfg.uncheckedWrite([]api.Message{write}) {
		t.Error("with no stated check the council makes none")
	}
}

// A check_call names its tool, and one that disagrees with council.check is
// refused rather than half obeyed.
func TestACheckCallMustNameItsTool(t *testing.T) {
	for _, d := range []*api.CouncilDirective{
		{CheckCall: &api.CouncilCheckCall{}},
		{Check: "read_files", CheckCall: &api.CouncilCheckCall{Tool: "run_commands"}},
	} {
		if _, err := reportCfg().Direct(d); err == nil {
			t.Errorf("%+v was accepted", d)
		}
	}
}

// cycleStub edits and runs the check, in turn, for ever.
type cycleStub struct {
	scriptStub
	n int
}

func (s *cycleStub) StreamTools(_ context.Context, req Request, _ func(string)) (Reply, error) {
	s.reqs = append(s.reqs, req)
	s.n++
	if s.n%2 == 1 {
		return Reply{Calls: []api.ToolCall{tcall("write_file", map[string]any{"content": fmt.Sprint(s.n)})}}, nil
	}
	return Reply{Calls: []api.ToolCall{checkRun()}}, nil
}

// cycleSteps runs one synthesizer cycle, answering the check with out(i) on
// its i-th run, and returns how many steps the cycle took.
func cycleRun(t *testing.T, out func(i int) string) int {
	t.Helper()
	cfg := checkCfg(t)
	cfg.MaxSteps = 4
	s := &cycleStub{}
	req := Request{Role: Synthesizer, Messages: []api.Message{{Role: "user", Content: "go"}}}
	key := MemberKey(req.Role, req.Index, req.Round)
	var turns []api.Message
	runs := 0
	for range 100 {
		_, next, err := callTools(t.Context(), s, cfg, req, turns, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if next == nil {
			return toolSteps(turns)
		}
		for _, c := range next[len(next)-1].ToolCalls {
			res := "WROTE"
			if cfg.isCheck(c) {
				runs++
				res = out(runs)
			}
			next = append(next, api.Message{Role: "tool", ToolCallID: ForwardedID(key, c.ID), Content: res})
			cfg.Results[ForwardedID(key, c.ID)] = res
		}
		turns = next
	}
	t.Fatal("the cycle never ended")
	return 0
}

// While each fix changes what the check reports, the synthesizer keeps its
// cycle; a check that stops moving ends it at the step bound, and a moving one
// at the ceiling (native.sh run 0416: six cycles, one error each).
func TestACycleLastsWhileItsCheckMoves(t *testing.T) {
	stuck := cycleRun(t, func(int) string { return "Error: x is not defined" })
	moving := cycleRun(t, func(i int) string { return fmt.Sprintf("Error: fault %d", i) })
	if stuck > 4+2 {
		t.Errorf("a check that does not move kept the cycle for %d steps, want at most %d", stuck, 4+2)
	}
	if moving <= 4+2 || moving > maxCycleSteps*4 {
		t.Errorf("a moving check ended the cycle after %d steps, want more than %d and at most %d", moving, 4+2, maxCycleSteps*4)
	}
}
