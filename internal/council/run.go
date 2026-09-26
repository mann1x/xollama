package council

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/ollama/ollama/api"
)

// Run answers one turn. conv is the conversation as every member sends it:
// an empty system message, then the turns (plans/agentic-council-chat.md,
// 9.3). The charter and the client's system prompt come from cfg. The first member error cancels the
// others and is returned; so does ctx.
func Run(ctx context.Context, cfg Config, m Model, conv []api.Message, emit Emit) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	if len(conv) == 0 {
		return Result{}, ErrNoConversation
	}
	emit = serial(emit)
	d := NewDraws(cfg)

	route, err := Decide(ctx, m, cfg, d, conv)
	if err != nil {
		return Result{Draws: d}, err
	}
	if route == "direct" {
		ans, err := Direct(ctx, m, cfg, d, conv, emit)
		return Result{Route: route, Answer: ans, Draws: d}, err
	}

	plan, err := MakePlan(ctx, m, cfg, d, conv, emit)
	if err != nil {
		return Result{Route: route, Draws: d}, err
	}
	var findings, critiques []string
	round := 0
	for ; ; round++ {
		findings = make([]string, cfg.Researchers)
		g, gctx := errgroup.WithContext(ctx)
		for i := range cfg.Researchers {
			g.Go(func() (err error) {
				findings[i], err = Research(gctx, m, cfg, d, conv, plan, i, round, critiques, emit)
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return Result{Route: route, Draws: d}, err
		}
		critiques = make([]string, cfg.Critics)
		g, gctx = errgroup.WithContext(ctx)
		for i := range cfg.Critics {
			g.Go(func() (err error) {
				critiques[i], err = Critique(gctx, m, cfg, d, conv, plan, findings, i, round, emit)
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
