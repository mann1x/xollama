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

// maxCycleSteps bounds, in multiples of the cycle's steps, a cycle whose check
// keeps moving: progress extends it, but not without end.
const maxCycleSteps = 4

// cycleSteps is the synthesizer's tool steps since its check last moved: the
// steps after the latest run of the check whose output differed from the run
// before it. While each fix changes what the check reports -- the next error,
// fewer failures -- the synthesizer keeps its cycle, as a plain agent keeps
// going; a full round of research starts only when the check stops moving or
// it asks for one. Measured on native.sh run 0416: six cycles, each ended by
// the step bound one fix after the check had named the next error plainly,
// each then paying 3-5 minutes of planner, researchers and critics.
//
// from is where that window starts in turns.
func (cfg Config) cycleSteps(key string, turns []api.Message) (steps, from int) {
	prev := ""
	for i, t := range turns {
		for _, c := range t.ToolCalls {
			if !cfg.isCheck(c) {
				continue
			}
			res, _, ok := cfg.result(key, c)
			if !ok {
				continue
			}
			if prev != "" && !sameCheck(res, prev) {
				from = i + 1
			}
			prev = res
		}
	}
	return toolSteps(turns[from:]), from
}
