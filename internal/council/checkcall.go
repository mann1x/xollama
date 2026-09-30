package council

import (
	"log/slog"

	"github.com/ollama/ollama/api"
)

// A harness that states its check as a call (api.CouncilCheckCall) has it
// made for the synthesizer. Measured on Cerebriline's harness: its check
// runs through a shell tool, run_commands, which is no read, so the council
// never saw a check at all -- every cycle read as changes with none checked.
// The call stated is the check, and the one call that is.

// isCheck reports whether c is the harness's stated check: the same tool
// with the same arguments.
func (cfg Config) isCheck(c api.ToolCall) bool {
	return cfg.CheckCall != nil && readKey(c) == readKey(*cfg.CheckCall)
}

// uncheckedWrite reports whether the member's turns changed something after
// its last run of the stated check.
func (cfg Config) uncheckedWrite(turns []api.Message) bool {
	if cfg.CheckCall == nil {
		return false
	}
	wrote := false
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			switch {
			case local(c):
			case cfg.isCheck(c):
				wrote = false
			case !cfg.readOnly(c):
				wrote = true
			}
		}
	}
	return wrote
}

// issueCheck is the synthesizer's reply as a turn that makes the stated
// check, for a synthesizer that changed something and ended its turn without
// running it: the client runs the check, and the synthesizer goes on from its
// result, as if it had called it.
func (cfg Config) issueCheck(key string, turns []api.Message, rep Reply) []api.Message {
	slog.Info("council: the synthesizer ended without checking its changes; the council runs the check", "member", key, "tool", cfg.CheckCall.Function.Name)
	return withReasoning(turns, api.Message{Role: "assistant", Content: rep.Content, Thinking: rep.Thinking, ToolCalls: named([]api.ToolCall{*cfg.CheckCall}, len(turns))})
}
