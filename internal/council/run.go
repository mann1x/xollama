package council

import (
	"context"
	"maps"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/ollama/ollama/api"
)

// Progress is what a turn has finished, so a turn that broke off resumes
// rather than starts over (council_chat_state_v1). Findings and Critiques hold
// one slot per member, "" for one not yet done; each round is one entry.
// Suspended holds each member waiting on the client's tool results, by
// MemberKey: its own turns so far, each with the calls it made.
type Progress struct {
	Route     string
	Plan      *Plan
	Rounds    []RoundProgress
	Suspended map[string][]api.Message
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
	if len(p.Suspended) > 0 {
		out.Suspended = make(map[string][]api.Message, len(p.Suspended))
		for k, v := range p.Suspended {
			out.Suspended[k] = clone(v)
		}
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
// again, and each suspended member, which continues from its turns with the
// results in cfg.Results. checkpoint, when set, receives the progress each
// time a member finishes or suspends -- the points a broken-off turn can
// resume from. It is called from one goroutine at a time.
//
// A step whose members call tools ends the turn there: every member of the
// step runs to its reply or its calls, and the Result carries the calls and
// the progress to resume from.
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
	// settle records one member's outcome: its reply, or its turns while it
	// waits on the client.
	settle := func(key string, turns []api.Message, done func()) {
		mark(func() {
			if turns != nil {
				if p.Suspended == nil {
					p.Suspended = map[string][]api.Message{}
				}
				p.Suspended[key] = turns
				return
			}
			delete(p.Suspended, key)
			done()
		})
	}
	// answered records the member that answers the user: a checkpoint when it
	// suspends, or to clear the suspension it resumed from. Its answer itself
	// is the done chunk's, not a checkpoint's.
	answered := func(key string, turns []api.Message) {
		mu.Lock()
		_, was := p.Suspended[key]
		mu.Unlock()
		if turns != nil || was {
			settle(key, turns, func() {})
		}
	}
	// resumes is where each member of a step picks up, read before any of
	// them runs: a member that finishes deletes its own entry meanwhile.
	resumes := func() map[string][]api.Message {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(p.Suspended)
	}
	// suspended ends the turn at a step whose members wait on the client,
	// with their calls in member order.
	suspended := func(res Result, members ...member) (Result, bool) {
		for _, mb := range members {
			res.Calls = append(res.Calls, cfg.forwarded(mb.role, mb.key, p.Suspended[mb.key])...)
		}
		if len(res.Calls) == 0 {
			return res, false
		}
		for i := range res.Calls {
			res.Calls[i].Function.Index = i
		}
		res.Progress = p.clone()
		return res, true
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
		key := MemberKey(Planner, 0, 0)
		ans, turns, err := direct(ctx, m, cfg, d, conv, emit, p.Suspended[key])
		if err != nil {
			return Result{Route: route, Draws: d}, err
		}
		answered(key, turns)
		if res, ok := suspended(Result{Route: route, Draws: d}, member{Planner, key}); ok {
			return res, nil
		}
		return Result{Route: route, Answer: ans, Draws: d}, nil
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
		froms := resumes()
		var step []member
		g, gctx := errgroup.WithContext(ctx)
		for i := range cfg.Researchers {
			if findings[i] != "" {
				continue
			}
			key := MemberKey(Researcher, i, round)
			step = append(step, member{Researcher, key})
			from := froms[key]
			g.Go(func() error {
				out, turns, err := research(gctx, m, cfg, d, conv, plan, i, round, critiques, emit, from)
				if err == nil {
					settle(key, turns, func() { findings[i], p.Rounds[round].Findings[i] = out, out })
				}
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return Result{Route: route, Draws: d}, err
		}
		if res, ok := suspended(Result{Route: route, Draws: d}, step...); ok {
			return res, nil
		}
		critiques = append([]string(nil), p.Rounds[round].Critiques...)
		froms = resumes()
		step = nil
		g, gctx = errgroup.WithContext(ctx)
		for i := range cfg.Critics {
			if critiques[i] != "" {
				continue
			}
			key := MemberKey(Critic, i, round)
			step = append(step, member{Critic, key})
			from := froms[key]
			g.Go(func() error {
				out, turns, err := critique(gctx, m, cfg, d, conv, plan, findings, i, round, emit, from)
				if err == nil {
					settle(key, turns, func() { critiques[i], p.Rounds[round].Critiques[i] = out, out })
				}
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return Result{Route: route, Draws: d}, err
		}
		if res, ok := suspended(Result{Route: route, Draws: d}, step...); ok {
			return res, nil
		}
		if !NeedsRevision(cfg, critiques, round) {
			break
		}
	}
	key := MemberKey(Synthesizer, 0, 0)
	ans, turns, err := synthesize(ctx, m, cfg, d, conv, plan, findings, critiques, emit, p.Suspended[key])
	if err != nil {
		return Result{Route: route, Rounds: round + 1, Draws: d}, err
	}
	answered(key, turns)
	if res, ok := suspended(Result{Route: route, Rounds: round + 1, Draws: d}, member{Synthesizer, key}); ok {
		return res, nil
	}
	return Result{Route: route, Answer: ans, Rounds: round + 1, Draws: d}, nil
}

// member names one member of a step: its role, for the tool policy, and its key.
type member struct {
	role Role
	key  string
}

// fit sizes a round's slots to the council's width: a record from a council
// configured wider or narrower keeps what still has a member to own it.
func fit(s []string, n int) []string {
	out := make([]string, n)
	copy(out, s)
	return out
}
