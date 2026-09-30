package council

// The builder (plans/agentic-council-chat.md, 11.5; the owner's design
// 2026-09-28). A council is configured before anyone knows what it will be
// asked, so its built-in instructions are generic by necessity. The first time
// a request goes to the council, the builder -- on the planner's slot -- reads
// the system prompt, the user's requests so far and the tools the client
// offers, and writes what each role should do for this kind of work, the
// think budget each needs and how many check cycles the work deserves. The
// synthesizer summons it again (RebuildTool) when a new request no longer
// fits the target.
//
// The builder shapes the council the user defined; it never changes it. The
// roles, their counts and models, a role prompt or think setting the user
// stated, and the tool policy (researchers and critics read, the synthesizer
// alone writes) stand. Its instructions are added to each role's own.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// Builder is the role that shapes a council for its work. It runs on its own
// model and host when the council states them (council.builder), and where
// the planner does otherwise.
const Builder Role = xollama.RoleBuilder

// builderOn is the builder's model, host and think setting: its own, each
// falling back to the planner's.
func builderOn(cfg Config) (model, host, think string) {
	model, host, think = cfg.Models[Builder], cfg.Hosts[Builder], cfg.Think[Builder]
	if model == "" && host == "" {
		model, host = cfg.Models[Planner], cfg.Hosts[Planner]
	}
	if think == "" {
		think = cfg.Think[Planner]
	}
	return model, host, think
}

// Build is what the builder decided for the council's current work.
type Build struct {
	// Target says in a few sentences what the council is set up for: the
	// synthesizer reads it on every later request.
	Target string
	// Instructions are added to each role's own, by role.
	Instructions map[Role]string
	// Think is the budget, in tokens, of each role the user left without a
	// think setting; 0 is none.
	Think map[Role]int
	// MaxTests bounds the check cycles of a turn (11.4), and MaxSteps the
	// synthesizer's tool steps in one cycle before it reports back.
	MaxTests int
	MaxSteps int
}

func (b *Build) clone() *Build {
	if b == nil {
		return nil
	}
	out := &Build{Target: b.Target, MaxTests: b.MaxTests, MaxSteps: b.MaxSteps, Instructions: map[Role]string{}, Think: map[Role]int{}}
	for r, s := range b.Instructions {
		out.Instructions[r] = s
	}
	for r, n := range b.Think {
		out.Think[r] = n
	}
	return out
}

// The builder's choices are bounded: a think level is one of four budgets,
// and the check cycles a number the loop can afford.
var thinkLevels = map[string]int{"off": 0, "low": 1024, "medium": 2048, "high": 4096}

// maxBuiltThink is the most a builder can give a role to think: "high".
const maxBuiltThink = 4096

// ThinkRoom is the most role r may think in one call on a window of window
// tokens: its stated think setting, which stands; else the harness's stated
// build; else the most the builder can give it. It is room to book, so an
// unstated role counts the builder's ceiling even on a turn that gives it
// less.
func (cfg Config) ThinkRoom(r Role, window int) int {
	if t := cfg.Think[r]; t != "" {
		return ThinkBudget(t, window)
	}
	if cfg.Stated != nil {
		return cfg.Stated.Think[r]
	}
	return maxBuiltThink
}

const maxBuildTests = 12

// A synthesizer's steps in one cycle: the builder chooses within these, and
// DefaultMaxSteps holds without a build.
const (
	minSteps        = 2
	maxSteps        = 16
	DefaultMaxSteps = 6
)

var builtRoles = []Role{Planner, Researcher, Critic, Synthesizer}

var buildSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"target":{"type":"string"},` +
	`"planner":{"type":"string"},"researcher":{"type":"string"},"critic":{"type":"string"},"synthesizer":{"type":"string"},` +
	`"think":{"type":"object","properties":{"planner":{"enum":["off","low","medium","high"]},"researcher":{"enum":["off","low","medium","high"]},"critic":{"enum":["off","low","medium","high"]},"synthesizer":{"enum":["off","low","medium","high"]}}},` +
	`"max_tests":{"type":"integer","minimum":0,"maximum":12},"max_steps":{"type":"integer","minimum":2,"maximum":16}},` +
	`"required":["target","planner","researcher","critic","synthesizer","max_tests"]}`)

// builderPrompt is the builder's instruction. Its examples are the owner's
// ask: practical ones, and a detailed one for coding, the work a coding
// agent's system prompt and tools make obvious.
const builderPrompt = `ROLE: BUILDER. You set up the council for the work in the conversation above, before it starts. Read the system prompt, the user's requests so far and the tools listed at the start (or that there are none): they say what kind of work this is. Then write, for each role, a short instruction that fits this work -- what to look at, what a good result is, what to avoid. Each is added to the role's own instruction, so do not repeat what a role already does, and never give a role a tool it may not call: researchers and critics may only call the tools that read; the synthesizer alone makes changes and runs checks, and it applies the researchers' proposals. Also choose how much each role thinks (off, low, medium, high) and how many check cycles the work deserves (max_tests: 0 when nothing can be checked, a few for simple edits, more for code or science), and how many tool steps the synthesizer may take in one cycle before it reports back (max_steps: 2 to 16; a few for small edits, more when each check needs several steps), and write a target: two or three sentences saying what the council is set up for, so a later request can be judged against it.

Never name a cause, a suspected place or a fix, even when the conversation already offers one: finding those out is the council's work, and an instruction that names them makes every member look only there (measured: a builder that wrote "focus on template literals" sent the whole council after a theory the first check had already refuted). Say what kind of work this is and how to do it well. Critics judge proposals against the material and the evidence; they never make or run a change, so do not ask them to test.

The planner is the council's coordinator: it keeps the council's task list, schedules each researcher on tasks from it, and closes a task only on the evidence that settled it. Its instruction must make it keep that record for this kind of work: say what one task is here (a part of the material, a question, a hypothesis to settle), what evidence closes a task as done or refuted, and that it reads the task list and every failed check before it plans again, so no work is done twice and nothing refuted is tried again.

Examples of the kind of fit wanted:

- Proof-reading ("fix the typos and rewrite the prose"): researchers each take part of the text and list the errors with the corrected wording; the critic checks that the meaning is kept; the synthesizer applies the corrections in one pass. Think low or off. max_tests 1.
- A factual or research question with no tools: researchers each take one angle and state what they know and how sure they are; the critic looks for contradictions and unsupported claims; the synthesizer answers with the uncertainty stated. Think medium. max_tests 0.
- Data analysis with tools that read files and run queries: researchers inspect the data and propose the query or computation that answers the question, with the check that shows it is right; the synthesizer runs it and reports numbers with their source. Think medium. max_tests 3.
- Coding (a coding agent's system prompt; tools to read, search, edit and run): this is the case to get right.
  - planner: split the work by file, component or hypothesis so researchers do not overlap; one task per part or hypothesis, closed only by a check's output or the code read; after a failed check, update the task list from what the check showed, not from the first theory; when the checks stop moving, the next tasks change approach.
  - planner: when the error does not say where it is, the first job is to locate it: split the material between the researchers so each narrows down its own part, rather than giving each a theory.
  - researcher: localize before theorizing -- when an error names no place, narrow it down (take the material apart, count what must balance, compare with what works) until one place is left; read the code the brief names before concluding; report every fault found in its part, not only the first, each as the exact edit (old text, new text) and the command that shows it works (a test, a build, a run); say what would prove the proposal wrong; never report a diagnosis that was not read in the code.
  - critic: check each proposal against the code it cites; reject one that repeats a refuted check; name the proposal most likely to work first.
  - synthesizer: make all the proposed edits that do not conflict in one reply, then run the check once and read the whole output; when it shows a new error whose place and fix are plain (a name that is not defined, a line it points at), fix it and check again; when an edit is refused, read the code it targets as it is now and make the edit again from that text, never the same call unchanged; stop when the check passes and report what changed.
  Think medium for the researchers and critic, low for the planner and synthesizer. max_tests 6 for a bug fix, 8 or more for a feature across several files; max_steps 6.

The council, as the user defined it:
%s
Reply with JSON only: {"target":"...","planner":"...","researcher":"...","critic":"...","synthesizer":"...","think":{"planner":"...","researcher":"...","critic":"...","synthesizer":"..."},"max_tests":N}.`

// architecture describes the council the user defined, for the builder.
func (cfg Config) architecture() string {
	var b strings.Builder
	fmt.Fprintf(&b, "- 1 planner, %d researcher(s), %d critic(s), 1 synthesizer.\n", cfg.Researchers, cfg.Critics)
	for _, r := range builtRoles {
		var parts []string
		if m := cfg.Models[r]; m != "" {
			parts = append(parts, "model "+m)
		}
		if p := cfg.Prompts[r]; p != "" {
			parts = append(parts, fmt.Sprintf("its own instruction: %q", p))
		}
		if t := cfg.Think[r]; t != "" {
			parts = append(parts, "think "+t+" (the user's setting; it stands)")
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "- %s: %s.\n", r, strings.Join(parts, "; "))
		}
	}
	if len(cfg.Tools) == 0 {
		b.WriteString("- The client offers no tools.\n")
	}
	return b.String()
}

// MakeBuild runs the builder over the conversation. A reply that is not the
// JSON asked for is an empty build: the council runs as configured.
func MakeBuild(ctx context.Context, m Model, cfg Config, d Draws, conv []api.Message, emit Emit) (*Build, error) {
	model, host, think := builderOn(cfg)
	out, err := call(ctx, m, cfg, emit, Request{
		Role: Builder, Model: model, Host: host, NumCtx: numCtx(cfg, Builder),
		Messages: append(builderConversation(conv), user(cfg.withInstructions(sourcesNote+"\n\n"+fmt.Sprintf(builderPrompt, cfg.architecture()), Builder))),
		Seed:     d.Plan.Seed, Temperature: d.Plan.Temperature, MaxTokens: maxTok(cfg, Builder, model, host), Think: think,
		Format: buildSchema,
	}, Thinking)
	if err != nil {
		return nil, err
	}
	return parseBuild(out), nil
}

func parseBuild(out string) *Build {
	var v struct {
		Target, Planner, Researcher, Critic, Synthesizer string
		Think                                            map[string]string
		MaxTests                                         *int `json:"max_tests"`
		MaxSteps                                         *int `json:"max_steps"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &v) != nil || strings.TrimSpace(v.Target) == "" {
		// Recorded, so a resumed turn does not ask again; with no target it
		// shapes nothing and a later turn builds afresh (previousBuild).
		return &Build{MaxTests: DefaultMaxTests}
	}
	b := &Build{Target: strings.TrimSpace(v.Target), MaxTests: DefaultMaxTests, Instructions: map[Role]string{}, Think: map[Role]int{}}
	for r, s := range map[Role]string{Planner: v.Planner, Researcher: v.Researcher, Critic: v.Critic, Synthesizer: v.Synthesizer} {
		if s = strings.TrimSpace(s); s != "" {
			b.Instructions[r] = s
		}
	}
	for r, l := range v.Think {
		if n, ok := thinkLevels[l]; ok {
			b.Think[Role(r)] = n
		}
	}
	if v.MaxTests != nil {
		b.MaxTests = min(max(*v.MaxTests, 0), maxBuildTests)
	}
	if v.MaxSteps != nil {
		b.MaxSteps = min(max(*v.MaxSteps, minSteps), maxSteps)
	}
	return b
}

// apply shapes cfg by build: each role's instruction gains the builder's, a
// role the user left without a think setting takes the builder's budget, and
// the check cycles are the builder's.
func (cfg Config) apply(b *Build) Config {
	if b == nil {
		return cfg
	}
	cfg.build = b
	prompts := make(map[Role]string, len(builtRoles))
	for r, p := range cfg.Prompts {
		prompts[r] = p
	}
	think := make(map[Role]string, len(builtRoles))
	for r, t := range cfg.Think {
		think[r] = t
	}
	for _, r := range builtRoles {
		if s := b.Instructions[r]; s != "" {
			prompts[r] = basePrompt(cfg, r) + " For this work: " + s
		}
		if n, ok := b.Think[r]; ok && cfg.Think[r] == "" && n > 0 {
			think[r] = strconv.Itoa(n)
		}
	}
	cfg.Prompts, cfg.Think = prompts, think
	cfg.MaxTests = b.MaxTests
	if b.MaxSteps > 0 {
		cfg.MaxSteps = b.MaxSteps
	}
	return cfg
}

// targetNote is what the synthesizer reads of the build on a later request.
func (b *Build) targetNote() string {
	if b == nil {
		return ""
	}
	return "The council is set up for: " + b.Target
}

// RouteRebuild sends a request to a council shaped again: the builder runs
// before the plan. The route decision offers it once a build exists.
const RouteRebuild = "rebuild"

const rebuildChoice = `If the latest message is new work that this target does not fit, reply {"route":"rebuild"} instead of {"route":"council"}.`

// routeSchema is the route decision's schema: the routes this turn offers.
func (cfg Config) routeSchema() json.RawMessage {
	routes := []string{`"direct"`, `"council"`}
	if cfg.canContinue() {
		routes = []string{`"direct"`, `"continue"`, `"council"`}
	}
	if cfg.previousBuild() != nil {
		routes = append(routes, `"rebuild"`)
	}
	return json.RawMessage(`{"type":"object","properties":{"route":{"type":"string","enum":[` + strings.Join(routes, ",") + `]}},"required":["route"]}`)
}

// canContinue reports whether the last deliberation can be continued.
func (cfg Config) canContinue() bool {
	_, _, ok := continuing(cfg.Previous)
	return ok
}

// previousBuild is the build the council's last deliberation left.
func (cfg Config) previousBuild() *Build {
	if cfg.Previous == nil || cfg.Previous.Build == nil || cfg.Previous.Build.Target == "" {
		return nil
	}
	return cfg.Previous.Build
}

// keptWith is the deliberation a direct answer leaves: the one before, with
// a build the front made meanwhile.
func keptWith(prev *Progress, b *Build) *Progress {
	if b == nil {
		return prev
	}
	k := Progress{Route: RouteCouncil}
	if prev != nil {
		k = prev.clone()
	}
	k.Build = b.clone()
	return &k
}

// The failed checks a council carries between turns (B): the most recent
// maxPrior, each at most maxPriorChars.
const (
	maxPrior      = 6
	maxPriorChars = 6000
)

// previousPrior is what the last deliberation leaves of failed checks.
func (cfg Config) previousPrior() []string {
	if cfg.Previous == nil {
		return nil
	}
	var out []string
	for _, s := range cfg.Previous.Prior {
		if !strings.HasPrefix(s, earlierMark) {
			s = earlierMark + s
		}
		out = append(out, s)
	}
	return out
}

func lastPrior(p []string) []string {
	for i := range p {
		p[i] = truncate(p[i], maxPriorChars)
	}
	return p[max(0, len(p)-maxPrior):]
}

// earlierMark opens a check carried from an earlier turn, so a rebuild can
// drop those and keep the front's attempts at the new work.
const earlierMark = "(earlier turn) "

// dropEarlier keeps only this turn's attempts: a rebuild is new work.
func dropEarlier(p []string) []string {
	var out []string
	for _, s := range p {
		if !strings.HasPrefix(s, earlierMark) {
			out = append(out, s)
		}
	}
	return out
}

const priorIntro = "Checks that already failed before this council began: on earlier turns of this work, or the synthesizer's own attempts this turn. Each says what was tried and what came back. Do not propose again what they refuted; build on what they showed.\n\n"

// withPrior is the conversation followed by the failed checks before this
// council, when there are some: the builder, the planner and every member
// read them ahead of the plan.
func withPrior(conv []api.Message, prior []string) []api.Message {
	if len(prior) == 0 {
		return conv
	}
	return append(clone(conv), sourced(priorSource, priorIntro+joinNumbered("EARLIER CHECK", prior)))
}

// builderConversation is what the builder reads of the conversation: the
// system prompt and the user's own messages, never the answers, the members'
// work or the tools' results. It judges the kind of work, and a result in view
// gave it a place to name (the third simple run on eleven2go: "likely in the
// Level class methods on lines 115-126", written into every role).
func builderConversation(conv []api.Message) []api.Message {
	var out []api.Message
	for _, m := range conv {
		if m.Role == "system" || (m.Role == "user" && !strings.HasPrefix(m.Content, sourceOpen)) {
			out = append(out, api.Message{Role: m.Role, Content: m.Content, Images: m.Images})
		}
	}
	return out
}
