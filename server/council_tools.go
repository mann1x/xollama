package server

// xollama: tools on council turns, the server's half -- see
// plans/agentic-council-chat.md (9.5) and internal/council/tools.go.

import (
	"slices"

	"github.com/ollama/ollama/api"
)

// councilToolTurn splits a resumed turn's own traffic off its conversation.
// Everything after the last user message is the council's calls this turn and
// the client's results; the results go back to the members that made the
// calls (by the forwarded id, "r2:call_0_0"), never into the conversation
// every member shares. A result that names no call takes the next unanswered
// call of the assistant turn before it, as a client that keeps no ids sends
// them.
func councilToolTurn(conv []api.Message) ([]api.Message, map[string]string) {
	last := -1
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" {
			last = i
			break
		}
	}
	if last < 0 || last == len(conv)-1 {
		return conv, nil
	}
	results := map[string]string{}
	var open []string
	for _, msg := range conv[last+1:] {
		switch msg.Role {
		case "assistant":
			open = nil
			for _, c := range msg.ToolCalls {
				open = append(open, c.ID)
			}
		case "tool":
			id := msg.ToolCallID
			if id == "" && len(open) > 0 {
				id = open[0]
			}
			open = slices.DeleteFunc(open, func(s string) bool { return s == id })
			if id != "" {
				results[id] = msg.Content
			}
		}
	}
	return conv[:last+1], results
}
