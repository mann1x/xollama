package api

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
