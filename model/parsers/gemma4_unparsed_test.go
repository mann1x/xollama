package parsers

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// A gemma4 tool call the parser cannot read must not vanish.
//
// Captured live on 2026-09-21 from a v9-agentic run: the model closed
// `new_text` with a backtick instead of the `<|"|>` marker, so the scanner ran
// on to `path`'s opening marker and the arguments no longer parsed. The parser
// logged a warning and emitted nothing at all -- no tool call and no content --
// so the turn reached the client empty and nothing downstream could say why.
//
// Deliberately not repaired. The same run shows why: the other instance had
// `new_text` full of `text=text=text=` degeneration, and a repair that made
// that call well-formed would have written the degenerate fragment into the
// user's file. A call the parser cannot read is a call that must not run.
func TestGemma4UnreadableToolCallBecomesContent(t *testing.T) {
	tools := []api.Tool{{
		Type:     "function",
		Function: api.ToolFunction{Name: "editor", Description: "edit a file"},
	}}

	const swallowed = `call:editor{end_line:<|"|>159<|"|>,new_text:<|"|>` +
		"</div><script>\n// ... [I should have read more precisely]...\n`" +
		`,path:<|"|>manic_miner.html<|"|>}`

	for _, tc := range []struct {
		name    string
		payload string
		done    bool
	}{
		{"closed tool call", "<|tool_call>" + swallowed + "<tool_call|>", true},
		{"flushed without a closing tag", "<|tool_call>" + swallowed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Gemma4Parser{}
			p.Init(tools, nil, nil)

			content, _, calls, err := p.Add(tc.payload, tc.done)
			if err != nil {
				t.Fatalf("Add returned an error: %v", err)
			}
			if len(calls) != 0 {
				t.Fatalf("an unreadable tool call must not be run, got %d call(s)", len(calls))
			}
			if !strings.Contains(content, "call:editor") {
				t.Fatalf("the unreadable call was dropped instead of surfaced as content; content = %q", content)
			}
		})
	}
}
