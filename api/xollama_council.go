package api

import "time"

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
