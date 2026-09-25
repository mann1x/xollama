// Package eino is the council as a cloudwego/eino compose.Graph: the route is
// a graph branch, each fan-out a multi-branch over pre-declared member slots
// with the library's map merge as the fan-in reducer, and the revise loop a
// cycle in a Pregel-mode graph. The graph is compiled once per slot count and
// reused by every request.
package eino

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/compose"

	"councileval/council"
)

// DefaultSlots is the member width the first graph is compiled for; a wider
// request compiles (once) and caches a graph with more slots.
const DefaultSlots = 8

// Runner compiles lazily and caches one graph per slot count. Native carries
// researcher and critic tokens through the graph as eino streams (see
// native.go) instead of handing emit to the members.
type Runner struct {
	Native bool
	graphs sync.Map // int -> *compiled
}

type compiled struct {
	once sync.Once
	r    compose.Runnable[*job, council.Result]
	err  error
}

func (r *Runner) Name() string {
	if r.Native {
		return "eino-native"
	}
	return "eino"
}

// job is the per-request input; the graph's state holds it for every node.
type job struct {
	cancel context.CancelCauseFunc
	cfg    council.Config
	m      council.Model
	conv   []council.Message
	emit   council.Emit
	d      council.Draws
	wg     sync.WaitGroup // native mode: stream producers and callback drainers
	drain  sync.WaitGroup // native mode: callback drainers only
}

// state is eino's per-run local state, reached through compose.ProcessState.
type state struct {
	*job
	plan      council.Plan
	findings  []string
	critiques []string
	round     int
}

// get reads the job and a snapshot of the run so far under the state lock.
func get(ctx context.Context) (s state, err error) {
	err = compose.ProcessState(ctx, func(_ context.Context, st *state) error { s = *st; return nil })
	return s, err
}

func put(ctx context.Context, f func(*state)) error {
	return compose.ProcessState(ctx, func(_ context.Context, st *state) error { f(st); return nil })
}

// fail cancels the siblings of a failing member: eino waits for the whole
// superstep and does not cancel the others itself.
func fail(j *job, err error) error {
	if err != nil {
		j.cancel(err)
	}
	return err
}

func researcher(i int) string { return fmt.Sprintf("researcher_%d", i) }
func critic(i int) string     { return fmt.Sprintf("critic_%d", i) }

func build(slots int, native bool) (compose.Runnable[*job, council.Result], error) {
	g := compose.NewGraph[*job, council.Result](compose.WithGenLocalState(func(context.Context) *state { return &state{} }))
	var errs []error
	add := func(err error) { errs = append(errs, err) }

	// decide: the job enters the state in the node's pre-handler.
	add(g.AddLambdaNode("decide", compose.InvokableLambda(func(ctx context.Context, j *job) (string, error) {
		return council.Decide(ctx, j.m, j.cfg, j.d, j.conv)
	}), compose.WithStatePreHandler(func(_ context.Context, j *job, s *state) (*job, error) { s.job = j; return j, nil })))

	add(g.AddLambdaNode("direct", compose.InvokableLambda(func(ctx context.Context, route string) (council.Result, error) {
		s, err := get(ctx)
		if err != nil {
			return council.Result{}, err
		}
		ans, err := council.Direct(ctx, s.m, s.cfg, s.d, s.conv, s.emit)
		return council.Result{Route: route, Answer: ans}, err
	})))

	// plan -> researchers: the value on every member edge is the round.
	add(g.AddLambdaNode("plan", compose.InvokableLambda(func(ctx context.Context, _ string) (int, error) {
		s, err := get(ctx)
		if err != nil {
			return 0, err
		}
		p, err := council.MakePlan(ctx, s.m, s.cfg, s.d, s.conv, s.emit)
		if err != nil {
			return 0, err
		}
		return 0, put(ctx, func(st *state) { st.plan = p })
	})))
	researchers := map[string]bool{}
	critics := map[string]bool{}
	for i := range slots {
		researchers[researcher(i)] = true
		critics[critic(i)] = true
	}
	// fanOut picks the first n slots: the runtime width of a static topology.
	fanOut := func(name func(int) string, n func(state) int) func(context.Context, int) (map[string]bool, error) {
		return func(ctx context.Context, _ int) (map[string]bool, error) {
			s, err := get(ctx)
			if err != nil {
				return nil, err
			}
			ends := make(map[string]bool, n(s))
			for i := range n(s) {
				ends[name(i)] = true
			}
			return ends, nil
		}
	}

	// Members return {index: text}; eino's default map merge is the fan-in reducer.
	for i := range slots {
		if native {
			add(g.AddLambdaNode(researcher(i), compose.StreamableLambda(streamMember(i, council.Researcher)), compose.WithNodeName(researcher(i))))
			add(g.AddLambdaNode(critic(i), compose.StreamableLambda(streamMember(i, council.Critic)), compose.WithNodeName(critic(i))))
			continue
		}
		add(g.AddLambdaNode(researcher(i), compose.InvokableLambda(func(ctx context.Context, round int) (map[int]string, error) {
			s, err := get(ctx)
			if err != nil {
				return nil, err
			}
			f, err := council.Research(ctx, s.m, s.cfg, s.d, s.conv, s.plan, i, round, s.critiques, s.emit)
			return map[int]string{i: f}, fail(s.job, err)
		})))
		add(g.AddLambdaNode(critic(i), compose.InvokableLambda(func(ctx context.Context, round int) (map[int]string, error) {
			s, err := get(ctx)
			if err != nil {
				return nil, err
			}
			c, err := council.Critique(ctx, s.m, s.cfg, s.d, s.conv, s.plan, s.findings, i, round, s.emit)
			return map[int]string{i: c}, fail(s.job, err)
		})))
	}

	ordered := func(m map[int]string, n int) ([]string, error) {
		out := make([]string, n)
		for i := range n {
			v, ok := m[i]
			if !ok {
				return nil, fmt.Errorf("eino: member %d missing from the fan-in", i)
			}
			out[i] = v
		}
		return out, nil
	}
	add(g.AddLambdaNode("findings", compose.InvokableLambda(func(ctx context.Context, m map[int]string) (int, error) {
		round := 0
		return round, compose.ProcessState(ctx, func(_ context.Context, st *state) (err error) {
			round = st.round
			st.findings, err = ordered(m, st.cfg.Researchers)
			return err
		})
	})))

	// verdict: the loop's back edge is a branch to the researcher slots.
	add(g.AddLambdaNode("verdict", compose.InvokableLambda(func(ctx context.Context, m map[int]string) (int, error) {
		round := 0
		return round, compose.ProcessState(ctx, func(_ context.Context, st *state) (err error) {
			if st.critiques, err = ordered(m, st.cfg.Critics); err != nil {
				return err
			}
			if council.NeedsRevision(st.cfg, st.critiques, st.round) {
				st.round++
				round = st.round
			} else {
				round = -1
			}
			return nil
		})
	})))
	loopEnds := maps(researchers, map[string]bool{"synthesize": true})

	add(g.AddLambdaNode("synthesize", compose.InvokableLambda(func(ctx context.Context, _ int) (council.Result, error) {
		s, err := get(ctx)
		if err != nil {
			return council.Result{}, err
		}
		// Native mode: the tapped member streams drain at the consumer's pace,
		// so the answer must wait or deliberation lands after it.
		s.drain.Wait()
		ans, err := council.Synthesize(ctx, s.m, s.cfg, s.d, s.conv, s.plan, s.findings, s.critiques, s.emit)
		return council.Result{Route: "council", Answer: ans, Rounds: s.round + 1}, err
	})))

	// Edges and branches only after every node exists: eino rejects a
	// forward reference, and the first error sticks to the graph.
	add(g.AddEdge(compose.START, "decide"))
	add(g.AddBranch("decide", compose.NewGraphBranch(func(_ context.Context, route string) (string, error) {
		if route == "direct" {
			return "direct", nil
		}
		return "plan", nil
	}, map[string]bool{"direct": true, "plan": true})))
	add(g.AddEdge("direct", compose.END))
	add(g.AddBranch("plan", compose.NewGraphMultiBranch(fanOut(researcher, func(s state) int { return s.cfg.Researchers }), researchers)))
	for i := range slots {
		add(g.AddEdge(researcher(i), "findings"))
		add(g.AddEdge(critic(i), "verdict"))
	}
	add(g.AddBranch("findings", compose.NewGraphMultiBranch(fanOut(critic, func(s state) int { return s.cfg.Critics }), critics)))
	add(g.AddBranch("verdict", compose.NewGraphMultiBranch(func(ctx context.Context, round int) (map[string]bool, error) {
		if round < 0 {
			return map[string]bool{"synthesize": true}, nil
		}
		return fanOut(researcher, func(s state) int { return s.cfg.Researchers })(ctx, round)
	}, loopEnds)))
	add(g.AddEdge("synthesize", compose.END))

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	// AnyPredecessor (Pregel) is the default and the only mode that allows the
	// cycle; the superstep barrier is what makes the fan-in wait for all members.
	return g.Compile(context.Background(), compose.WithNodeTriggerMode(compose.AnyPredecessor), compose.WithGraphName("council"))
}

func maps(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func (r *Runner) graph(width int) (compose.Runnable[*job, council.Result], error) {
	slots := max(DefaultSlots, width)
	v, _ := r.graphs.LoadOrStore(slots, &compiled{})
	c := v.(*compiled)
	c.once.Do(func() { c.r, c.err = build(slots, r.Native) })
	return c.r, c.err
}

func (r *Runner) Run(ctx context.Context, cfg council.Config, m council.Model, conv []council.Message, emit council.Emit) (council.Result, error) {
	if err := cfg.Validate(); err != nil {
		return council.Result{}, err
	}
	if len(conv) == 0 {
		return council.Result{}, council.ErrNoConversation
	}
	g, err := r.graph(max(cfg.Researchers, cfg.Critics))
	if err != nil {
		return council.Result{}, err
	}
	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	j := &job{cancel: cancel, cfg: cfg, m: m, conv: conv, emit: council.Serial(emit), d: council.NewDraws(cfg)}
	// decide, plan, 4 supersteps per round, synthesize, END; the compiled
	// default (nodes+10) is a node count, not an iteration bound.
	steps := 3 + 4*max(cfg.MaxRounds, 1) + 2
	opts := []compose.Option{compose.WithRuntimeMaxSteps(steps)}
	if r.Native {
		opts = append(opts, compose.WithCallbacks(forwarder(j)))
	}
	res, err := g.Invoke(rctx, j, opts...)
	if r.Native {
		// Producers and drainers outlive their node: the graph returns on
		// the concatenated value, not on the goroutines behind the stream.
		cancel(nil)
		j.wg.Wait()
	}
	if err != nil {
		// A member's own failure beats the context.Canceled its siblings saw.
		if cause := context.Cause(rctx); cause != nil && !errors.Is(cause, context.Canceled) {
			return council.Result{}, cause
		}
		return council.Result{}, err
	}
	return res, nil
}
