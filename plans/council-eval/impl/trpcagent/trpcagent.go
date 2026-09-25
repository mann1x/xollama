// Package trpcagent is the council on trpc-agent-go's graph package: a
// StateGraph compiled once per Runner and run by a shared graph.Executor
// (Pregel/BSP supersteps). The route is a conditional edge, each fan-out is a
// node returning []*graph.Command (one task per member, width chosen at run
// time), the per-member results merge through schema reducers, and the
// critic→researcher revision loop is a cycle closed by a conditional edge.
package trpcagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/graph"

	"councileval/council"
)

// State keys.
const (
	kRun       = "council_run" // *run: per-request dependencies (model, cfg, draws, emit)
	kConv      = "conv"        // []council.Message
	kRoute     = "route"       // string
	kPlan      = "plan"        // council.Plan
	kRound     = "round"       // int
	kMember    = "member"      // int, set per fan-out task by its Command overlay
	kFindings  = "findings"    // []string, one slot per researcher
	kCritiques = "critiques"   // []string, one slot per critic
	kRevise    = "revise"      // bool
	kAnswer    = "answer"      // string
)

// Node IDs.
const (
	nDecide, nDirect, nPlan = "decide", "direct", "plan"
	nFanResearch, nResearch = "fan_research", "research"
	nFanCritics, nCritic    = "fan_critics", "critic"
	nGate, nSynth           = "gate", "synthesize"
	eventType               = "council"
	maxWidth, maxSteps      = 64, 1000
)

// Runner runs the council on a graph compiled once, on first use.
type Runner struct {
	// Native carries member tokens through the executor's event channel
	// (graph.EventEmitter.EmitCustom) instead of calling emit from the node.
	Native bool

	once sync.Once
	exec *graph.Executor
	err  error
}

// New returns a Runner that streams through closures.
func New() *Runner { return &Runner{} }

func (r *Runner) Name() string { return "trpc-agent-go" }

// slot is one member's result, merged into its index by slotReducer.
type slot struct {
	I int
	S string
}

// slotReducer puts a member's result at its index, so critics and the
// synthesizer read them in member order whatever order they finished in.
func slotReducer(existing, update any) any {
	u, ok := update.(slot)
	if !ok {
		return update
	}
	old, _ := existing.([]string)
	out := make([]string, max(len(old), u.I+1))
	copy(out, old)
	out[u.I] = u.S
	return out
}

// run is one request's dependencies. It travels in the state under kRun with
// deep copy disabled; DeepCopy returns itself because the executor's final
// state snapshot deep-copies every key regardless of DisableDeepCopy.
type run struct {
	cfg    council.Config
	m      council.Model
	emit   council.Emit
	d      council.Draws
	native bool
	cancel context.CancelCauseFunc

	mu  sync.Mutex
	err error
}

func (r *run) DeepCopy() any                { return r }
func (r *run) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// fail records the first error and cancels every sibling still running: the
// executor waits for all tasks of a superstep and cancels none of them.
func (r *run) fail(err error) error {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
		r.cancel(err)
	}
	r.mu.Unlock()
	return err
}

func (r *run) firstErr() error { r.mu.Lock(); defer r.mu.Unlock(); return r.err }

// emitter is where a member's events go: straight to the serialized emit, or
// into the executor's event channel for Run to forward.
func (r *run) emitter(ctx context.Context, s graph.State) council.Emit {
	if !r.native {
		return r.emit
	}
	em := graph.GetEventEmitterWithContext(ctx, s)
	return func(e council.Event) { _ = em.EmitCustom(eventType, e) }
}

func get[T any](s graph.State, k string) T { v, _ := graph.GetStateValue[T](s, k); return v }

// node adapts a council step to a graph node: it pulls the run out of the
// state and turns a failure into a cancellation of the whole run.
func node(f func(ctx context.Context, r *run, s graph.State) (any, error)) graph.NodeFunc {
	return func(ctx context.Context, s graph.State) (any, error) {
		r := get[*run](s, kRun)
		out, err := f(ctx, r, s)
		if err != nil {
			return nil, r.fail(err)
		}
		return out, nil
	}
}

// fanOut sends one task per member to target, each with its index in the
// overlay. The width is read from the request's config at run time.
func fanOut(target string, width func(council.Config) int) graph.NodeFunc {
	return node(func(_ context.Context, r *run, _ graph.State) (any, error) {
		n := width(r.cfg)
		cmds := make([]*graph.Command, n)
		for i := range n {
			cmds[i] = &graph.Command{GoTo: target, Update: graph.State{kMember: i}}
		}
		return cmds, nil
	})
}

func compile() (*graph.Executor, error) {
	str := reflect.TypeFor[string]()
	strs := reflect.TypeFor[[]string]()
	schema := graph.NewStateSchema().
		AddField(kRun, graph.StateField{Type: reflect.TypeFor[*run](), DisableDeepCopy: true}).
		AddField(kConv, graph.StateField{Type: reflect.TypeFor[[]council.Message]()}).
		AddField(kRoute, graph.StateField{Type: str}).
		AddField(kPlan, graph.StateField{Type: reflect.TypeFor[council.Plan]()}).
		AddField(kRound, graph.StateField{Type: reflect.TypeFor[int]()}).
		AddField(kFindings, graph.StateField{Type: strs, Reducer: slotReducer}).
		AddField(kCritiques, graph.StateField{Type: strs, Reducer: slotReducer}).
		AddField(kRevise, graph.StateField{Type: reflect.TypeFor[bool]()}).
		AddField(kAnswer, graph.StateField{Type: str})

	sg := graph.NewStateGraph(schema)
	sg.AddNode(nDecide, node(func(ctx context.Context, r *run, s graph.State) (any, error) {
		route, err := council.Decide(ctx, r.m, r.cfg, r.d, get[[]council.Message](s, kConv))
		return graph.State{kRoute: route}, err
	}))
	sg.AddNode(nDirect, node(func(ctx context.Context, r *run, s graph.State) (any, error) {
		ans, err := council.Direct(ctx, r.m, r.cfg, r.d, get[[]council.Message](s, kConv), r.emitter(ctx, s))
		return graph.State{kAnswer: ans}, err
	}))
	sg.AddNode(nPlan, node(func(ctx context.Context, r *run, s graph.State) (any, error) {
		p, err := council.MakePlan(ctx, r.m, r.cfg, r.d, get[[]council.Message](s, kConv), r.emitter(ctx, s))
		return graph.State{kPlan: p, kRound: 0}, err
	}))
	sg.AddNode(nFanResearch, fanOut(nResearch, func(c council.Config) int { return c.Researchers }))
	sg.AddNode(nResearch, node(func(ctx context.Context, r *run, s graph.State) (any, error) {
		i := get[int](s, kMember)
		f, err := council.Research(ctx, r.m, r.cfg, r.d, get[[]council.Message](s, kConv), get[council.Plan](s, kPlan),
			i, get[int](s, kRound), get[[]string](s, kCritiques), r.emitter(ctx, s))
		return graph.State{kFindings: slot{i, f}}, err
	}))
	sg.AddNode(nFanCritics, fanOut(nCritic, func(c council.Config) int { return c.Critics }))
	sg.AddNode(nCritic, node(func(ctx context.Context, r *run, s graph.State) (any, error) {
		i := get[int](s, kMember)
		c, err := council.Critique(ctx, r.m, r.cfg, r.d, get[[]council.Message](s, kConv), get[council.Plan](s, kPlan),
			get[[]string](s, kFindings), i, get[int](s, kRound), r.emitter(ctx, s))
		return graph.State{kCritiques: slot{i, c}}, err
	}))
	sg.AddNode(nGate, node(func(_ context.Context, r *run, s graph.State) (any, error) {
		round := get[int](s, kRound)
		if council.NeedsRevision(r.cfg, get[[]string](s, kCritiques), round) {
			return graph.State{kRevise: true, kRound: round + 1}, nil
		}
		return graph.State{kRevise: false}, nil
	}))
	sg.AddNode(nSynth, node(func(ctx context.Context, r *run, s graph.State) (any, error) {
		ans, err := council.Synthesize(ctx, r.m, r.cfg, r.d, get[[]council.Message](s, kConv), get[council.Plan](s, kPlan),
			get[[]string](s, kFindings), get[[]string](s, kCritiques), r.emitter(ctx, s))
		return graph.State{kAnswer: ans}, err
	}))

	sg.SetEntryPoint(nDecide).
		AddConditionalEdges(nDecide, func(_ context.Context, s graph.State) (string, error) {
			return get[string](s, kRoute), nil
		}, map[string]string{"direct": nDirect, "council": nPlan}).
		AddEdge(nPlan, nFanResearch).
		AddEdge(nResearch, nFanCritics).
		AddEdge(nCritic, nGate).
		AddConditionalEdges(nGate, func(_ context.Context, s graph.State) (string, error) {
			if get[bool](s, kRevise) {
				return "revise", nil
			}
			return "done", nil
		}, map[string]string{"revise": nFanResearch, "done": nSynth}).
		SetFinishPoint(nDirect).
		SetFinishPoint(nSynth)

	g, err := sg.Compile()
	if err != nil {
		return nil, err
	}
	return graph.NewExecutor(g, graph.WithMaxConcurrency(maxWidth), graph.WithMaxSteps(maxSteps))
}

// memberEvent is graph.NodeCustomEventMetadata with a typed payload: the
// executor carries custom events as JSON in StateDelta.
type memberEvent struct {
	EventType string        `json:"eventType"`
	Payload   council.Event `json:"payload"`
}

func (r *Runner) Run(ctx context.Context, cfg council.Config, m council.Model, conv []council.Message, emit council.Emit) (council.Result, error) {
	if err := cfg.Validate(); err != nil {
		return council.Result{}, err
	}
	if len(conv) == 0 {
		return council.Result{}, council.ErrNoConversation
	}
	if r.once.Do(func() { r.exec, r.err = compile() }); r.err != nil {
		return council.Result{}, r.err
	}
	emit = council.Serial(emit)

	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	rn := &run{cfg: cfg, m: m, emit: emit, d: council.NewDraws(cfg), native: r.Native, cancel: cancel}

	var ro agent.RunOptions
	agent.WithDisableGraphExecutorEvents(true)(&ro)
	agent.WithDisableTracing(true)(&ro)
	inv := agent.NewInvocation(agent.WithInvocationRunOptions(ro))

	events, err := r.exec.Execute(rctx, graph.State{kRun: rn, kConv: conv}, inv)
	if err != nil {
		return council.Result{}, err
	}
	var final map[string][]byte
	var graphErr error
	for ev := range events { // drain to close: the executor goroutine exits only then
		switch {
		case ev.Object == graph.ObjectTypeGraphNodeCustom:
			var me memberEvent
			if json.Unmarshal(ev.StateDelta[graph.MetadataKeyNodeCustom], &me) == nil && me.EventType == eventType {
				emit(me.Payload)
			}
		case ev.Object == graph.ObjectTypeGraphExecution && ev.Response != nil && ev.Response.Done:
			final = ev.StateDelta
		case ev.Response != nil && ev.Response.Error != nil && graphErr == nil:
			graphErr = errors.New(ev.Response.Error.Message)
		}
	}
	if err := rn.firstErr(); err != nil {
		return council.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return council.Result{}, err
	}
	if graphErr != nil {
		return council.Result{}, graphErr
	}
	if final == nil {
		return council.Result{}, errors.New("trpcagent: graph ended without a completion event")
	}
	var res council.Result
	var round int
	for k, v := range map[string]any{kRoute: &res.Route, kAnswer: &res.Answer, kRound: &round} {
		if b, ok := final[k]; ok {
			if err := json.Unmarshal(b, v); err != nil {
				return council.Result{}, err
			}
		}
	}
	if res.Route == "council" {
		res.Rounds = round + 1
	}
	return res, nil
}
