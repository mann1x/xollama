package council

import (
	"context"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/ollama/ollama/api"
)

// Progress is what a turn has finished, so a turn that broke off resumes
// rather than starts over (council_chat_state_v1). Findings and Critiques hold
// one slot per member, "" for one not yet done; each round is one entry.
type Progress struct {
	Route  string
	Plan   *Plan
	Rounds []RoundProgress
}

// RoundProgress is one round's research and review.
type RoundProgress struct {
	Findings  []string
	Critiques []string
}

func (p Progress) clone() Progress {
	out := Progress{Route: p.Route}
	if p.Plan != nil {
		pl := Plan{Plan: p.Plan.Plan, Briefs: append([]string(nil), p.Plan.Briefs...)}
		out.Plan = &pl
	}
	for _, r := range p.Rounds {
		out.Rounds = append(out.Rounds, RoundProgress{
			Findings: append([]string(nil), r.Findings...), Critiques: append([]string(nil), r.Critiques...),
		})
	}
	return out
}

// Run answers one turn. conv is the conversation as every member sends it:
// an empty system message, then the turns (plans/agentic-council-chat.md,
// 9.3). The charter and the client's system prompt come from cfg. The first
// member error cancels the others and is returned; so does ctx.
func Run(ctx context.Context, cfg Config, m Model, conv []api.Message, emit Emit) (Result, error) {
	return RunFrom(ctx, cfg, m, conv, Progress{}, nil, emit)
}

// RunFrom answers one turn from what from records as done: the route, the
// plan, and each member's reply, which are used as they are and not asked
// again. checkpoint, when set, receives the progress each time a member
// finishes -- the points a broken-off turn can resume from. It is called
// from one goroutine at a time.
func RunFrom(ctx context.Context, cfg Config, m Model, conv []api.Message, from Progress, checkpoint func(Progress), emit Emit) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	if len(conv) == 0 {
		return Result{}, ErrNoConversation
	}
	emit = serial(emit)
	d := NewDraws(cfg)
	p := from.clone()
	var mu sync.Mutex
	mark := func(update func()) {
		mu.Lock()
		defer mu.Unlock()
		update()
		if checkpoint != nil {
			checkpoint(p.clone())
		}
	}

	route := p.Route
	if route == "" {
		var err error
		if route, err = Decide(ctx, m, cfg, d, conv); err != nil {
			return Result{Draws: d}, err
		}
		mark(func() { p.Route = route })
	}
	if route == "direct" {
		ans, err := Direct(ctx, m, cfg, d, conv, emit)
		return Result{Route: route, Answer: ans, Draws: d}, err
	}

	if p.Plan == nil {
		plan, err := MakePlan(ctx, m, cfg, d, conv, emit)
		if err != nil {
			return Result{Route: route, Draws: d}, err
		}
		mark(func() { p.Plan = &plan })
	}
	plan := *p.Plan
	var findings, critiques []string
	round := 0
	for ; ; round++ {
		mu.Lock()
		for len(p.Rounds) <= round {
			p.Rounds = append(p.Rounds, RoundProgress{})
		}
		p.Rounds[round].Findings = fit(p.Rounds[round].Findings, cfg.Researchers)
		p.Rounds[round].Critiques = fit(p.Rounds[round].Critiques, cfg.Critics)
		mu.Unlock()
		findings = append([]string(nil), p.Rounds[round].Findings...)
		g, gctx := errgroup.WithContext(ctx)
		for i := range cfg.Researchers {
			if findings[i] != "" {
				continue
			}
			g.Go(func() error {
				out, err := Research(gctx, m, cfg, d, conv, plan, i, round, critiques, emit)
				if err == nil {
					mark(func() { findings[i], p.Rounds[round].Findings[i] = out, out })
				}
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return Result{Route: route, Draws: d}, err
		}
		critiques = append([]string(nil), p.Rounds[round].Critiques...)
		g, gctx = errgroup.WithContext(ctx)
		for i := range cfg.Critics {
			if critiques[i] != "" {
				continue
			}
			g.Go(func() error {
				out, err := Critique(gctx, m, cfg, d, conv, plan, findings, i, round, emit)
				if err == nil {
					mark(func() { critiques[i], p.Rounds[round].Critiques[i] = out, out })
				}
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return Result{Route: route, Draws: d}, err
		}
		if !NeedsRevision(cfg, critiques, round) {
			break
		}
	}
	ans, err := Synthesize(ctx, m, cfg, d, conv, plan, findings, critiques, emit)
	return Result{Route: route, Answer: ans, Rounds: round + 1, Draws: d}, err
}

// fit sizes a round's slots to the council's width: a record from a council
// configured wider or narrower keeps what still has a member to own it.
func fit(s []string, n int) []string {
	out := make([]string, n)
	copy(out, s)
	return out
}
