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
)

// Builder is the role that shapes a council for its work. It runs where the
// planner does.
const Builder Role = "builder"

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
	// MaxTests bounds the check cycles of a turn (11.4).
	MaxTests int
}

func (b *Build) clone() *Build {
	if b == nil {
		return nil
	}
	out := &Build{Target: b.Target, MaxTests: b.MaxTests, Instructions: map[Role]string{}, Think: map[Role]int{}}
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

const maxBuildTests = 12

var builtRoles = []Role{Planner, Researcher, Critic, Synthesizer}

var buildSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"target":{"type":"string"},` +
	`"planner":{"type":"string"},"researcher":{"type":"string"},"critic":{"type":"string"},"synthesizer":{"type":"string"},` +
	`"think":{"type":"object","properties":{"planner":{"enum":["off","low","medium","high"]},"researcher":{"enum":["off","low","medium","high"]},"critic":{"enum":["off","low","medium","high"]},"synthesizer":{"enum":["off","low","medium","high"]}}},` +
	`"max_tests":{"type":"integer","minimum":0,"maximum":12}},` +
	`"required":["target","planner","researcher","critic","synthesizer","max_tests"]}`)

// builderPrompt is the builder's instruction. Its examples are the owner's
// ask: practical ones, and a detailed one for coding, the work a coding
// agent's system prompt and tools make obvious.
const builderPrompt = `ROLE: BUILDER. You set up the council for the work in the conversation above, before it starts. Read the system prompt, the user's requests so far and the tools listed at the start (or that there are none): they say what kind of work this is. Then write, for each role, a short instruction that fits this work -- what to look at, what a good result is, what to avoid. Each is added to the role's own instruction, so do not repeat what a role already does, and never give a role a tool it may not call: researchers and critics may only call the tools that read; the synthesizer alone makes changes and runs checks, and it applies the researchers' proposals. Also choose how much each role thinks (off, low, medium, high) and how many check cycles the work deserves (max_tests: 0 when nothing can be checked, a few for simple edits, more for code or science), and write a target: two or three sentences saying what the council is set up for, so a later request can be judged against it.

Examples of the kind of fit wanted:

- Proof-reading ("fix the typos and rewrite the prose"): researchers each take part of the text and list the errors with the corrected wording; the critic checks that the meaning is kept; the synthesizer applies the corrections in one pass. Think low or off. max_tests 1.
- A factual or research question with no tools: researchers each take one angle and state what they know and how sure they are; the critic looks for contradictions and unsupported claims; the synthesizer answers with the uncertainty stated. Think medium. max_tests 0.
- Data analysis with tools that read files and run queries: researchers inspect the data and propose the query or computation that answers the question, with the check that shows it is right; the synthesizer runs it and reports numbers with their source. Think medium. max_tests 3.
- Coding (a coding agent's system prompt; tools to read, search, edit and run): this is the case to get right.
  - planner: split the work by file, component or hypothesis so researchers do not overlap; after a failed check, plan from what the check showed, not from the first theory.
  - researcher: read the code the brief names before concluding; locate the defect or the place to change to a file and line; propose the change as the exact edit (old text, new text) and the command that shows it works (a test, a build, a run); say what would prove the proposal wrong; never report a diagnosis that was not read in the code.
  - critic: check each proposal against the code it cites; reject one that repeats a refuted check; name the proposal most likely to work first.
  - synthesizer: apply one proposal at a time, run its check, and read the whole output; on a failure, re-read the code before editing again and never resend an edit that failed; stop when the check passes and report what changed.
  Think medium for the researchers and critic, low for the planner and synthesizer. max_tests 6 for a bug fix, 8 or more for a feature across several files.

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
	out, err := call(ctx, m, cfg, emit, Request{
		Role: Builder, Model: cfg.Models[Planner], Host: cfg.Hosts[Planner],
		Messages: append(clone(conv), user(fmt.Sprintf(builderPrompt, cfg.architecture()))),
		Seed:     d.Plan.Seed, Temperature: d.Plan.Temperature, MaxTokens: maxTok(cfg, Builder), Think: cfg.Think[Planner],
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
			prompts[r] = prompt(cfg, r) + " For this work: " + s
		}
		if n, ok := b.Think[r]; ok && cfg.Think[r] == "" && n > 0 {
			think[r] = strconv.Itoa(n)
		}
	}
	cfg.Prompts, cfg.Think = prompts, think
	cfg.MaxTests = b.MaxTests
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
