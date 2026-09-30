package council

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

const codingBuild = `{"target":"Fixing a bug in a game.","planner":"split by file","researcher":"read before concluding","critic":"reject refuted proposals","synthesizer":"one edit at a time","think":{"researcher":"high","planner":"off","critic":"medium"},"max_tests":40}`

// The builder shapes the council the user defined and never changes it: its
// instructions follow each role's own, a think setting the user stated
// stands, and its check cycles are bounded.
func TestTheBuilderShapesTheCouncilTheUserDefined(t *testing.T) {
	yes := true
	cfg := FromModel(&xollama.Council{
		Enabled: &yes,
		Critic:  &xollama.CouncilRole{Prompt: "Be harsh.", Think: "low"},
	}, 0.7)
	cfg.Tools = testTools
	s := &toolStub{stub: stub{route: `{"route":"council"}`, build: codingBuild}}
	res, err := Run(t.Context(), cfg, s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	var builder bool
	for _, c := range s.calls {
		last := c.Messages[len(c.Messages)-1].Content
		switch c.Role {
		case Builder:
			builder = true
			if !strings.Contains(last, "1 planner, 2 researcher(s), 1 critic(s), 1 synthesizer") || !strings.Contains(last, `its own instruction: "Be harsh."`) || !strings.Contains(last, "think low (the user's setting; it stands)") {
				t.Errorf("the builder was not given the council as defined: %q", last)
			}
		case Researcher:
			if !strings.Contains(last, defaultPrompts[Researcher]+" For this work: read before concluding") || c.Think != "8192" { // high: half the 16384 cap
				t.Errorf("researcher: think %q, instruction %q", c.Think, last)
			}
		case Critic:
			if !strings.Contains(last, "Be harsh. For this work: reject refuted proposals") || c.Think != "low" {
				t.Errorf("critic: think %q (the user's low stands), instruction %q", c.Think, last)
			}
		case Planner:
			if c.Format != nil && strings.Contains(string(c.Format), "briefs") && c.Think != "" {
				t.Errorf("planner: think %q, want off", c.Think)
			}
		}
	}
	if !builder {
		t.Fatal("no builder ran")
	}
	if res.Kept == nil || res.Kept.Build == nil || res.Kept.Build.MaxTests != maxBuildTests || res.Kept.Build.Target != "Fixing a bug in a game." {
		t.Errorf("kept build %+v", res.Kept)
	}

	// The next turn reads the kept build and does not build again; asked
	// to rebuild, it does.
	for route, want := range map[string]int{`{"route":"council"}`: 0, `{"route":"rebuild"}`: 1} {
		cfg.Previous = res.Kept
		s := &toolStub{stub: stub{route: route, build: codingBuild}}
		if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		if n := s.count(Builder); n != want {
			t.Errorf("%s: %d builders, want %d", route, n, want)
		}
		decide := s.calls[0].Messages[len(s.calls[0].Messages)-1].Content
		if !strings.Contains(decide, "The council is set up for: Fixing a bug in a game.") || !strings.Contains(decide, `{"route":"rebuild"}`) {
			t.Errorf("the route decision lacks the target and the rebuild choice: %q", decide)
		}
	}
}

// A builder that does not answer in the JSON asked for shapes nothing, and is
// recorded so a resumed turn does not ask again.
func TestABuilderThatSaysNothingShapesNothing(t *testing.T) {
	b := parseBuild("I think this is about code.", Config{})
	if b == nil || b.Target != "" || len(b.Instructions) != 0 || b.MaxTests != DefaultMaxTests {
		t.Fatalf("build %+v", b)
	}
	cfg := toolCfg()
	if got := cfg.apply(b); got.Prompts[Researcher] != "" {
		t.Errorf("an empty build changed a prompt: %q", got.Prompts[Researcher])
	}
	cfg.Previous = &Progress{Build: b}
	if cfg.previousBuild() != nil {
		t.Error("an empty build is offered as a target")
	}
}

// The builder runs on its own model and host when the council states them,
// and on the planner's otherwise; a think setting of its own stands, and
// without one it reasons as the planner does.
func TestTheBuilderRunsOnItsOwnModel(t *testing.T) {
	yes := true
	for _, tt := range []struct {
		name                   string
		c                      *xollama.Council
		model, host, wantThink string
	}{
		{"unstated", &xollama.Council{Enabled: &yes, Planner: &xollama.CouncilRole{Model: "p", Host: "http://gpu2:11434", Think: "low"}}, "p", "http://gpu2:11434", "low"},
		{"its own", &xollama.Council{
			Enabled: &yes, Planner: &xollama.CouncilRole{Model: "p", Host: "http://gpu2:11434", Think: "low"},
			Builder: &xollama.CouncilRole{Model: "glm-5.3-turbo:cloud", Think: "high"},
		}, "glm-5.3-turbo:cloud", "", "high"},
		{"its own model, the planner's think", &xollama.Council{
			Enabled: &yes, Planner: &xollama.CouncilRole{Think: "low"}, Builder: &xollama.CouncilRole{Model: "glm"},
		}, "glm", "", "low"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := FromModel(tt.c, 0.7)
			s := &stub{build: codingBuild}
			if _, err := MakeBuild(t.Context(), s, cfg, Draws{}, conv, func(Event) {}); err != nil {
				t.Fatal(err)
			}
			var got *Request
			for i := range s.calls {
				if s.calls[i].Role == Builder {
					got = &s.calls[i]
				}
			}
			if got == nil {
				t.Fatal("no builder ran")
			}
			if got.Model != tt.model || got.Host != tt.host || got.Think != tt.wantThink {
				t.Errorf("builder on %q at %q, think %q; want %q at %q, think %q", got.Model, got.Host, got.Think, tt.model, tt.host, tt.wantThink)
			}
		})
	}
}
