// Package council is the shared core of the Phase 1 bake-off: the types, the
// prompts, the per-role sampling draws and the steps of the council. Every
// candidate library orchestrates these same steps, so the comparison measures
// orchestration and nothing else.
//
// The flow (plans/agentic-council-chat.md):
//
//	decide (route-only) ─ direct ─► answer, streamed as content
//	        └ council ─► plan ─► researchers ∥ ─► critics ∥ ─► synthesizer
//	                               ▲                 │ (optional bounded loop)
//	                               └──── revise ─────┘
package council

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
)

// Role is a council member's role.
type Role string

const (
	Planner     Role = "planner"
	Researcher  Role = "researcher"
	Critic      Role = "critic"
	Synthesizer Role = "synthesizer"
)

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is one model call made by one member.
type Request struct {
	Role        Role
	Index       int
	Round       int
	Messages    []Message
	Seed        int64
	Temperature float64
	MaxTokens   int
	// RouteOnly asks for {"route":"direct"|"council"} under a JSON-schema grammar.
	RouteOnly bool
	// WantBriefs asks for a plan with this many researcher briefs, as JSON.
	WantBriefs int
}

// Model is what the council calls. onToken receives every piece as it arrives
// and may be called from the calling goroutine only; Stream returns the whole
// text. It must return promptly with ctx.Err() once ctx is done.
type Model interface {
	Stream(ctx context.Context, req Request, onToken func(string)) (string, error)
}

// Kind says how an event reaches the client: deliberation travels as
// thinking, the answer as content.
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
}

// Emit receives events. Members run in parallel, so an Emit may be called
// from several goroutines at once; the runner must serialize (see Serial).
type Emit func(Event)

// Config is the council section of a model's configuration.
type Config struct {
	Researchers int
	Critics     int
	Temperature float64
	// Jitter is the relative temperature spread for researchers and critics:
	// 0.02 draws uniformly from [T·0.98, T·1.02].
	Jitter float64
	// Seed, when non-nil, makes a run reproducible: every member's seed is
	// derived from it. Nil draws a random seed per member per request.
	Seed      *int64
	MaxRounds int
	// ShowDeliberation false drops the thinking events; the answer is unchanged.
	ShowDeliberation bool
	MaxTokens        map[Role]int
}

// Defaults is the owner's specification: 2 researchers, 2 critics, ±2 %.
func Defaults() Config {
	return Config{Researchers: 2, Critics: 2, Temperature: 0.7, Jitter: 0.02, MaxRounds: 1,
		ShowDeliberation: true,
		MaxTokens:        map[Role]int{Planner: 512, Researcher: 384, Critic: 256, Synthesizer: 512}}
}

// Draw is one member's sampling parameters.
type Draw struct {
	Seed        int64
	Temperature float64
}

// Draws holds every member's parameters for one request, drawn up front so
// parallel members never share a random source.
type Draws struct {
	Decide, Direct, Plan, Synth Draw
	// Researchers[round][i], Critics[round][i]
	Researchers, Critics [][]Draw
}

// NewDraws draws the parameters for every call a request can make.
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
	d := Draws{Decide: fixed(), Direct: fixed(), Plan: fixed(), Synth: fixed(),
		Researchers: make([][]Draw, rounds), Critics: make([][]Draw, rounds)}
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

// Result is what a run produced.
type Result struct {
	Route  string
	Answer string
	Rounds int
}

// Runner is one orchestration of the council: the baseline or a library.
type Runner interface {
	Name() string
	Run(ctx context.Context, cfg Config, m Model, conv []Message, emit Emit) (Result, error)
}

// ErrNoConversation is returned for an empty conversation.
var ErrNoConversation = errors.New("council: empty conversation")

// Validate checks a config before a run.
func (c Config) Validate() error {
	if c.Researchers < 1 || c.Critics < 1 {
		return fmt.Errorf("council: need at least one researcher and one critic, have %d/%d", c.Researchers, c.Critics)
	}
	if c.Jitter < 0 || c.Jitter >= 1 {
		return fmt.Errorf("council: jitter %v out of [0,1)", c.Jitter)
	}
	return nil
}
