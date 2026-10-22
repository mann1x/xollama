package server

import (
	"log/slog"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/model/parsers"
	"github.com/ollama/ollama/tools"
)

// xollama-hook: tool-choice — see docs/xollama/tool-choice.mdx
//
// tool_choice "none" keeps the tools in the prompt, so the client's prefix is
// reused, and keeps the model from calling them (tool_choice_none_v1). The
// engine is kept from opening a call (llm/engine_tool_choice.go); a call
// written anyway is dropped here, so the client never receives one.

// noToolCallTag is the tag the model's tool calls open with, when the request
// forbids them: the built-in parser's, else the template's. Empty when calls
// are allowed or the request has no tools.
func noToolCallTag(req *api.ChatRequest, builtin parsers.Parser, toolParser *tools.Parser) string {
	if len(req.Tools) == 0 || !api.ToolChoiceNone(req.ToolChoice) {
		return ""
	}
	if tag := parsers.ToolCallStartTagForParser(builtin); tag != "" {
		return tag
	}
	if toolParser != nil && toolParser.Tag() != "" {
		return toolParser.Tag()
	}
	slog.Warn("tool_choice none: this model's tool calls open with no tag the engine can be kept from writing; a call it writes is dropped",
		"model", req.Model)
	return ""
}

// forbiddenToolCalls drops the calls a tool_choice "none" request forbade.
func forbiddenToolCalls(req *api.ChatRequest, calls []api.ToolCall) []api.ToolCall {
	if len(calls) == 0 || !api.ToolChoiceNone(req.ToolChoice) {
		return calls
	}
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Function.Name
	}
	slog.Warn("dropping tool calls the request forbade (tool_choice none)", "model", req.Model, "tools", names)
	return nil
}
