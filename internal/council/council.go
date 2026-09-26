// Package council runs a council turn: one chat turn answered by a planner,
// researchers in parallel, critics in parallel and a synthesizer, streamed back
// as one answer. The deliberation travels as thinking, the answer as content.
//
//	decide (route-only) ─ direct ─► answer, streamed as content
//	        └ council ─► plan ─► researchers ∥ ─► critics ∥ ─► synthesizer
//	                               ▲                 │ (bounded loop)
//	                               └──── revise ─────┘
//
// The package knows nothing about HTTP, the scheduler or the engine: a Model
// makes each member's call. It is the errgroup runner the Phase 1 bake-off
// chose over eino, langgraphgo and trpc-agent-go
// (plans/agentic-council-chat.md), with the model's council settings from
// types/xollama wired in.
package council

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// Role is a council member's role.
type Role string

const (
	Planner     Role = xollama.RolePlanner
	Researcher  Role = xollama.RoleResearcher
	Critic      Role = xollama.RoleCritic
	Synthesizer Role = xollama.RoleSynthesizer
)

// Request is one model call made by one member.
type Request struct {
	Role  Role
	Index int
	Round int
	// Model is the role's own model, or "" for the council's.
	Model string
	// Host is the server that serves Model, or "" for this one.
	Host        string
	Messages    []api.Message
	Seed        int64
	Temperature float64
	MaxTokens   int
	// Think is the role's think setting (xollama.CouncilRole.Think), or ""
	// for none. ThinkBudget resolves it against the member's window.
	Think string
	// Format is a JSON schema the reply must follow, or nil for free text.
	Format json.RawMessage
}

// Model makes one member's call. onToken receives the reply as it arrives,
// from the calling goroutine only; the whole reply is returned. It must return
// promptly with ctx.Err() once ctx is done.
type Model interface {
	Stream(ctx context.Context, req Request, onToken func(string)) (string, error)
}

// Kind says how an event reaches the client.
type Kind uint8

const (
	Thinking Kind = iota
	Content
)

// Event is one streamed piece, tagged with the member that produced it.
type Event struct {
	Role  Role
	Index int
	Round int
	Kind  Kind
	Text  string
	// Done marks the member's last event: its reply is complete.
	Done bool
}

// Emit receives events. Run serializes it, so it is never called from two
// goroutines at once.
type Emit func(Event)

// Config is a council's settings for one turn.
type Config struct {
	Researchers int
	Critics     int
	// Temperature is the model's; the planner and synthesizer use it as is.
	Temperature float64
	// Jitter is the relative spread for researchers and critics: 0.02 draws
	// uniformly from [T·0.98, T·1.02].
	Jitter float64
	// Seed, when non-nil, makes a turn reproducible. Nil draws a random seed
	// per member per request.
	Seed             *int64
	MaxRounds        int
	ShowDeliberation bool
	MaxTokens        map[Role]int
	// Prompts replace a role's built-in instruction; Models serve a role on
	// another model.
	Prompts map[Role]string
	Models  map[Role]string
	// Hosts serve a role on another server (with its model in Models).
	Hosts map[Role]string
	// Think is each role's think setting; absent means no reasoning.
	Think map[Role]string
}

// Built-in defaults: the owner's specification, measured in Phase 0 and 1.
const (
	DefaultWidth  = 2
	DefaultJitter = 0.02
)

var defaultMaxTokens = map[Role]int{Planner: 512, Researcher: 384, Critic: 256, Synthesizer: 1024}

// FromModel resolves a model's council section against the defaults.
// temperature is the model's own, after request and Modelfile options.
func FromModel(c *xollama.Council, temperature float64) Config {
	cfg := Config{
		Researchers: DefaultWidth, Critics: DefaultWidth,
		Temperature: temperature, Jitter: DefaultJitter,
		MaxRounds: 1, ShowDeliberation: true,
		MaxTokens: map[Role]int{}, Prompts: map[Role]string{}, Models: map[Role]string{},
		Hosts: map[Role]string{}, Think: map[Role]string{},
	}
	for r, n := range defaultMaxTokens {
		cfg.MaxTokens[r] = n
	}
	if c == nil {
		return cfg
	}
	if c.TemperatureJitter != nil {
		cfg.Jitter = *c.TemperatureJitter
	}
	cfg.Seed = c.Seed
	if c.MaxRounds > 0 {
		cfg.MaxRounds = c.MaxRounds
	}
	if c.ShowDeliberation != nil {
		cfg.ShowDeliberation = *c.ShowDeliberation
	}
	for _, r := range []Role{Planner, Researcher, Critic, Synthesizer} {
		role := c.Role(string(r))
		if role == nil {
			continue
		}
		if role.Count > 0 {
			switch r {
			case Researcher:
				cfg.Researchers = role.Count
			case Critic:
				cfg.Critics = role.Count
			}
		}
		if role.MaxTokens > 0 {
			cfg.MaxTokens[r] = role.MaxTokens
		}
		if role.Prompt != "" {
			cfg.Prompts[r] = role.Prompt
		}
		if role.Model != "" {
			cfg.Models[r] = role.Model
		}
		if role.Host != "" {
			cfg.Hosts[r] = role.Host
		}
		if role.Think != "" {
			cfg.Think[r] = role.Think
		}
	}
	return cfg
}

// Charter returns the system prompt every member shares.
func Charter(c *xollama.Council) string {
	if c != nil && c.Charter != "" {
		return c.Charter
	}
	return DefaultCharter
}

// Draw is one member's sampling parameters.
type Draw struct {
	Seed        int64
	Temperature float64
}

// Draws holds every member's parameters for one turn, drawn up front so
// parallel members never share a random source.
type Draws struct {
	Decide, Direct, Plan, Synth Draw
	// Researchers[round][i], Critics[round][i]
	Researchers, Critics [][]Draw
}

// NewDraws draws the parameters for every call a turn can make.
func NewDraws(cfg Config) Draws {
	var src *rand.Rand
	if cfg.Seed != nil {
		src = rand.New(rand.NewPCG(uint64(*cfg.Seed), 0x636f756e63696c)) // "council"
	} else {
		src = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	seed := func() int64 { return int64(src.Uint32() >> 1) }
	fixed := func() Draw { return Draw{Seed: seed(), Temperature: cfg.Temperature} }
	jit := func() Draw {
		t := cfg.Temperature * (1 + cfg.Jitter*(2*src.Float64()-1))
		return Draw{Seed: seed(), Temperature: t}
	}
	rounds := max(cfg.MaxRounds, 1)
	d := Draws{
		Decide: fixed(), Direct: fixed(), Plan: fixed(), Synth: fixed(),
		Researchers: make([][]Draw, rounds), Critics: make([][]Draw, rounds),
	}
	for r := range rounds {
		for range cfg.Researchers {
			d.Researchers[r] = append(d.Researchers[r], jit())
		}
		for range cfg.Critics {
			d.Critics[r] = append(d.Critics[r], jit())
		}
	}
	return d
}

// Result is what a turn produced.
type Result struct {
	Route  string
	Answer string
	Rounds int
	Draws  Draws
}

// ErrNoConversation is returned for an empty conversation.
var ErrNoConversation = errors.New("council: empty conversation")

// Validate checks a config before a turn.
func (c Config) Validate() error {
	if c.Researchers < 1 || c.Critics < 1 {
		return fmt.Errorf("council: need at least one researcher and one critic, have %d/%d", c.Researchers, c.Critics)
	}
	if c.Jitter < 0 || c.Jitter >= 1 {
		return fmt.Errorf("council: jitter %v out of [0,1)", c.Jitter)
	}
	return nil
}

// ThinkBudget resolves a role's think setting to the tokens a member may spend
// reasoning, or 0 for none. "on" is xollama.DefaultCouncilThinkBudget; a
// level is its share of window,
// the member's context, by the same table a chat request's think level uses;
// a positive integer is a budget as it stands.
func ThinkBudget(setting string, window int) int {
	switch setting {
	case "", xollama.CouncilThinkOff:
		return 0
	case xollama.CouncilThinkOn:
		return xollama.DefaultCouncilThinkBudget
	}
	if n, err := strconv.Atoi(setting); err == nil {
		return max(n, 0)
	}
	return (&api.ThinkValue{Value: setting}).BudgetTokens(window)
}
