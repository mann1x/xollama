package api

import (
	"bytes"
	"encoding/json"
)

// xollama-hook: tool-choice — see docs/xollama/tool-choice.mdx
//
// ToolChoiceNone reports whether a request's tool_choice forbids tool calls:
// the string "none", as OpenAI spells it, or {"type": "none"}, as Anthropic
// does. Anything else, absent included, leaves the model free to call
// (tool_choice_none_v1).
func ToolChoiceNone(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == "none"
	}
	var obj struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &obj) == nil && obj.Type == "none"
}
