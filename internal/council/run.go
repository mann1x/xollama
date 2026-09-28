package council

import (
	"context"
	"maps"
	"strconv"
	"sync"
	"sync/atomic"

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
	// Notes is the turn's broadcast board and Seen how far each member has
	// read it (broadcast.go).
	Notes []Note
	Seen  map[string]int
	// Tests are the synthesizer's failed checks, one per test cycle ended
	// (11.4): each cycle after one reads them all. Replans[c-1] is the plan
	// the planner made for cycle c from them.
	Tests   []string
	Replans []Plan
	// Prior are the checks that failed before this turn's council began:
	// on earlier turns of the same work, and the front's own attempts this
	// turn (B, F). Every member reads them, ahead of the plan.
	Prior []string
	// Build is the builder's shaping of the council (build.go), kept with
	// the deliberation so later turns read its target.
	Build *Build
}

// RoundProgress is one round's research and review.
type RoundProgress struct {
	Findings  []string
	Critiques []string
}

func (p Progress) clone() Progress {
	out := Progress{Route: p.Route, Notes: append([]Note(nil), p.Notes...), Tests: append([]string(nil), p.Tests...), Prior: append([]string(nil), p.Prior...), Build: p.Build.clone()}
	if len(p.Seen) > 0 {
		out.Seen = maps.Clone(p.Seen)
	}
	if p.Plan != nil {
		pl := Plan{Plan: p.Plan.Plan, Briefs: append([]string(nil), p.Plan.Briefs...)}
		out.Plan = &pl
	}
	for _, pl := range p.Replans {
		out.Replans = append(out.Replans, Plan{Plan: pl.Plan, Briefs: append([]string(nil), pl.Briefs...)})
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
	if cfg.Turn == "" {
		cfg.Turn = strconv.Itoa(len(conv))
	}
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" {
			cfg.request = conv[i].Content
			break
		}
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
	if cfg.Broadcast {
		// Kept in the progress without a checkpoint of its own: the next
		// member that settles carries it.
		cfg.board = newBoard(p, func(notes []Note, seen map[string]int) {
			mu.Lock()
			p.Notes, p.Seen = notes, seen
			mu.Unlock()
		})
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
		res.Calls = once(cfg.Tools, res.Calls)
		for i := range res.Calls {
			res.Calls[i].Function.Index = i
		}
		res.Progress = p.clone()
		return res, true
	}

	route := p.Route
	if route == "" && cfg.previousBuild() != nil && len(p.Prior) == 0 {
		// B: the checks that failed on earlier turns of the same work come
		// with it; a rebuild, for new work, drops them below.
		mark(func() { p.Prior = cfg.previousPrior() })
	}
	if route == "" && cfg.fronted(m) {
		// The synthesizer takes the request first (front.go).
		route = RouteFront
		mark(func() { p.Route = route })
	}
	if route == RouteFront {
		key := MemberKey(Front, 0, 0)
		fcfg := cfg.apply(cfg.previousBuild())
		fcfg.build = p.Build
		ans, turns, next, err := front(ctx, m.(ToolModel), fcfg, d, conv, emit, p.Suspended[key], func(ctx context.Context) (*Build, error) {
			b, err := MakeBuild(ctx, m, cfg, d, conv, emit)
			if err == nil {
				mark(func() {
					p.Build = b.clone()
					p.Prior = dropEarlier(p.Prior)
				})
			}
			return b, err
		})
		if err != nil {
			return Result{Route: route, Draws: d}, err
		}
		if next == "" {
			answered(key, turns)
			if res, ok := suspended(Result{Route: "direct", Draws: d}, member{Front, key}); ok {
				return res, nil
			}
			return Result{Route: "direct", Answer: ans, Draws: d, Kept: keptWith(cfg.Previous, p.Build)}, nil
		}
		route = next
		report := fcfg.frontReport(turns)
		mark(func() {
			delete(p.Suspended, key)
			p.Route = route
			if report != "" {
				p.Prior = append(p.Prior, report)
			}
		})
	}
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
		// A direct answer keeps the council's last deliberation alive: a
		// "thanks" between two pieces of feedback does not end the work.
		return Result{Route: route, Answer: ans, Draws: d, Kept: cfg.Previous}, nil
	}

	if route == RouteContinue {
		// The synthesizer goes on from the deliberation it answered from,
		// taken into this turn's progress so it resumes from its own record.
		if p.Plan == nil {
			pl, last, ok := continuing(cfg.Previous)
			if !ok {
				return Result{Route: route, Draws: d}, errNothingToContinue
			}
			mark(func() { p.Plan, p.Rounds, p.Build = &pl, []RoundProgress{last}, cfg.Previous.Build.clone() })
		}
		cfg = cfg.apply(p.Build)
		last := p.Rounds[len(p.Rounds)-1]
		cfg.continuing = true
		// A continued synthesizer has no council behind it to send a failed
		// check back to.
		cfg.MaxTests = 0
		key := MemberKey(Synthesizer, 0, 0)
		ans, turns, err := synthesize(ctx, m, cfg, d, conv, *p.Plan, last.Findings, last.Critiques, emit, p.Suspended[key])
		if err != nil {
			return Result{Route: route, Draws: d}, err
		}
		answered(key, turns)
		if res, ok := suspended(Result{Route: route, Draws: d}, member{Synthesizer, key}); ok {
			return res, nil
		}
		return Result{Route: route, Answer: ans, Draws: d, Kept: Kept(p)}, nil
	}

	// The builder shapes the council the first time a request reaches it, and
	// again when the request is new work its target does not fit.
	if route == RouteRebuild {
		mark(func() { p.Prior = dropEarlier(p.Prior) })
	}
	if p.Build == nil {
		b := cfg.previousBuild()
		if b == nil || route == RouteRebuild {
			var err error
			if b, err = MakeBuild(ctx, m, cfg, d, conv, emit); err != nil {
				return Result{Route: route, Draws: d}, err
			}
		}
		if b != nil {
			mark(func() { p.Build = b.clone() })
		}
	}
	cfg = cfg.apply(p.Build)
	conv = withPrior(conv, p.Prior)
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
	// Each test cycle is a council round (or its revisions) and one
	// synthesizer; a failed check starts the next from its report.
	for cycle := 0; ; cycle++ {
		cfg.tests, cfg.cycleStart, cfg.first = p.Tests[:min(cycle, len(p.Tests))], round, p.Plan
		if cycle > 0 {
			critiques = nil
			// The planner schedules the work again from the failed checks.
			if len(p.Replans) < cycle {
				pl, err := Replan(ctx, m, cfg, d, conv, emit)
				if err != nil {
					return Result{Route: route, Rounds: round, Draws: d}, err
				}
				mark(func() { p.Replans = append(p.Replans, pl) })
			}
			plan = p.Replans[cycle-1]
		}
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
			// The first critic to confirm where the error is stops the others:
			// the synthesizer starts on it rather than wait for them.
			cctx, stop := context.WithCancel(ctx)
			var confirm atomic.Bool
			g, gctx = errgroup.WithContext(cctx)
			for i := range cfg.Critics {
				if critiques[i] != "" {
					continue
				}
				key := MemberKey(Critic, i, round)
				step = append(step, member{Critic, key})
				from := froms[key]
				g.Go(func() error {
					out, turns, err := critique(gctx, m, cfg, d, conv, plan, findings, i, round, emit, from)
					if err != nil && confirm.Load() && ctx.Err() == nil {
						out, turns, err = stoppedCritique, nil, nil
					}
					if err == nil {
						settle(key, turns, func() { critiques[i], p.Rounds[round].Critiques[i] = out, out })
						if _, ok := confirmed(out); ok && turns == nil {
							confirm.Store(true)
							stop()
						}
					}
					return err
				})
			}
			err := g.Wait()
			stop()
			if err != nil {
				return Result{Route: route, Draws: d}, err
			}
			if confirm.Load() {
				// A critic that was waiting on the client is not asked again.
				for i := range cfg.Critics {
					key := MemberKey(Critic, i, round)
					if _, waiting := p.Suspended[key]; waiting {
						settle(key, nil, func() { critiques[i], p.Rounds[round].Critiques[i] = stoppedCritique, stoppedCritique })
					}
				}
			}
			if res, ok := suspended(Result{Route: route, Draws: d}, step...); ok {
				return res, nil
			}
			if !NeedsRevision(cfg, critiques, round) {
				break
			}
		}
		if cycle < len(p.Tests) {
			// This cycle's check already failed: the turn resumes past it.
			round++
			continue
		}
		key := MemberKey(Synthesizer, 0, cycle)
		if cycle > 0 && p.Suspended[key] == nil {
			// The failed check before it streamed as content too.
			emit(Event{Role: Synthesizer, Round: cycle, Kind: Content, Text: "\n\n"})
		}
		ans, turns, err := synthesize(ctx, m, cfg, d, conv, plan, findings, critiques, emit, p.Suspended[key])
		if err != nil {
			return Result{Route: route, Rounds: round + 1, Draws: d}, err
		}
		answered(key, turns)
		if res, ok := suspended(Result{Route: route, Rounds: round + 1, Draws: d}, member{Synthesizer, key}); ok {
			return res, nil
		}
		if cfg.retested(ans, cycle) {
			mark(func() { p.Tests = append(p.Tests, ans) })
			round++
			continue
		}
		return Result{Route: route, Answer: withoutVerdict(ans), Rounds: round + 1, Draws: d, Kept: Kept(p)}, nil
	}
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
