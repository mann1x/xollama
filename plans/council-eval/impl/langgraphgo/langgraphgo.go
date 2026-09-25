// Package langgraphgo is the council on github.com/smallnest/langgraphgo's
// graph package: a typed StateGraph whose route is a conditional edge, whose
// researchers and critics are parallel nodes of one superstep, and whose
// revision round is a cycle back into the researchers.
//
// The fan-out width is part of the graph's topology in this library (a node
// is a name, and a typed state cannot return a Command to Goto a runtime list
// of them), so one graph is compiled per (researchers, critics) shape and
// cached on the Runner. See NOTES.md.
package langgraphgo

import (
	"context"
	"fmt"
	"sync"

	"github.com/smallnest/langgraphgo/graph"

	"councileval/council"
)

// run is everything request-scoped. It rides in the state as a pointer, so
// the compiled graph holds no per-request closures and can be shared.
type run struct {
	cfg    council.Config
	m      council.Model
	conv   []council.Message
	emit   council.Emit
	d      council.Draws
	cancel context.CancelCauseFunc
	// partial is what Run returns if the answering node fails, as the
	// baseline does: written by that node before it streams.
	partial council.Result
}

// out is one parallel member's contribution; the merger puts it in its slot,
// so the library's random fan-out order cannot reorder findings.
type out struct {
	role council.Role
	i    int
	text string
}

type state struct {
	run       *run
	Route     string
	Plan      council.Plan
	Round     int
	Findings  []string
	Critiques []string
	Prior     []string
	Answer    string
	Rounds    int
	out       *out
}

type shape struct{ r, c int }

// Runner compiles one graph per shape, once, and reuses it for every request.
type Runner struct {
	mu     sync.Mutex
	graphs map[shape]*graph.StateRunnable[state]
}

func (*Runner) Name() string { return "langgraphgo" }

func (r *Runner) compiled(s shape) (*graph.StateRunnable[state], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g := r.graphs[s]; g != nil {
		return g, nil
	}
	g, err := build(s)
	if err != nil {
		return nil, err
	}
	if r.graphs == nil {
		r.graphs = map[shape]*graph.StateRunnable[state]{}
	}
	r.graphs[s] = g
	return g, nil
}

func (r *Runner) Run(ctx context.Context, cfg council.Config, m council.Model, conv []council.Message, emit council.Emit) (council.Result, error) {
	if err := cfg.Validate(); err != nil {
		return council.Result{}, err
	}
	if len(conv) == 0 {
		return council.Result{}, council.ErrNoConversation
	}
	g, err := r.compiled(shape{cfg.Researchers, cfg.Critics})
	if err != nil {
		return council.Result{}, err
	}
	// The library neither cancels siblings when a node fails nor reports the
	// failure first (it returns the lowest-index error of the superstep, which
	// may be a sibling's context.Canceled): a cause-carrying ctx does both.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	rs := &run{cfg: cfg, m: m, conv: conv, emit: council.Serial(emit), d: council.NewDraws(cfg), cancel: cancel}

	final, err := g.Invoke(ctx, state{run: rs})
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		return rs.partial, err
	}
	return council.Result{Route: final.Route, Answer: final.Answer, Rounds: final.Rounds}, nil
}

func research(i int) string { return fmt.Sprintf("research/%d", i) }
func critic(i int) string   { return fmt.Sprintf("critic/%d", i) }

// node adds what every node needs and the library does not do: stop at a
// superstep boundary once ctx is done, and cancel the siblings on failure.
func node(fn func(context.Context, state) (state, error)) func(context.Context, state) (state, error) {
	return func(ctx context.Context, s state) (state, error) {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		ns, err := fn(ctx, s)
		if err != nil {
			s.run.cancel(err)
		}
		return ns, err
	}
}

func build(sh shape) (*graph.StateRunnable[state], error) {
	g := graph.NewStateGraph[state]()

	g.AddNode("decide", "route-only decision", node(func(ctx context.Context, s state) (state, error) {
		r := s.run
		route, err := council.Decide(ctx, r.m, r.cfg, r.d, r.conv)
		s.Route = route
		return s, err
	}))
	g.AddConditionalEdge("decide", func(_ context.Context, s state) string {
		if s.Route == "direct" {
			return "direct"
		}
		return "plan"
	})

	g.AddNode("direct", "answer a trivial message", node(func(ctx context.Context, s state) (state, error) {
		r := s.run
		r.partial = council.Result{Route: s.Route}
		ans, err := council.Direct(ctx, r.m, r.cfg, r.d, r.conv, r.emit)
		s.Answer = ans
		return s, err
	}))
	g.AddEdge("direct", graph.END)

	g.AddNode("plan", "plan and briefs", node(func(ctx context.Context, s state) (state, error) {
		r := s.run
		p, err := council.MakePlan(ctx, r.m, r.cfg, r.d, r.conv, r.emit)
		s.Plan, s.Round = p, 0
		s.Findings, s.Critiques = make([]string, sh.r), make([]string, sh.c)
		return s, err
	}))
	g.AddNode("revise", "start another round", node(func(_ context.Context, s state) (state, error) {
		s.Round++
		s.Prior = s.Critiques
		s.Findings, s.Critiques = make([]string, sh.r), make([]string, sh.c)
		return s, nil
	}))

	for i := range sh.r {
		g.AddNode(research(i), "researcher", node(func(ctx context.Context, s state) (state, error) {
			r := s.run
			f, err := council.Research(ctx, r.m, r.cfg, r.d, r.conv, s.Plan, i, s.Round, s.Prior, r.emit)
			return state{out: &out{council.Researcher, i, f}}, err
		}))
		g.AddEdge("plan", research(i))
		g.AddEdge("revise", research(i))
		// Fan-in: the next superstep's node set is deduplicated, so every
		// researcher pointing at every critic yields each critic once.
		for j := range sh.c {
			g.AddEdge(research(i), critic(j))
		}
	}
	for j := range sh.c {
		g.AddNode(critic(j), "critic", node(func(ctx context.Context, s state) (state, error) {
			r := s.run
			c, err := council.Critique(ctx, r.m, r.cfg, r.d, r.conv, s.Plan, s.Findings, j, s.Round, r.emit)
			return state{out: &out{council.Critic, j, c}}, err
		}))
		// Evaluated once per critic over the same merged state, so all agree.
		g.AddConditionalEdge(critic(j), func(_ context.Context, s state) string {
			if council.NeedsRevision(s.run.cfg, s.Critiques, s.Round) {
				return "revise"
			}
			return "synthesize"
		})
	}

	g.AddNode("synthesize", "write the answer", node(func(ctx context.Context, s state) (state, error) {
		r := s.run
		r.partial = council.Result{Route: s.Route, Rounds: s.Round + 1}
		ans, err := council.Synthesize(ctx, r.m, r.cfg, r.d, r.conv, s.Plan, s.Findings, s.Critiques, r.emit)
		s.Answer, s.Rounds = ans, s.Round+1
		return s, err
	}))
	g.AddEdge("synthesize", graph.END)

	// The reducer. Without one the library keeps the last result of a
	// superstep, and that order is a map iteration, so all but one random
	// member's work would be lost.
	g.SetStateMerger(func(_ context.Context, cur state, news []state) (state, error) {
		for _, n := range news {
			switch {
			case n.out == nil:
				cur = n
			case n.out.role == council.Researcher:
				cur.Findings[n.out.i] = n.out.text
			default:
				cur.Critiques[n.out.i] = n.out.text
			}
		}
		return cur, nil
	})
	g.SetEntryPoint("decide")
	return g.Compile()
}
