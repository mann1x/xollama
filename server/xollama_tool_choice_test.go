package server

import (
	"encoding/json"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/model/parsers"
	"github.com/ollama/ollama/tools"
)

func TestNoToolCallTag(t *testing.T) {
	editor := api.Tools{{Type: "function", Function: api.ToolFunction{Name: "editor"}}}
	none := json.RawMessage(`"none"`)
	gemma4 := parsers.ParserForName("gemma4")

	for _, tt := range []struct {
		name    string
		req     api.ChatRequest
		builtin parsers.Parser
		generic *tools.Parser
		want    string
	}{
		{"built-in parser's tag", api.ChatRequest{Tools: editor, ToolChoice: none}, gemma4, nil, "<|tool_call>"},
		{"the template's tag", api.ChatRequest{Tools: editor, ToolChoice: none}, nil, tools.NewParserWithTag(editor, "<tool_call>"), "<tool_call>"},
		{"anthropic spelling", api.ChatRequest{Tools: editor, ToolChoice: json.RawMessage(`{"type":"none"}`)}, gemma4, nil, "<|tool_call>"},
		{"calls allowed", api.ChatRequest{Tools: editor, ToolChoice: json.RawMessage(`"auto"`)}, gemma4, nil, ""},
		{"no tool_choice", api.ChatRequest{Tools: editor}, gemma4, nil, ""},
		{"no tools", api.ChatRequest{ToolChoice: none}, gemma4, nil, ""},
		{"no tag known", api.ChatRequest{Tools: editor, ToolChoice: none}, nil, nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := noToolCallTag(&tt.req, tt.builtin, tt.generic); got != tt.want {
				t.Fatalf("noToolCallTag = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForbiddenToolCallsAreDropped(t *testing.T) {
	calls := []api.ToolCall{{Function: api.ToolCallFunction{Name: "editor"}}}

	none := api.ChatRequest{ToolChoice: json.RawMessage(`"none"`)}
	if got := forbiddenToolCalls(&none, calls); got != nil {
		t.Fatalf("tool_choice none kept %v", got)
	}
	auto := api.ChatRequest{}
	if got := forbiddenToolCalls(&auto, calls); len(got) != 1 {
		t.Fatalf("calls allowed lost them: %v", got)
	}
}
