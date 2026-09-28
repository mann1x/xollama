package council

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

func readCall(id, path string) api.ToolCall {
	args := api.NewToolCallFunctionArguments()
	args.Set("path", path)
	return api.ToolCall{ID: id, Function: api.ToolCallFunction{Name: "read_files", Arguments: args}}
}

// An earlier council turn reads, for every member, as who called what and
// what came back: one message per member, without its working notes, and a
// long result it has already seen pointed at.
func TestEarlierTurnsShowWhichMemberCalled(t *testing.T) {
	big := strings.Repeat("x", repeatAt)
	in := []api.Message{
		{Role: "system"},
		{Role: "user", Content: "fix it"},
		{Role: "assistant", Content: "I bet it is the template literals.", Thinking: "musing", ToolCalls: []api.ToolCall{readCall("r1.2:a", "a.go"), readCall("r2.2:b", "b.go")}},
		{Role: "tool", ToolCallID: "r2.2:b", Content: "B"},
		{Role: "tool", ToolCallID: "r1.2:a", Content: big},
		{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{readCall("s:c", "a.go")}},
		{Role: "tool", Content: big}, // no id: matched by order
		{Role: "assistant", Content: "Fixed."},
		{Role: "assistant", Content: "plain", ToolCalls: []api.ToolCall{readCall("call_9", "z.go")}},
		{Role: "tool", ToolCallID: "call_9", Content: big},
		{Role: "user", Content: "still broken"},
	}
	out := History(in)
	var got []string
	for _, m := range out {
		got = append(got, m.Role+"|"+m.Content)
	}
	want := []string{
		"system|",
		"user|fix it",
		"assistant|" + header("RESEARCHER 1, ROUND 2 · TOOL CALLS"),
		"tool|" + big,
		"assistant|" + header("RESEARCHER 2, ROUND 2 · TOOL CALLS"),
		"tool|B",
		"assistant|" + header("SYNTHESIZER · TOOL CALLS"),
		`tool|(the same 1000 characters as the earlier read_files {"path":"a.go"} for researcher 1, round 2 returned: unchanged since)`,
		"assistant|Fixed.",
		"assistant|plain", // a plain model's turn is its own
		"tool|" + big,
		"user|still broken",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("history:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if out[2].Thinking != "" || len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != "r1.2:a" {
		t.Errorf("researcher 1's message %+v", out[2])
	}
	// The same on every turn: the members' shared prefix holds.
	if again := History(in); len(again) != len(out) || again[7].Content != out[7].Content {
		t.Error("the rewrite is not stable")
	}
}

// Every message the council adds names its source; the user's do not.
func TestEveryCouncilMessageNamesItsSource(t *testing.T) {
	s := &stub{route: `{"route":"council"}`}
	if _, err := Run(t.Context(), FromModel(nil, 0.7), s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	syn := s.calls[len(s.calls)-1]
	if syn.Role != Synthesizer {
		t.Fatalf("last call %s", syn.Role)
	}
	var sources []string
	for _, m := range syn.Messages[len(conv):] {
		i := strings.Index(m.Content, "]\n")
		if !strings.HasPrefix(m.Content, sourceOpen) || i < 0 {
			t.Fatalf("a council message without a source: %q", m.Content)
		}
		sources = append(sources, m.Content[len(sourceOpen):i])
	}
	if got := strings.Join(sources, " / "); got != strings.Join([]string{instructionsSource, planSource, findingsSource, critiqueSource, instructionsSource}, " / ") {
		t.Errorf("sources %s", got)
	}
	if !strings.Contains(syn.Messages[len(conv)].Content, sourcesNote) {
		t.Error("the members are not told how to read the headers")
	}
	for _, m := range conv {
		if strings.HasPrefix(m.Content, sourceOpen) {
			t.Errorf("the user's message was headed: %q", m.Content)
		}
	}
}
