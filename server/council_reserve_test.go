package server

import (
	"testing"

	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/types/xollama"
)

// The owner's window holds the replies of the roles on the council's own
// model only. A role on another model, however large its cap, runs on its own
// and books nothing there.
func TestTheReserveCountsOnlyTheRolesOnTheCouncilsModel(t *testing.T) {
	glm := "glm-5.3-flash:cloud"
	local := councilReserve(council.FromModel(councilOn(), 0.7))
	c := councilOn()
	c.Critic = &xollama.CouncilRole{Model: glm, MaxTokens: 131072}
	c.Researcher = &xollama.CouncilRole{Model: glm, MaxTokens: 131072}
	mixed := councilReserve(council.FromModel(c, 0.7))
	cfg := council.FromModel(c, 0.7)
	want := cfg.Cap(council.Planner) + cfg.Cap(council.Synthesizer) + 1024
	if mixed != want {
		t.Errorf("planner and synthesizer on the council's model, the rest on glm: reserve %d, want %d", mixed, want)
	}
	if mixed >= local {
		t.Errorf("moving roles off the council's model grew the reserve: %d, was %d", mixed, local)
	}
	all := councilOn()
	for _, r := range []**xollama.CouncilRole{&all.Planner, &all.Researcher, &all.Critic, &all.Synthesizer} {
		*r = &xollama.CouncilRole{Model: glm, MaxTokens: 131072}
	}
	if got := councilReserve(council.FromModel(all, 0.7)); got != 1024 {
		t.Errorf("every role on glm: reserve %d, want only the 1024 for instructions", got)
	}
}
