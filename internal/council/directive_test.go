package council

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

func roles(calls []Request) []Role {
	var out []Role
	for _, c := range calls {
		out = append(out, c.Role)
	}
	return out
}

func directed(t *testing.T, cfg Config, d *api.CouncilDirective) Config {
	t.Helper()
	cfg, err := cfg.Direct(d)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Every role reads the owner's and the client's guidance, each once and
// under its source; the builder reads its own; a role reads only its own.
func TestEveryRoleReadsItsInstructionsOnce(t *testing.T) {
	cfg := FromModel(&xollama.Council{
		Instructions: "OWNER-ALL",
		Researcher:   &xollama.CouncilRole{Instructions: "OWNER-RESEARCHER"},
		Builder:      &xollama.CouncilRole{Instructions: "OWNER-BUILDER"},
	}, 0.7)
	cfg = directed(t, cfg, &api.CouncilDirective{Instructions: map[string]string{"council": "CLIENT-ALL", "critic": "CLIENT-CRITIC"}})
	s := &stub{route: `{"route":"council"}`}
	if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.calls {
		text := all(c)
		if c.Role != Builder && strings.Count(text, "OWNER-ALL") != 1 || c.Role != Builder && strings.Count(text, "CLIENT-ALL") != 1 {
			t.Errorf("%s reads the council's guidance %d/%d times, want once each", c.Role, strings.Count(text, "OWNER-ALL"), strings.Count(text, "CLIENT-ALL"))
		}
		for tag, r := range map[string]Role{"OWNER-RESEARCHER": Researcher, "CLIENT-CRITIC": Critic, "OWNER-BUILDER": Builder} {
			want := 0
			if c.Role == r {
				want = 1
			}
			// A later member continues from the plan request, never from a
			// researcher's own; only its role may read it.
			if got := strings.Count(text, tag); got != want && !(want == 0 && c.Role == Synthesizer) {
				t.Errorf("%s reads %s %d times, want %d", c.Role, tag, got, want)
			}
		}
	}
	if !strings.Contains(all(lastOf(s.calls, Planner)), ownerSaid+":\nOWNER-ALL\n\n"+clientSaid+":\nCLIENT-ALL") {
		t.Error("the council's guidance is not under its sources, owner first")
	}
}

// Without guidance, what every member reads is what it read before.
func TestNoInstructionsChangeNothing(t *testing.T) {
	run := func(cfg Config) []string {
		s := &stub{route: `{"route":"council"}`}
		if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range s.calls {
			out = append(out, all(c))
		}
		// Parallel members finish in any order.
		slices.Sort(out)
		return out
	}
	plain := FromModel(nil, 0.7)
	plain.Instructions = nil
	withEmpty := directed(t, FromModel(&xollama.Council{}, 0.7), &api.CouncilDirective{})
	if !slices.Equal(run(plain), run(withEmpty)) {
		t.Error("an empty directive changed what the members read")
	}
}

// Each mode's calls: answer is one call, escalate and deliberate skip the
// route decision, and a stated build skips the builder.
func TestEachModeMakesItsCalls(t *testing.T) {
	build, _ := json.Marshal(map[string]any{"target": "fix it", "planner": "", "researcher": "LOOK-HERE", "critic": "", "synthesizer": "", "max_tests": 2, "max_steps": 9})
	for name, tc := range map[string]struct {
		d     *api.CouncilDirective
		check func(t *testing.T, s *stub)
	}{
		"answer": {&api.CouncilDirective{Mode: api.CouncilModeAnswer}, func(t *testing.T, s *stub) {
			if got := roles(s.calls); !slices.Equal(got, []Role{Planner}) || s.calls[0].Format != nil {
				t.Errorf("calls %v, want the direct answer alone", got)
			}
		}},
		"escalate": {&api.CouncilDirective{Mode: api.CouncilModeEscalate, Evidence: []api.CouncilEvidence{{Tried: "renamed x", Check: "run tests", Result: "FAIL: x undefined"}}}, func(t *testing.T, s *stub) {
			for _, c := range s.calls {
				if c.Format != nil && strings.Contains(string(c.Format), "route") {
					t.Error("escalate asked for a route")
				}
			}
			if p := all(lastOf(s.calls, Planner)); !strings.Contains(p, evidenceIntro) || !strings.Contains(p, "FAIL: x undefined") {
				t.Errorf("the planner does not read the agent's attempt:\n%s", p)
			}
		}},
		"deliberate": {&api.CouncilDirective{Mode: api.CouncilModeDeliberate}, func(t *testing.T, s *stub) {
			if s.count(Planner) == 0 || strings.Contains(string(s.calls[0].Format), "route") {
				t.Errorf("calls %v, want the council without a route decision", roles(s.calls))
			}
		}},
		"stated build": {&api.CouncilDirective{Mode: api.CouncilModeDeliberate, Build: build}, func(t *testing.T, s *stub) {
			if s.count(Builder) != 0 {
				t.Error("the builder was called over a stated build")
			}
			if r := all(lastOf(s.calls, Researcher)); !strings.Contains(r, "For this work: LOOK-HERE") {
				t.Errorf("the researcher does not read the stated build:\n%s", r)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := directed(t, FromModel(nil, 0.7), tc.d)
			s := &stub{route: `{"route":"council"}`}
			if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
				t.Fatal(err)
			}
			tc.check(t, s)
		})
	}
}

// The harness user's cycles stand, within the builder's bounds.
func TestAStatedBuildSetsTheCycles(t *testing.T) {
	for _, tc := range []struct{ tests, steps, wantTests, wantSteps int }{{2, 9, 2, 9}, {40, 1, maxBuildTests, minSteps}} {
		raw, _ := json.Marshal(map[string]any{"target": "t", "max_tests": tc.tests, "max_steps": tc.steps})
		cfg := directed(t, FromModel(nil, 0.7), &api.CouncilDirective{Build: raw})
		if got := cfg.apply(cfg.Stated); got.MaxTests != tc.wantTests || got.MaxSteps != tc.wantSteps {
			t.Errorf("stated %d/%d applied as %d/%d, want %d/%d", tc.tests, tc.steps, got.MaxTests, got.MaxSteps, tc.wantTests, tc.wantSteps)
		}
	}
}

// A mode or slot the council does not know is refused, never ignored.
func TestAnUnknownDirectiveIsRefused(t *testing.T) {
	for _, d := range []*api.CouncilDirective{{Mode: "fast"}, {Instructions: map[string]string{"front": "x"}}} {
		if _, err := FromModel(nil, 0.7).Direct(d); err == nil {
			t.Errorf("%+v was accepted", d)
		}
	}
}

// Under escalate, the council's first check is compared with the agent's
// last: the same output twice across the hand-off is already stuck.
func TestTheFirstCheckIsComparedWithTheAgents(t *testing.T) {
	cfg := directed(t, FromModel(nil, 0.7), &api.CouncilDirective{Mode: api.CouncilModeEscalate, Evidence: []api.CouncilEvidence{{Result: "Error: x  undefined"}}})
	cfg.tests, cfg.checks = []string{"tried y"}, []string{"Error: x undefined"}
	if n := cfg.stuck(); n != 2 {
		t.Errorf("stuck %d, want 2", n)
	}
	if b := cfg.testsBody(); !strings.Contains(b, sameAgentNote) || !strings.Contains(b, "The last 2 checks") {
		t.Errorf("tests body:\n%s", b)
	}
	cfg.checks = []string{"Error: z undefined"}
	if b := cfg.testsBody(); !strings.Contains(b, movedAgentNote) || cfg.stuck() != 1 {
		t.Errorf("a moved check: stuck %d, body:\n%s", cfg.stuck(), b)
	}
}

// A harness that names its check tool decides which read is the check.
func TestTheNamedCheckToolIsTheCheck(t *testing.T) {
	cfg := toolCfg()
	cfg.Tools = append(cfg.Tools, api.Tool{Type: "function", Function: api.ToolFunction{Name: "run_tests", ReadOnly: true}})
	cfg.CheckTool = "run_tests"
	cfg.Results = map[string]string{ForwardedID("s", "r"): "file text", ForwardedID("s", "k"): "FAIL 3"}
	turns := []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w", "a", "b")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{pathRead("r", "a.js")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "k", Function: api.ToolCallFunction{Name: "run_tests", Arguments: api.NewToolCallFunctionArguments()}}}},
	}
	if got := cfg.lastCheck("s", turns); got != "FAIL 3" {
		t.Errorf("check %q, want the named tool's result", got)
	}
}

// The route decision and the front read the user's cues about care; a
// stated mode asks neither.
func TestTheUsersCuesReachTheRouteAndTheFront(t *testing.T) {
	s := &stub{route: `{"route":"direct"}`}
	if _, err := Run(t.Context(), FromModel(nil, 0.7), s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if r := all(s.calls[0]); !strings.Contains(r, routeCue) {
		t.Errorf("the route decision does not read the cue:\n%s", r)
	}
	if f := all(Request{Messages: frontRequest(toolCfg(), conv)}); !strings.Contains(f, frontCue) {
		t.Errorf("the front does not read the cue:\n%s", f)
	}
	answer := directed(t, toolCfg(), &api.CouncilDirective{Mode: api.CouncilModeAnswer})
	if f := all(Request{Messages: frontRequest(answer, conv)}); strings.Contains(f, frontCue) || strings.Contains(f, ForwardTool) {
		t.Errorf("answer mode's front is offered a hand-off:\n%s", f)
	}
}

// 4770e33b run 2: the synthesizer ran the check, edited again, then read the
// file it had edited. The check is the call it checks with, not the read
// after its last change, or every cycle's "check" is the file text and six
// identical errors read as progress.
func TestTheCheckIsTheCallTheMemberChecksWith(t *testing.T) {
	cfg := toolCfg()
	cfg.Tools = append(cfg.Tools, api.Tool{Type: "function", Function: api.ToolFunction{Name: "run_game", ReadOnly: true}})
	run := func(id string) api.ToolCall {
		return api.ToolCall{ID: id, Function: api.ToolCallFunction{Name: "run_game", Arguments: api.NewToolCallFunctionArguments()}}
	}
	cfg.Results = map[string]string{
		ForwardedID("s", "w1"): "edited", ForwardedID("s", "k1"): "SyntaxError: Unexpected token '{'",
		ForwardedID("s", "k2"): "SyntaxError: Unexpected token '{'", ForwardedID("s", "w2"): "edited",
		ForwardedID("s", "w3"): "edited", ForwardedID("s", "r"): "1: <html> the file as edited",
		ForwardedID("s", "f"): "111: a search hit",
	}
	search := pathRead("f", "a.js")
	search.Function.Name = "read_files"
	turns := []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w1", "a", "b"), run("k1")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{run("k2")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w2", "c", "d")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w3", "e", "f")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{pathRead("r", "a.js")}},
	}
	if got := cfg.lastCheck("s", turns); got != "SyntaxError: Unexpected token '{'" {
		t.Errorf("check %q, want the run the member checks with", got)
	}
	// One run and one read after it: the earliest, the run, is the check;
	// and a read whose output repeats the previous check is that check.
	one := []api.Message{{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w1", "a", "b"), run("k1"), pathRead("r", "a.js")}}}
	if got := cfg.lastCheck("s", one); got != "SyntaxError: Unexpected token '{'" {
		t.Errorf("check %q, want the earliest read after the change", got)
	}
	cfg.checks = []string{"1: <html> the file as edited"}
	if got := cfg.lastCheck("s", one); got != "1: <html> the file as edited" {
		t.Errorf("check %q, want the call that repeats the previous check", got)
	}
}
