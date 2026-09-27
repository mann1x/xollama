package council

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The prompts. They match plans/council-eval/probe/council_tree.py, so the
// Go runs and the Phase 0 probe put the same bytes in front of the engine.

const Charter = `You are one member of a council of assistants that answers a user together.
The council has four roles. The PLANNER reads the conversation and decides whether
the latest user message needs the council at all; trivial messages (greetings,
thanks, small talk, a one-line fact) are answered directly. Otherwise the planner
writes a short plan and one brief per researcher. RESEARCHERS each investigate
their brief using only the conversation and their own knowledge, and report
findings with the evidence for each. CRITICS review the plan and all findings:
they point out errors, gaps, unsupported claims and disagreements between
researchers, and say which findings they would keep. The SYNTHESIZER writes the
single answer the user receives, using the findings and honouring the critiques.
Every member writes plainly, cites the part of the conversation it relies on, and
never invents facts about documents it was given. Your role for this turn is
stated in the last message.`

const routeMsg = `ROLE: PLANNER. Is the user's latest message trivial (a greeting, thanks, small talk, a one-line fact) or does it need the council? Reply with JSON only: {"route":"direct"} or {"route":"council"}.`

// Revise is the word a critic ends with to send the council another round.
const Revise = "VERDICT: REVISE"

// Plan is the planner's output on the council path.
type Plan struct {
	Plan   string   `json:"plan"`
	Briefs []string `json:"briefs"`
}

func user(s string) Message { return Message{Role: "user", Content: s} }

func maxTok(cfg Config, r Role) int {
	if n := cfg.MaxTokens[r]; n > 0 {
		return n
	}
	return Defaults().MaxTokens[r]
}

// forward sends one member's tokens to emit, unless deliberation is hidden.
func forward(cfg Config, emit Emit, r Role, i, round int, k Kind) func(string) {
	if k == Thinking && !cfg.ShowDeliberation {
		return func(string) {}
	}
	return func(s string) { emit(Event{Role: r, Index: i, Round: round, Kind: k, Text: s}) }
}

// Decide is the route-only decision. Anything but a clean "direct" is the
// council: a malformed decision must never lose the question.
func Decide(ctx context.Context, m Model, cfg Config, d Draws, conv []Message) (string, error) {
	out, err := m.Stream(ctx, Request{Role: Planner, Messages: append(clone(conv), user(routeMsg)),
		Seed: d.Decide.Seed, Temperature: d.Decide.Temperature, MaxTokens: 16, RouteOnly: true}, func(string) {})
	if err != nil {
		return "", err
	}
	var v struct {
		Route string `json:"route"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &v) == nil && v.Route == "direct" {
		return "direct", nil
	}
	return "council", nil
}

// Direct answers a trivial message: an ordinary turn, streamed as content.
func Direct(ctx context.Context, m Model, cfg Config, d Draws, conv []Message, emit Emit) (string, error) {
	return m.Stream(ctx, Request{Role: Planner, Messages: conv, Seed: d.Direct.Seed,
		Temperature: d.Direct.Temperature, MaxTokens: maxTok(cfg, Synthesizer)},
		forward(cfg, emit, Planner, 0, 0, Content))
}

func planMsg(n int) Message {
	return user(fmt.Sprintf(`ROLE: PLANNER. The council will answer the user's latest message. Reply with JSON only: {"plan":"<the plan>","briefs":[<exactly %d researcher briefs>]}.`, n))
}

// MakePlan writes the plan and one brief per researcher.
func MakePlan(ctx context.Context, m Model, cfg Config, d Draws, conv []Message, emit Emit) (Plan, error) {
	out, err := m.Stream(ctx, Request{Role: Planner, Messages: append(clone(conv), planMsg(cfg.Researchers)),
		Seed: d.Plan.Seed, Temperature: d.Plan.Temperature, MaxTokens: maxTok(cfg, Planner), WantBriefs: cfg.Researchers},
		forward(cfg, emit, Planner, 0, 0, Thinking))
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &p) != nil || p.Plan == "" {
		p = Plan{Plan: strings.TrimSpace(out)}
	}
	for len(p.Briefs) < cfg.Researchers {
		p.Briefs = append(p.Briefs, "Investigate the question from a different angle than the other researchers.")
	}
	p.Briefs = p.Briefs[:cfg.Researchers]
	return p, nil
}

// base is the conversation plus the plan: the prefix every later member shares.
func base(conv []Message, p Plan, researchers int) []Message {
	b, _ := json.Marshal(p)
	return append(clone(conv), planMsg(researchers), Message{Role: "assistant", Content: string(b)})
}

// Research runs researcher i. prior holds the previous round's critiques, if any.
func Research(ctx context.Context, m Model, cfg Config, d Draws, conv []Message, p Plan, i, round int, prior []string, emit Emit) (string, error) {
	msgs := base(conv, p, cfg.Researchers)
	if len(prior) > 0 {
		msgs = append(msgs, user(joinNumbered("CRITIQUE", prior)))
	}
	msgs = append(msgs, user(fmt.Sprintf("ROLE: RESEARCHER %d. Your brief: %s\nReport your findings with the evidence for each.", i+1, p.Briefs[i])))
	dr := d.Researchers[round][i]
	return m.Stream(ctx, Request{Role: Researcher, Index: i, Round: round, Messages: msgs, Seed: dr.Seed,
		Temperature: dr.Temperature, MaxTokens: maxTok(cfg, Researcher)}, forward(cfg, emit, Researcher, i, round, Thinking))
}

// Critique runs critic i over all findings, in researcher order.
func Critique(ctx context.Context, m Model, cfg Config, d Draws, conv []Message, p Plan, findings []string, i, round int, emit Emit) (string, error) {
	msgs := append(base(conv, p, cfg.Researchers), user(joinNumbered("FINDINGS OF RESEARCHER", findings)),
		user(fmt.Sprintf("ROLE: CRITIC %d. Review the plan and all findings above: errors, gaps, unsupported claims, disagreements. Say which findings you would keep. If the findings are not good enough to answer from, end with %q.", i+1, Revise)))
	dc := d.Critics[round][i]
	return m.Stream(ctx, Request{Role: Critic, Index: i, Round: round, Messages: msgs, Seed: dc.Seed,
		Temperature: dc.Temperature, MaxTokens: maxTok(cfg, Critic)}, forward(cfg, emit, Critic, i, round, Thinking))
}

// NeedsRevision reports whether another round is wanted and allowed.
func NeedsRevision(cfg Config, critiques []string, round int) bool {
	if round+1 >= max(cfg.MaxRounds, 1) {
		return false
	}
	for _, c := range critiques {
		if strings.Contains(c, Revise) {
			return true
		}
	}
	return false
}

// Synthesize writes the answer, streamed as content.
func Synthesize(ctx context.Context, m Model, cfg Config, d Draws, conv []Message, p Plan, findings, critiques []string, emit Emit) (string, error) {
	msgs := append(base(conv, p, cfg.Researchers), user(joinNumbered("FINDINGS OF RESEARCHER", findings)),
		user(joinNumbered("CRITIQUE", critiques)),
		user("ROLE: SYNTHESIZER. Write the one answer the user receives, from the findings and honouring the critiques."))
	return m.Stream(ctx, Request{Role: Synthesizer, Messages: msgs, Seed: d.Synth.Seed,
		Temperature: d.Synth.Temperature, MaxTokens: maxTok(cfg, Synthesizer)}, forward(cfg, emit, Synthesizer, 0, 0, Content))
}

func joinNumbered(label string, parts []string) string {
	var b strings.Builder
	for i, s := range parts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%s %d:\n%s", label, i+1, s)
	}
	return b.String()
}

func clone(m []Message) []Message { return append([]Message(nil), m...) }

// Serial makes an Emit safe to call from parallel members: one event at a
// time reaches the wrapped function, in the order the members produced them.
func Serial(emit Emit) Emit {
	var mu sync.Mutex
	return func(e Event) { mu.Lock(); defer mu.Unlock(); emit(e) }
}
