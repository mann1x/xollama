package api

import (
	"encoding/json"
	"time"
)

// CouncilUsage is what one role's members spent on a council request
// (FeatureCouncilUsage), on its done chunk: one entry per role and per the
// model and host it ran on. A background reviewer's work is reported on the
// next done chunk of its conversation after it finished.
type CouncilUsage struct {
	Role string `json:"role"`
	// Model and Host are set when the role runs elsewhere than the
	// council's own model.
	Model string `json:"model,omitempty"`
	Host  string `json:"host,omitempty"`
	Calls int    `json:"calls"`
	// PromptTokens is every prompt token the role's members sent: what a
	// cloud model bills. CachedTokens is the part the engine served from its
	// cache or a pool without computing it.
	PromptTokens int `json:"prompt_tokens"`
	CachedTokens int `json:"cached_tokens,omitempty"`
	EvalTokens   int `json:"eval_tokens"`
	// PromptDuration and EvalDuration are the engine's; Wall is each call's
	// time from request to its last token, summed, so parallel members
	// overlap.
	PromptDuration time.Duration `json:"prompt_duration,omitempty"`
	EvalDuration   time.Duration `json:"eval_duration,omitempty"`
	Wall           time.Duration `json:"wall,omitempty"`
}

// CouncilTag names the council member a thinking chunk belongs to
// (FeatureCouncilTags). Index and Round count from 0: the second researcher of
// the first round is {researcher, 1, 0}. The planner and the synthesizer are
// always index 0. A content chunk -- the answer, the synthesizer's or the
// planner's on a turn it answers directly -- carries no tag.
type CouncilTag struct {
	Role  string `json:"role"`
	Index int    `json:"index"`
	Round int    `json:"round"`
}

// The modes of a council directive (FeatureCouncilDirective). A stated mode
// holds for the whole turn: the council never switches out of it.
const (
	// CouncilModeAuto is the council deciding, as without a directive.
	CouncilModeAuto = "auto"
	// CouncilModeAnswer is the synthesizer answering alone, as a plain agent
	// turn, with no hand-off to the council offered.
	CouncilModeAnswer = "answer"
	// CouncilModeEscalate starts the council at once, with the harness's
	// Evidence as the checks that already failed.
	CouncilModeEscalate = "escalate"
	// CouncilModeDeliberate starts the council at once, with no evidence.
	CouncilModeDeliberate = "deliberate"
)

// CouncilDirective is what a harness states about a council turn, so the
// council does not work it out again (plans/council-harness.md, Phase 2).
// Send it on every request of the turn, as the tools are: a resumed turn
// reads it again.
type CouncilDirective struct {
	// Mode is one of the CouncilMode names; empty is auto.
	Mode string `json:"mode,omitempty"`
	// Instructions are added after the model's own, by slot: "council" (every
	// role, after the charter), "builder", and a role's name ("planner",
	// "researcher", "critic", "synthesizer").
	Instructions map[string]string `json:"instructions,omitempty"`
	// Build is a build in the builder's own reply format. When it names a
	// target, the builder is not called; its max_tests and max_steps are the
	// harness user's and stand, within the builder's bounds.
	Build json.RawMessage `json:"build,omitempty"`
	// Evidence is what the harness's agent tried and what came back, oldest
	// first. Under escalate it is the turn's checks that already failed.
	Evidence []CouncilEvidence `json:"evidence,omitempty"`
	// Check names the client tool whose result is the check of a change.
	// Without it, the check is the first read-only call after the last
	// change.
	Check string `json:"check,omitempty"`
	// CheckCall is the check itself, when the harness can state it: a client
	// tool and its arguments (council_check_call_v1). Only this call is the
	// check, whether or not its tool only reads -- a shell tool that runs the
	// check also makes changes -- and a synthesizer that changed something
	// and ends its turn without it has it made for it. It names Check.
	CheckCall *CouncilCheckCall `json:"check_call,omitempty"`
}

// CouncilCheckCall is a harness's check, as the tool call that runs it.
type CouncilCheckCall struct {
	Tool      string                    `json:"tool"`
	Arguments ToolCallFunctionArguments `json:"arguments"`
}

// CouncilEvidence is one attempt of the harness's agent.
type CouncilEvidence struct {
	// Tried is what the agent changed or did, in the harness's words.
	Tried string `json:"tried,omitempty"`
	// Check is how it checked, such as the command it ran.
	Check string `json:"check,omitempty"`
	// Result is the check's output, verbatim: the council compares its own
	// first check with it.
	Result string `json:"result,omitempty"`
}
