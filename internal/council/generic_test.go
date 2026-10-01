package council

import (
	"testing"

	"github.com/ollama/ollama/api"
)

func namedTools(names ...string) api.Tools {
	var ts api.Tools
	for _, n := range names {
		ts = append(ts, api.Tool{Type: "function", Function: api.ToolFunction{Name: n}})
	}
	return ts
}

// A client that marks no tool read-only has the readers told apart by name;
// one that marks any is taken at its word.
func TestReadOnlyIsInferredOnlyWhenTheClientMarksNone(t *testing.T) {
	got := InferReadOnly(namedTools("read_files", "listFiles", "grep", "search_codebase", "fetch_web_content", "run_commands", "editor", "write_file", "read_and_write", "check_file", "lint", "run_check", "browser"))
	want := map[string]bool{"read_files": true, "listFiles": true, "grep": true, "search_codebase": true, "fetch_web_content": true, "check_file": true, "lint": true}
	for _, tl := range got {
		if tl.Function.ReadOnly != want[tl.Function.Name] {
			t.Errorf("%s: read-only %v, want %v", tl.Function.Name, tl.Function.ReadOnly, want[tl.Function.Name])
		}
	}
	marked := namedTools("read_files", "grep")
	marked[0].Function.ReadOnly = true
	if got := InferReadOnly(marked); got[1].Function.ReadOnly {
		t.Error("a client that marked its readers had another one inferred")
	}
}

// With no check stated, the check is the call the member sends again,
// unchanged, after another change: an edit differs each time, a run does not.
func TestTheCheckIsTheCallRepeatedAcrossChanges(t *testing.T) {
	cfg := toolCfg()
	cfg.Tools = append(cfg.Tools, shellTool)
	edit := func(s string) api.Message {
		return api.Message{Role: "assistant", ToolCalls: []api.ToolCall{tcall("write_file", map[string]any{"content": s})}}
	}
	read := api.Message{Role: "assistant", ToolCalls: []api.ToolCall{tcall("read_files", map[string]any{"path": "a"})}}
	run := api.Message{Role: "assistant", ToolCalls: []api.ToolCall{checkRun()}}
	if c := cfg.inferredCheck([]api.Message{edit("1"), run, read, run}); c != nil {
		t.Errorf("a run repeated with no change between is not yet a check: %+v", c)
	}
	c := cfg.inferredCheck([]api.Message{edit("1"), run, edit("2"), run})
	if c == nil || readKey(*c) != readKey(checkRun()) {
		t.Fatalf("the run repeated across an edit: %+v", c)
	}
	cfg.CheckCall = c
	if cfg.uncheckedWrite([]api.Message{edit("1"), run, edit("2"), run}) || !cfg.uncheckedWrite([]api.Message{edit("1"), run, edit("2"), run, edit("3")}) {
		t.Error("with the check inferred, only a change after its last run is unchecked")
	}
}
