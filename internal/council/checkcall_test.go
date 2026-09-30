package council

import (
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
