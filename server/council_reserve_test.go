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
	local := councilReserve(council.FromModel(councilOn(), 0.7), 32768)
	c := councilOn()
	c.Critic = &xollama.CouncilRole{Model: glm, MaxTokens: 131072}
	c.Researcher = &xollama.CouncilRole{Model: glm, MaxTokens: 131072}
	mixed := councilReserve(council.FromModel(c, 0.7), 32768)
	cfg := council.FromModel(c, 0.7)
	want := cfg.Cap(council.Planner) + cfg.ThinkRoom(council.Planner, 32768) + cfg.Cap(council.Synthesizer) + cfg.ThinkRoom(council.Synthesizer, 32768) + 1024
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
	if got := councilReserve(council.FromModel(all, 0.7), 32768); got != 1024 {
		t.Errorf("every role on glm: reserve %d, want only the 1024 for instructions", got)
	}
}

// A member may think its budget and write its cap (num_predict is the two),
// so the owner books both: a stated think setting against the member's window,
// and for a role left without one the most the builder can give it.
func TestTheReserveBooksTheThinkingToo(t *testing.T) {
	c := councilOn()
	c.Planner = &xollama.CouncilRole{Think: "on"}
	c.Researcher = &xollama.CouncilRole{Think: "off"}
	c.Critic = &xollama.CouncilRole{Count: 2, Think: "4096"}
	cfg := council.FromModel(c, 0.7)
	want := cfg.Cap(council.Planner) + xollama.DefaultCouncilThinkBudget + // stated: on
		cfg.Cap(council.Synthesizer) + 4096 + // unstated: the builder's ceiling
		cfg.Researchers*cfg.Cap(council.Researcher) + // stated: off
		2*(cfg.Cap(council.Critic)+4096) + 1024
	if got := councilReserve(cfg, 32768); got != want {
		t.Errorf("reserve %d, want %d", got, want)
	}
	// A level is a share of the member's window.
	c.Planner.Think = "medium"
	small, large := councilReserve(council.FromModel(c, 0.7), 16384), councilReserve(council.FromModel(c, 0.7), 131072)
	if large <= small {
		t.Errorf("a medium planner booked %d on a 131072 window and %d on 16384; want more on the larger", large, small)
	}
}
