package council

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// stub answers every role with a fixed reply and records the calls.
type stub struct {
	mu      sync.Mutex
	calls   []Request
	route   string
	fail    Role
	revise  bool
	live    atomic.Int32
	peak    atomic.Int32
	blockAt Role
	release chan struct{}
	// away fails every member served on another model or host.
	away bool
}

func (s *stub) Stream(ctx context.Context, req Request, onToken func(string)) (string, error) {
	n := s.live.Add(1)
	defer s.live.Add(-1)
	for p := s.peak.Load(); n > p && !s.peak.CompareAndSwap(p, n); p = s.peak.Load() {
	}
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	if s.blockAt == req.Role && s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if req.Role == s.fail || (s.away && (req.Model != "" || req.Host != "")) {
		return "", errors.New("member failed")
	}
	var out string
	switch {
	case req.Format != nil && strings.Contains(string(req.Format), "route"):
		out = s.route
	case req.Format != nil:
		out = `{"plan":"p","briefs":["a","b","c","d"]}`
	case req.Role == Critic && s.revise:
		out = "not enough. " + Revise
	default:
		out = string(req.Role) + " says"
	}
	onToken(out)
	return out, nil
}

func (s *stub) count(r Role) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c.Role == r {
			n++
		}
	}
	return n
}

var conv = []api.Message{{Role: "system", Content: DefaultCharter}, {Role: "user", Content: "Why is the sky blue?"}}

func TestADirectTurnIsTwoCalls(t *testing.T) {
	s := &stub{route: `{"route":"direct"}`}
	var content strings.Builder
	res, err := Run(t.Context(), FromModel(nil, 0.7), s, conv, func(e Event) {
		if e.Kind != Content {
			t.Errorf("a direct turn streamed %v", e.Kind)
		}
		content.WriteString(e.Text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Route != "direct" || len(s.calls) != 2 || content.String() != "planner says" {
		t.Errorf("route %q, %d calls, content %q", res.Route, len(s.calls), content.String())
	}
}

func TestAMalformedDecisionConvenesTheCouncil(t *testing.T) {
	for _, reply := range []string{"", "direct", `{"route":"maybe"}`, `{"route":"council"}`} {
		s := &stub{route: reply}
		res, err := Run(t.Context(), FromModel(nil, 0.7), s, conv, func(Event) {})
		if err != nil {
			t.Fatal(err)
		}
		if res.Route != "council" {
			t.Errorf("decision %q routed %q, want council", reply, res.Route)
		}
	}
}

func TestTheCouncilRunsEveryRoleAtItsWidth(t *testing.T) {
	three := 3
	s := &stub{route: `{"route":"council"}`}
	cfg := FromModel(&xollama.Council{Researcher: &xollama.CouncilRole{Count: three}}, 0.7)
	var thinking, content int
	var done int
	res, err := Run(t.Context(), cfg, s, conv, func(e Event) {
		switch {
		case e.Done:
			done++
		case e.Kind == Thinking:
			thinking++
		default:
			content++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.count(Researcher); got != 3 {
		t.Errorf("researchers = %d, want 3", got)
	}
	if got := s.count(Critic); got != DefaultWidth {
		t.Errorf("critics = %d, want %d", got, DefaultWidth)
	}
	if s.count(Synthesizer) != 1 || res.Answer != "synthesizer says" {
		t.Errorf("answer %q", res.Answer)
	}
	// plan + 3 findings + 2 critiques as thinking, the answer as content
	if thinking != 6 || content != 1 || done != 7 {
		t.Errorf("thinking %d content %d done %d, want 6, 1 and 7", thinking, content, done)
	}
}

func TestResearchersAndCriticsRunInParallel(t *testing.T) {
	s := &stub{route: `{"route":"council"}`, blockAt: Researcher, release: make(chan struct{})}
	done := make(chan error)
	go func() {
		_, err := Run(t.Context(), FromModel(nil, 0.7), s, conv, func(Event) {})
		done <- err
	}()
	for s.live.Load() < DefaultWidth {
	}
	close(s.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.peak.Load() < DefaultWidth {
		t.Errorf("peak concurrency %d, want %d", s.peak.Load(), DefaultWidth)
	}
}

func TestAFailedMemberCancelsItsSiblings(t *testing.T) {
	s := &stub{route: `{"route":"council"}`, fail: Critic, blockAt: Researcher, release: make(chan struct{})}
	close(s.release)
	_, err := Run(t.Context(), FromModel(nil, 0.7), s, conv, func(Event) {})
	if err == nil || s.count(Synthesizer) != 0 {
		t.Errorf("err %v, synthesizer calls %d", err, s.count(Synthesizer))
	}
}

func TestHiddenDeliberationLeavesTheAnswer(t *testing.T) {
	off := false
	s := &stub{route: `{"route":"council"}`}
	cfg := FromModel(&xollama.Council{ShowDeliberation: &off}, 0.7)
	_, err := Run(t.Context(), cfg, s, conv, func(e Event) {
		if e.Kind == Thinking {
			t.Errorf("thinking event with deliberation off: %+v", e)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestACriticCanSendTheResearchBackWithinTheBound(t *testing.T) {
	s := &stub{route: `{"route":"council"}`, revise: true}
	cfg := FromModel(&xollama.Council{MaxRounds: 3}, 0.7)
	res, err := Run(t.Context(), cfg, s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rounds != 3 || s.count(Researcher) != 3*DefaultWidth {
		t.Errorf("rounds %d researchers %d", res.Rounds, s.count(Researcher))
	}
}

func TestJitterStaysInItsBandAndOnlyOnResearchersAndCritics(t *testing.T) {
	cfg := FromModel(nil, 0.7)
	for range 200 {
		d := NewDraws(cfg)
		for _, fixed := range []Draw{d.Decide, d.Direct, d.Plan, d.Synth} {
			if fixed.Temperature != 0.7 {
				t.Fatalf("planner/synthesizer temperature %v, want 0.7", fixed.Temperature)
			}
		}
		for _, row := range append(d.Researchers, d.Critics...) {
			for _, dr := range row {
				if dr.Temperature < 0.7*0.98-1e-9 || dr.Temperature > 0.7*1.02+1e-9 {
					t.Fatalf("temperature %v outside ±2%%", dr.Temperature)
				}
			}
		}
	}
}

func TestAStatedSeedReproducesEveryDraw(t *testing.T) {
	seed := int64(7)
	cfg := FromModel(&xollama.Council{Seed: &seed}, 0.7)
	a, b := NewDraws(cfg), NewDraws(cfg)
	if a.Decide != b.Decide || a.Researchers[0][1] != b.Researchers[0][1] || a.Critics[0][0] != b.Critics[0][0] {
		t.Error("the same seed drew different parameters")
	}
	c := NewDraws(FromModel(nil, 0.7))
	if c.Researchers[0][0].Seed == c.Researchers[0][1].Seed {
		t.Error("two researchers drew the same seed")
	}
}

func TestARolePromptReplacesOnlyTheInstruction(t *testing.T) {
	s := &stub{route: `{"route":"council"}`}
	cfg := FromModel(&xollama.Council{
		Researcher: &xollama.CouncilRole{Prompt: "Answer in French.", Model: "other:7b"},
		Charter:    "ignored here",
	}, 0.7)
	if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.calls {
		if c.Role != Researcher {
			continue
		}
		last := c.Messages[len(c.Messages)-1].Content
		if !strings.Contains(last, "Your brief: ") || !strings.Contains(last, "Answer in French.") {
			t.Errorf("researcher instruction %q", last)
		}
		if c.Model != "other:7b" {
			t.Errorf("researcher model %q", c.Model)
		}
	}
	if Charter(&xollama.Council{Charter: "mine"}) != "mine" || Charter(nil) != DefaultCharter {
		t.Error("charter")
	}
}

func TestACanceledTurnReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &stub{route: `{"route":"council"}`, blockAt: Researcher, release: make(chan struct{})}
	done := make(chan error)
	go func() {
		_, err := Run(ctx, FromModel(nil, 0.7), s, conv, func(Event) {})
		done <- err
	}()
	for s.live.Load() < DefaultWidth {
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want context.Canceled", err)
	}
}

func TestThinkBudgetResolvesARoleSetting(t *testing.T) {
	for setting, want := range map[string]int{
		"": 0, "off": 0,
		"on":     2048, // DefaultCouncilThinkBudget, whatever the window
		"medium": 4096,
		"high":   8192,
		"low":    2048,
		"2048":   2048,
	} {
		if got := ThinkBudget(setting, 16384); got != want {
			t.Errorf("ThinkBudget(%q, 16384) = %d, want %d", setting, got, want)
		}
	}
}

func TestEachRoleCarriesItsThinkButNeverTheRoute(t *testing.T) {
	yes := true
	cfg := FromModel(&xollama.Council{
		Enabled:     &yes,
		Planner:     &xollama.CouncilRole{Think: "on"},
		Researcher:  &xollama.CouncilRole{Think: "high"},
		Synthesizer: &xollama.CouncilRole{Think: "1024"},
	}, 0.7)
	s := &stub{route: `{"route":"council"}`}
	if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	want := map[Role]string{Planner: "on", Researcher: "high", Critic: "", Synthesizer: "1024"}
	for _, c := range s.calls {
		isRoute := c.Format != nil && strings.Contains(string(c.Format), "route")
		switch {
		case isRoute && c.Think != "":
			t.Errorf("the routing call carries think %q; it must never reason", c.Think)
		case !isRoute && c.Think != want[c.Role]:
			t.Errorf("%s: think %q, want %q", c.Role, c.Think, want[c.Role])
		}
	}

	// A direct answer is the planner's, so it reasons as the planner does.
	s = &stub{route: `{"route":"direct"}`}
	if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if last := s.calls[len(s.calls)-1]; last.Think != "on" {
		t.Errorf("direct answer think %q, want the planner's", last.Think)
	}
}

// A researcher or critic that fails on its own host or model is answered by
// the council's model, and the turn goes on; the one planner or synthesizer
// fails the turn.
func TestAFailedRemoteMemberFallsBackOnlyWhereItIsOneOfSeveral(t *testing.T) {
	yes := true
	remote := &xollama.CouncilRole{Model: "qwen3:4b", Host: "http://gpu2:11434"}
	cfg := FromModel(&xollama.Council{Enabled: &yes, Researcher: remote, Critic: remote}, 0.7)
	s := &stub{route: `{"route":"council"}`, away: true}
	var text strings.Builder
	res, err := Run(t.Context(), cfg, s, conv, func(e Event) { text.WriteString(e.Text) })
	if err != nil {
		t.Fatalf("a failed remote researcher failed the turn: %v", err)
	}
	if res.Answer == "" {
		t.Error("no answer")
	}
	local, away := 0, 0
	for _, c := range s.calls {
		if c.Role == Researcher || c.Role == Critic {
			if c.Host == "" && c.Model == "" {
				local++
			} else {
				away++
			}
		}
	}
	if away != 4 || local != 4 {
		t.Errorf("%d calls elsewhere and %d on the council's model, want 4 and 4", away, local)
	}
	if !strings.Contains(text.String(), "qwen3:4b at http://gpu2:11434 failed") {
		t.Errorf("the deliberation does not say a member fell back: %q", text.String())
	}

	for _, role := range []Role{Planner, Synthesizer} {
		c := &xollama.Council{Enabled: &yes}
		if role == Planner {
			c.Planner = remote
		} else {
			c.Synthesizer = remote
		}
		s := &stub{route: `{"route":"council"}`, away: true}
		if _, err := Run(t.Context(), FromModel(c, 0.7), s, conv, func(Event) {}); err == nil {
			t.Errorf("a failed remote %s did not fail the turn", role)
		}
	}
}

func TestACanceledTurnDoesNotFallBack(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if fallsBack(ctx, Request{Role: Researcher, Host: "http://gpu2:11434", Model: "m"}) {
		t.Error("a canceled turn retried its member")
	}
	if fallsBack(t.Context(), Request{Role: Researcher}) {
		t.Error("a member on the council's own model retried on itself")
	}
}
