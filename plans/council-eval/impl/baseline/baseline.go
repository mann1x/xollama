// Package baseline is the council on errgroup and nothing else: the bar every
// library has to clear.
package baseline

import (
	"context"

	"golang.org/x/sync/errgroup"

	"councileval/council"
)

type Runner struct{}

func (Runner) Name() string { return "baseline" }

func (Runner) Run(ctx context.Context, cfg council.Config, m council.Model, conv []council.Message, emit council.Emit) (council.Result, error) {
	if err := cfg.Validate(); err != nil {
		return council.Result{}, err
	}
	if len(conv) == 0 {
		return council.Result{}, council.ErrNoConversation
	}
	emit = council.Serial(emit)
	d := council.NewDraws(cfg)

	route, err := council.Decide(ctx, m, cfg, d, conv)
	if err != nil {
		return council.Result{}, err
	}
	if route == "direct" {
		ans, err := council.Direct(ctx, m, cfg, d, conv, emit)
		return council.Result{Route: route, Answer: ans}, err
	}

	plan, err := council.MakePlan(ctx, m, cfg, d, conv, emit)
	if err != nil {
		return council.Result{}, err
	}
	var findings, critiques []string
	round := 0
	for ; ; round++ {
		findings = make([]string, cfg.Researchers)
		g, gctx := errgroup.WithContext(ctx)
		for i := range cfg.Researchers {
			g.Go(func() (err error) {
				findings[i], err = council.Research(gctx, m, cfg, d, conv, plan, i, round, critiques, emit)
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return council.Result{}, err
		}
		critiques = make([]string, cfg.Critics)
		g, gctx = errgroup.WithContext(ctx)
		for i := range cfg.Critics {
			g.Go(func() (err error) {
				critiques[i], err = council.Critique(gctx, m, cfg, d, conv, plan, findings, i, round, emit)
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return council.Result{}, err
		}
		if !council.NeedsRevision(cfg, critiques, round) {
			break
		}
	}
	ans, err := council.Synthesize(ctx, m, cfg, d, conv, plan, findings, critiques, emit)
	return council.Result{Route: route, Answer: ans, Rounds: round + 1}, err
}
