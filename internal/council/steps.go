package council

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
)

// DefaultCharter is the council's standing instruction. Phase 0 measured it as
// the members' system prompt (plans/council-eval/probe/council_tree.py); since
// Phase 9.3 it opens the planner's plan request instead (Config.Charter).
const DefaultCharter = `You are one member of a council of assistants that answers a user together.
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

// systemIntro introduces the client's system prompt to the members that
// answer the user (Config.System).
const systemIntro = "The user's system prompt for this conversation. Your answer follows it:\n\n"

// directIntro leads a direct answer's copy of it: the message the answer is
// for is the one before.
const directIntro = "Answer the user's message above. "

// The role instructions a model's council may replace (council.<role>.prompt).
// The parts the council fills in -- a researcher's brief, the revise marker,
// the number of briefs -- are added after them, so a replacement keeps working.
var defaultPrompts = map[Role]string{
	Planner:     "The council will answer the user's latest message. Write the plan and one brief per researcher.",
	Researcher:  "Report your findings with the evidence for each.",
	Critic:      "Review the plan and all findings above: errors, gaps, unsupported claims, disagreements. Say which findings you would keep.",
	Synthesizer: "Write the one answer the user receives, from the findings and honouring the critiques.",
}

const routeMsg = `ROLE: PLANNER. Is the user's latest message trivial (a greeting, thanks, small talk, a one-line fact) or does it need the council? Reply with JSON only: {"route":"direct"} or {"route":"council"}.`

// Revise is the word a critic ends with to send the council another round.
const Revise = "VERDICT: REVISE"

var routeSchema = json.RawMessage(`{"type":"object","properties":{"route":{"type":"string","enum":["direct","council"]}},"required":["route"]}`)

func planSchema(n int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"plan":{"type":"string"},"briefs":{"type":"array","items":{"type":"string"},"minItems":%d,"maxItems":%d}},"required":["plan","briefs"]}`, n, n))
}

// Plan is the planner's output on the council path.
type Plan struct {
	Plan   string   `json:"plan"`
	Briefs []string `json:"briefs"`
}

func user(s string) api.Message { return api.Message{Role: "user", Content: s} }

func prompt(cfg Config, r Role) string {
	if p := cfg.Prompts[r]; p != "" {
		return p
	}
	return defaultPrompts[r]
}

func maxTok(cfg Config, r Role) int {
	if n := cfg.MaxTokens[r]; n > 0 {
		return n
	}
	return defaultMaxTokens[r]
}

// call makes one member's call, sending its tokens to emit as k -- unless it
// is deliberation and that is hidden -- and closing them with a Done event.
func call(ctx context.Context, m Model, cfg Config, emit Emit, req Request, k Kind) (string, error) {
	onToken := func(string) {}
	if k != Thinking || cfg.ShowDeliberation {
		onToken = func(s string) {
			emit(Event{Role: req.Role, Index: req.Index, Round: req.Round, Kind: k, Text: s})
		}
		defer emit(Event{Role: req.Role, Index: req.Index, Round: req.Round, Kind: k, Done: true})
	}
	out, err := m.Stream(ctx, req, onToken)
	// An empty reply is no finding: a member elsewhere that answered nothing
	// is replaced like one that failed.
	if err == nil && strings.TrimSpace(out) == "" && fallsBack(ctx, req) {
		err = errors.New("the member replied with nothing")
	}
	if err != nil && fallsBack(ctx, req) {
		// One of several researchers or critics: the council's own model
		// answers in its place, and the turn goes on.
		slog.Warn("council: member failed on its own model, answered by the council's", "role", req.Role, "index", req.Index, "model", req.Model, "host", req.Host, "error", err)
		onToken(fmt.Sprintf("\n\n(%s on %s failed; the council's model answers instead)\n\n", req.Role, memberWhere(req)))
		req.Model, req.Host = "", ""
		return m.Stream(ctx, req, onToken)
	}
	return out, err
}

// fallsBack reports whether a failed member is retried on the council's own
// model: a researcher or critic that ran elsewhere, while the turn is live. A
// planner or a synthesizer is the one of its kind, and its failure is the
// turn's.
func fallsBack(ctx context.Context, req Request) bool {
	return ctx.Err() == nil && (req.Model != "" || req.Host != "") &&
		(req.Role == Researcher || req.Role == Critic)
}

func memberWhere(req Request) string {
	if req.Host == "" {
		return req.Model
	}
	return req.Model + " at " + req.Host
}

// Decide is the route-only decision. Anything but a clean "direct" is the
// council: a malformed decision must never lose the question. It runs on the
// council's own model even when the planner has another: the direct answer
// continues the conversation, which is the council model's to give.
func Decide(ctx context.Context, m Model, cfg Config, d Draws, conv []api.Message) (string, error) {
	out, err := m.Stream(ctx, Request{
		Role: Planner, Messages: append(clone(conv), routeRequest(cfg)),
		Seed: d.Decide.Seed, Temperature: d.Decide.Temperature, MaxTokens: 16, Format: routeSchema,
	}, func(string) {})
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

// Direct answers a trivial message: an ordinary turn, streamed as content,
// followed by the client's system prompt when there is one.
func Direct(ctx context.Context, m Model, cfg Config, d Draws, conv []api.Message, emit Emit) (string, error) {
	msgs := conv
	if cfg.System != "" {
		msgs = append(clone(conv), user(directIntro+systemIntro+cfg.System))
	}
	return call(ctx, m, cfg, emit, Request{
		Role: Planner, Messages: msgs, Seed: d.Direct.Seed,
		Temperature: d.Direct.Temperature, MaxTokens: maxTok(cfg, Synthesizer), Think: cfg.Think[Planner],
	}, Content)
}

// plannerRole opens every planner instruction the council adds: the route
// decision and the plan request, which carries the charter before it.
const plannerRole = "ROLE: PLANNER."

// IsPlannerRequest reports whether a message is one of the planner's
// instructions, the first message the council adds after the conversation.
func IsPlannerRequest(s string) bool {
	return strings.HasPrefix(s, plannerRole) || strings.Contains(s, "\n\n"+plannerRole+" ")
}

// routeRequest is the route decision. The charter opens it as it opens the
// plan request: it is what says which messages are trivial, and with the
// system message empty the decision has no other source for it (measured on
// b133: without it, two of six storage questions were answered directly).
func routeRequest(cfg Config) api.Message {
	if cfg.Charter == "" {
		return user(routeMsg)
	}
	return user(cfg.Charter + "\n\n" + routeMsg)
}

// planMsg is the planner's plan request. The charter opens it: every later
// member continues from it (base), so the charter is prefilled once a turn.
func planMsg(cfg Config) api.Message {
	s := fmt.Sprintf(`ROLE: PLANNER. %s Reply with JSON only: {"plan":"<the plan>","briefs":[<exactly %d researcher briefs>]}.`,
		prompt(cfg, Planner), cfg.Researchers)
	if cfg.Charter != "" {
		s = cfg.Charter + "\n\n" + s
	}
	return user(s)
}

// MakePlan writes the plan and one brief per researcher.
func MakePlan(ctx context.Context, m Model, cfg Config, d Draws, conv []api.Message, emit Emit) (Plan, error) {
	out, err := call(ctx, m, cfg, emit, Request{
		Role: Planner, Model: cfg.Models[Planner], Host: cfg.Hosts[Planner], Messages: append(clone(conv), planMsg(cfg)),
		Seed: d.Plan.Seed, Temperature: d.Plan.Temperature, MaxTokens: maxTok(cfg, Planner), Think: cfg.Think[Planner],
		Format: planSchema(cfg.Researchers),
	}, Thinking)
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
func base(cfg Config, conv []api.Message, p Plan) []api.Message {
	b, _ := json.Marshal(p)
	return append(clone(conv), planMsg(cfg), api.Message{Role: "assistant", Content: string(b)})
}

// Research runs researcher i. prior holds the previous round's critiques, if any.
func Research(ctx context.Context, m Model, cfg Config, d Draws, conv []api.Message, p Plan, i, round int, prior []string, emit Emit) (string, error) {
	msgs := base(cfg, conv, p)
	if len(prior) > 0 {
		msgs = append(msgs, user(joinNumbered("CRITIQUE", prior)))
	}
	msgs = append(msgs, user(fmt.Sprintf("ROLE: RESEARCHER %d. Your brief: %s\n%s", i+1, p.Briefs[i], prompt(cfg, Researcher))))
	dr := d.Researchers[round][i]
	return call(ctx, m, cfg, emit, Request{
		Role: Researcher, Index: i, Round: round, Model: cfg.Models[Researcher], Host: cfg.Hosts[Researcher], Messages: msgs,
		Seed: dr.Seed, Temperature: dr.Temperature, MaxTokens: maxTok(cfg, Researcher), Think: cfg.Think[Researcher],
	}, Thinking)
}

// Critique runs critic i over all findings, in researcher order.
func Critique(ctx context.Context, m Model, cfg Config, d Draws, conv []api.Message, p Plan, findings []string, i, round int, emit Emit) (string, error) {
	instr := prompt(cfg, Critic)
	if round+1 < max(cfg.MaxRounds, 1) {
		instr += fmt.Sprintf(" If the findings are not good enough to answer from, end with %q.", Revise)
	}
	msgs := append(base(cfg, conv, p), user(joinNumbered("FINDINGS OF RESEARCHER", findings)),
		user(fmt.Sprintf("ROLE: CRITIC %d. %s", i+1, instr)))
	dc := d.Critics[round][i]
	return call(ctx, m, cfg, emit, Request{
		Role: Critic, Index: i, Round: round, Model: cfg.Models[Critic], Host: cfg.Hosts[Critic], Messages: msgs,
		Seed: dc.Seed, Temperature: dc.Temperature, MaxTokens: maxTok(cfg, Critic), Think: cfg.Think[Critic],
	}, Thinking)
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
func Synthesize(ctx context.Context, m Model, cfg Config, d Draws, conv []api.Message, p Plan, findings, critiques []string, emit Emit) (string, error) {
	msgs := append(base(cfg, conv, p), user(joinNumbered("FINDINGS OF RESEARCHER", findings)),
		user(joinNumbered("CRITIQUE", critiques)),
		user("ROLE: SYNTHESIZER. "+prompt(cfg, Synthesizer)))
	if cfg.System != "" {
		msgs = append(msgs, user(systemIntro+cfg.System))
	}
	return call(ctx, m, cfg, emit, Request{
		Role: Synthesizer, Model: cfg.Models[Synthesizer], Host: cfg.Hosts[Synthesizer], Messages: msgs,
		Seed: d.Synth.Seed, Temperature: d.Synth.Temperature, MaxTokens: maxTok(cfg, Synthesizer), Think: cfg.Think[Synthesizer],
	}, Content)
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

func clone(m []api.Message) []api.Message { return append([]api.Message(nil), m...) }

// serial makes an Emit safe to call from parallel members.
func serial(emit Emit) Emit {
	var mu sync.Mutex
	return func(e Event) { mu.Lock(); defer mu.Unlock(); emit(e) }
}
