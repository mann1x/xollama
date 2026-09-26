package server

// xollama: the council runner's server side -- see plans/agentic-council-chat.md
// and docs/xollama/tweak.mdx ("Making a model a council"). Additive: the one
// line in ChatHandler that reaches it is the `council` hook.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/model"
)

// councilMemberKey marks a chat request the council itself made. A member is
// an ordinary turn on the model; without the mark it would convene the
// council again. It is a gin context key, never a header, so no client can
// set it.
const councilMemberKey = "xollama.council.member"

// councilPlacementKey carries a member's place on the engine (its pool, or the
// owner's window) from the council to the completion ChatHandler builds.
const councilPlacementKey = "xollama.council.placement"

// councilPlacement is the placement the council gave this request, or nil.
func councilPlacement(c *gin.Context) *llm.Placement {
	if v, ok := c.Get(councilPlacementKey); ok {
		p, _ := v.(*llm.Placement)
		return p
	}
	return nil
}

// clientPlacement carries a client's placement (client_placement_v1) to the
// engine on a plain turn, through the same key a member's placement takes. A
// member's own turn keeps the council's; a council turn reads only the pool,
// in its tree (councilTreeFor). placementFields drops all of it where the
// engine has no sessions.
func clientPlacement(c *gin.Context, req api.ChatRequest) {
	if req.Placement == nil || c.GetBool(councilMemberKey) {
		return
	}
	if _, set := c.Get(councilPlacementKey); set {
		return
	}
	c.Set(councilPlacementKey, &llm.Placement{PoolID: req.Placement.PoolID, NumCtx: req.Placement.NumCtx, NumCtxMin: req.Placement.NumCtxMin})
}

// councilServes reports whether this chat turn goes to the council.
//
// Tools and a response format are the client driving the model's output
// itself: a council would answer something other than what was asked, so the
// model answers as an ordinary chat. So does a load-only or unload request,
// and a render-only one (`_debug_render_only`, chat_render_v1): a client
// rendering its PolyKV prefix for a council model needs what the members send,
// and members are served as plain turns of the same model.
func councilServes(c *gin.Context, m *Model, req api.ChatRequest) bool {
	if m == nil || m.Xollama == nil || !m.Xollama.Council.On() {
		return false
	}
	if c.GetBool(councilMemberKey) {
		return false
	}
	return len(req.Messages) > 0 && len(req.Tools) == 0 && len(req.Format) == 0 && !req.DebugRenderOnly
}

// councilChat answers one chat turn with the model's council.
func (s *Server) councilChat(c *gin.Context, req api.ChatRequest, m *Model) {
	start := time.Now()
	cc := m.Xollama.Council

	cfg := council.FromModel(cc, councilTemperature(m, req))
	// A client that turned thinking off gets the answer alone.
	if req.Think != nil && !req.Think.Bool() {
		cfg.ShowDeliberation = false
	}
	if err := cfg.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	conv, system := councilConversation(m, req.Messages)
	cfg.System = system
	members := &councilMembers{
		s:       s,
		base:    req,
		session: sessionIDForRequest(req.SessionID, m, conv, nil),
	}

	// PolyKV: the members share the conversation's KV through a pool tree
	// when the engine can carry one; otherwise each prefills its own copy.
	tree, err := s.councilTreeFor(c.Request.Context(), m, req, members.session)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	members.window = councilMemberWindow(m, req, tree)
	reserve := councilReserve(cfg)
	full := conv // the conversation as the client sent it
	// answer is set by the turn and read once the response is written; a
	// client that left may leave the turn still running, hence atomic.
	var answer atomic.Pointer[string]
	ctx := c.Request.Context()
	pressure := 0.0
	if tree != nil {
		members.tree = tree
		tree.reserve = reserve
		pressure = tree.begin(ctx)
	}
	// Compaction (Phase 8, Cerebriline's): the record carried from the last
	// fold is applied, and the conversation folded again if a trigger fires.
	compactor := s.councilCompactorFor(ctx, m, req, members, tree, cfg, reserve)
	if compactor != nil {
		conv = compactor.compact(ctx, conv, "", false, pressure)
	}
	if tree != nil {
		// The planner runs attached to the conversation's root, so the
		// conversation is held once (guide §6.2, arm C). An engine that
		// answers "compact the session" gets exactly that, once.
		if err := tree.buildRoot(ctx, conv); errors.Is(err, llm.ErrSessionFull) && compactor != nil {
			before := councilCompactions.get(compactor.key)
			if short := compactor.compact(ctx, full, "refused", false, pressure); councilCompactions.get(compactor.key) != before {
				conv = short
				_ = tree.buildRoot(ctx, conv)
			}
		}
	}
	defer func() {
		if tree != nil {
			tree.finish(reserve)
		}
		a := answer.Load()
		councilIdle.Add(1)
		go func() {
			defer councilIdle.Done()
			if tree != nil {
				// Always run: it ends the mark a next turn waits on.
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				tree.promoteRoot(ctx)
				cancel()
			}
			if a != nil && compactor != nil {
				s.councilIdleCompact(compactor, full, *a)
			}
		}()
	}()

	ch := make(chan any)
	go func() {
		defer close(ch)
		th := newThinkingTags()
		sendTagged := func(msg api.Message, tag *api.CouncilTag) {
			select {
			case ch <- api.ChatResponse{Model: req.Model, CreatedAt: time.Now().UTC(), Message: msg, Council: tag}:
			case <-c.Request.Context().Done():
			}
		}
		send := func(msg api.Message) { sendTagged(msg, nil) }
		res, err := council.Run(c.Request.Context(), cfg, members, conv, func(e council.Event) {
			if e.Kind == council.Content {
				if e.Text != "" {
					send(api.Message{Role: "assistant", Content: e.Text})
				}
				return
			}
			for _, seg := range th.add(e) {
				sendTagged(api.Message{Role: "assistant", Thinking: seg.text}, &seg.tag)
			}
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			ch <- gin.H{"error": err.Error(), "status": members.status(err)}
			return
		}
		answer.Store(&res.Answer)
		slog.Info("council turn", "model", req.Model, "route", res.Route, "rounds", res.Rounds,
			"members", members.calls.Load(), "duration", time.Since(start),
			"seeds", councilSeeds(res.Draws))
		final := api.ChatResponse{
			Model: req.Model, CreatedAt: time.Now().UTC(),
			Message: api.Message{Role: "assistant"}, Done: true, DoneReason: "stop",
		}
		final.Metrics = members.metrics(time.Since(start))
		ch <- final
	}()
	writeChatResponse(c, req, ch)
}

// councilTemperature is the temperature the model would sample at: the
// request's, else the Modelfile's, else the default.
// councilMemberWindow is the context a member works in: the window the
// council's owner asks for on PolyKV, otherwise the model's num_ctx after the
// request's options.
func councilMemberWindow(m *Model, req api.ChatRequest, tree *councilTree) int {
	if tree != nil && tree.window > 0 {
		return tree.window
	}
	opts := api.DefaultOptions()
	_ = opts.FromMap(m.Options)
	_ = opts.FromMap(req.Options)
	return opts.NumCtx
}

func councilTemperature(m *Model, req api.ChatRequest) float64 {
	opts := api.DefaultOptions()
	_ = opts.FromMap(m.Options)
	_ = opts.FromMap(req.Options)
	return float64(opts.Temperature)
}

// councilCompactorFor is the conversation's compactor, on every engine: on
// PolyKV it sizes against the tree's grant, elsewhere against num_ctx. Nil
// when the runner cannot be had, which leaves the conversation as it came.
func (s *Server) councilCompactorFor(ctx context.Context, m *Model, req api.ChatRequest, members *councilMembers, tree *councilTree, cfg council.Config, reserve int) *councilCompactor {
	cc := m.Xollama.Council.Context
	if tree != nil {
		return newCouncilCompactor(members, tree, cc, cfg, tree.render, tree.tokenize, tree.numCtx, reserve)
	}
	r, m2, opts, err := s.scheduleRunner(ctx, m, []model.Capability{model.CapabilityCompletion}, req.Options, req.KeepAlive, req.Shift)
	if err != nil {
		slog.Debug("council: no runner to size the conversation; not compacting", "error", err)
		return nil
	}
	return newCouncilCompactor(members, nil, cc, cfg, councilRenderer(m2, r, opts), r.Tokenize, opts.NumCtx, reserve)
}

// councilConversation is the conversation as every member sends it: an empty
// system message, then the turns -- the prefix every member shares, and the
// one a client's PolyKV root is cut from (plans/agentic-council-chat.md, 9.3).
// The system prompt it displaces -- the client's, else the model's -- is
// returned for the members that answer the user (council.Config.System). The
// empty message is explicit because ChatHandler adds the model's system prompt
// to a conversation that states none. The model's own MESSAGE turns are not
// folded in: ChatHandler prepends those to every request, as upstream does.
func councilConversation(m *Model, msgs []api.Message) ([]api.Message, string) {
	system := m.System
	var turns []api.Message
	for _, msg := range msgs {
		if msg.Role == "system" && len(turns) == 0 {
			system = msg.Content // a client's system prompt replaces the model's
			continue
		}
		turns = append(turns, msg)
	}
	return append([]api.Message{{Role: "system"}}, turns...), strings.TrimSpace(system)
}

func councilSeeds(d council.Draws) []int64 {
	out := []int64{d.Decide.Seed, d.Plan.Seed, d.Synth.Seed}
	for _, row := range append(d.Researchers, d.Critics...) {
		for _, dr := range row {
			out = append(out, dr.Seed)
		}
	}
	return out
}

// thinkingTags turns the members' interleaved tokens into readable thinking.
// Parallel members speak at once, so one member holds the floor: its text is
// released a line at a time as it arrives, while the others are held back and
// released whole, each under its own heading, once the floor is free.
//
// Each piece released is one member's (thinkingSegment), so every thinking
// chunk names its member in ChatResponse.Council; the headings stay for the
// clients that read thinking as text.
type thinkingTags struct {
	tags  map[string]api.CouncilTag
	floor string
	order []string // members waiting for the floor, in the order they spoke
	buf   map[string]*strings.Builder
	done  map[string]bool
	last  string // the member the last released text belonged to
}

func newThinkingTags() *thinkingTags {
	return &thinkingTags{buf: map[string]*strings.Builder{}, done: map[string]bool{}, tags: map[string]api.CouncilTag{}}
}

// thinkingSegment is thinking text released for one member.
type thinkingSegment struct {
	tag  api.CouncilTag
	text string
}

func (t *thinkingTags) add(e council.Event) []thinkingSegment {
	key := memberName(e)
	t.tags[key] = api.CouncilTag{Role: string(e.Role), Index: e.Index, Round: e.Round}
	b := t.buf[key]
	if b == nil {
		b = &strings.Builder{}
		t.buf[key] = b
		if key != t.floor {
			t.order = append(t.order, key)
		}
	}
	b.WriteString(e.Text)
	if e.Done {
		t.done[key] = true
	}
	if t.floor == "" {
		t.take()
	}

	var out []thinkingSegment
	for t.floor != "" {
		if text := t.release(t.floor, t.done[t.floor]); text != "" {
			out = append(out, thinkingSegment{tag: t.tags[t.floor], text: text})
		}
		if !t.done[t.floor] {
			break
		}
		t.forget(t.floor)
		t.floor = ""
		t.take()
	}
	return out
}

// take hands the floor to the member that has waited longest.
func (t *thinkingTags) take() {
	if len(t.order) > 0 {
		t.floor, t.order = t.order[0], t.order[1:]
	}
}

// release returns the member's complete lines, or everything once it is done,
// under a heading when the speaker changes.
func (t *thinkingTags) release(key string, all bool) string {
	b := t.buf[key]
	text := b.String()
	var out string
	if all {
		out = text
		b.Reset()
	} else if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		out = text[:i+1]
		b.Reset()
		b.WriteString(text[i+1:])
	}
	if out == "" {
		return ""
	}
	if key != t.last {
		head := "### " + key + "\n"
		if t.last != "" {
			head = "\n\n" + head
		}
		t.last = key
		out = head + out
	}
	return out
}

func (t *thinkingTags) forget(key string) {
	delete(t.buf, key)
	delete(t.done, key)
	delete(t.tags, key)
}

func memberName(e council.Event) string {
	name := strings.ToUpper(string(e.Role[:1])) + string(e.Role[1:])
	if e.Role == council.Researcher || e.Role == council.Critic {
		name = fmt.Sprintf("%s %d", name, e.Index+1)
	}
	if e.Round > 0 {
		name = fmt.Sprintf("%s (round %d)", name, e.Round+1)
	}
	return name
}

// councilMembers makes each member's call as an ordinary chat turn through
// ChatHandler, in process: the member gets the model's renderer, parser,
// scheduler and engine session exactly as a client would.
type councilMembers struct {
	s       *Server
	base    api.ChatRequest
	session string

	// tree places the members on PolyKV; nil runs every member on its own.
	tree *councilTree
	// window is a member's context, which a role's think level is a share of.
	window int

	calls  atomic.Int32
	mu     sync.Mutex
	m      api.Metrics
	cached int // prompt tokens served from cache or a pool, over all members
	last   int // the HTTP status of the last member error
}

func (cm *councilMembers) Stream(ctx context.Context, r council.Request, onToken func(string)) (string, error) {
	cm.calls.Add(1)
	stream, off := true, api.ThinkValue{Value: false}
	opts := maps.Clone(cm.base.Options)
	if opts == nil {
		opts = map[string]any{}
	}
	opts["seed"] = r.Seed
	opts["temperature"] = r.Temperature
	if r.MaxTokens > 0 {
		opts["num_predict"] = r.MaxTokens
	}
	req := api.ChatRequest{
		Model:     cm.base.Model,
		Messages:  r.Messages,
		Stream:    &stream,
		Format:    r.Format,
		Options:   opts,
		KeepAlive: cm.base.KeepAlive,
		// Thinking is off for every member: the council's deliberation is
		// its members' replies, and a member thinking first would spend its
		// budget where nobody reads it.
		Think:     &off,
		SessionID: cm.memberSession(r),
	}
	if r.Model != "" {
		// Keep think false: a thinking model sent no think reasons by default
		// (upstream's ChatHandler turns nil into true), and a small one spends
		// its whole reply cap doing so -- measured, qwen3:8b answered nothing
		// in 384 tokens. False is accepted by a model that cannot think.
		req.Model = r.Model
	}
	// A role that reasons gets its budget as a token count, and room for it
	// on top of its reply cap: a level sent as is would be a share of
	// num_predict, the reply cap, and bound nothing useful. The reasoning is
	// read nowhere below; only the reply joins the deliberation.
	if budget := council.ThinkBudget(r.Think, cm.window); budget > 0 {
		req.Think = &api.ThinkValue{Value: budget}
		// A cloud model or a stock ollama takes no token budget: ollama.com
		// refuses one ("think must be a boolean or string"). There the member
		// thinks, and num_predict, the reply cap plus the budget, bounds it.
		if !cm.councilTakesBudget(ctx, r) {
			req.Think = &api.ThinkValue{Value: true}
		}
		if r.MaxTokens > 0 {
			opts["num_predict"] = r.MaxTokens + budget
		}
	}
	if r.Host != "" {
		return cm.remote(ctx, r, req, onToken)
	}
	placement, worker := cm.place(ctx, r, &req)
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	pr, pw := io.Pipe()
	w := &memberWriter{h: http.Header{}, w: pw}
	gc, _ := gin.CreateTestContext(w)
	gc.Request = hr
	gc.Set(councilMemberKey, true)
	if placement != nil {
		gc.Set(councilPlacementKey, placement)
	}
	if worker != "" {
		defer cm.tree.closeWorker(worker)
	}
	go func() {
		cm.s.ChatHandler(gc)
		pw.Close()
	}()
	defer pr.Close()

	var out strings.Builder
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var line struct {
			api.ChatResponse
			Error string `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return out.String(), fmt.Errorf("council %s: %w", r.Role, err)
		}
		if line.Error != "" {
			cm.mu.Lock()
			cm.last = w.status()
			cm.mu.Unlock()
			return out.String(), fmt.Errorf("council %s: %s", r.Role, line.Error)
		}
		if t := line.Message.Content; t != "" {
			out.WriteString(t)
			onToken(t)
		}
		if line.Done {
			cm.mu.Lock()
			cm.m.PromptEvalCount += line.PromptEvalCount
			if line.PromptEvalCachedCount != nil {
				cm.cached += *line.PromptEvalCachedCount
			}
			cm.m.PromptEvalDuration += line.PromptEvalDuration
			cm.m.EvalCount += line.EvalCount
			cm.m.EvalDuration += line.EvalDuration
			cm.m.LoadDuration += line.LoadDuration
			cm.mu.Unlock()
		}
	}
	if err := ctx.Err(); err != nil {
		return out.String(), err
	}
	return out.String(), sc.Err()
}

// memberSession gives each member its own engine session, so parallel members
// never contend for one slot. The planner keeps the conversation's own id: its
// decision, a direct answer and the plan continue the conversation, and a
// direct answer is then served from the same KV a plain chat would have used.
func (cm *councilMembers) memberSession(r council.Request) string {
	if cm.session == "" || r.Role == council.Planner || r.Role == roleCompactWriter {
		return cm.session
	}
	id := cm.session + "~" + string(r.Role)
	if r.Role == council.Researcher || r.Role == council.Critic {
		id = fmt.Sprintf("%s-%d", id, r.Index+1)
	}
	return id
}

func (cm *councilMembers) metrics(total time.Duration) api.Metrics {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	m := cm.m
	m.TotalDuration = total
	if cm.cached > 0 {
		cached := cm.cached
		m.PromptEvalCachedCount = &cached
	}
	return m
}

func (cm *councilMembers) status(error) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.last >= 400 {
		return cm.last
	}
	return http.StatusInternalServerError
}

// memberWriter is the ResponseWriter a member's ChatHandler writes to: a pipe
// the council reads line by line, as a client would read the stream.
type memberWriter struct {
	h    http.Header
	w    *io.PipeWriter
	mu   sync.Mutex
	code int
}

func (m *memberWriter) Header() http.Header { return m.h }

func (m *memberWriter) WriteHeader(code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.code == 0 {
		m.code = code
	}
}

func (m *memberWriter) Write(p []byte) (int, error) {
	m.WriteHeader(http.StatusOK)
	return m.w.Write(p)
}

func (m *memberWriter) Flush() {}

// CloseNotify is required by gin's Context.Stream. A member's end comes from
// its request context, which the council cancels, so this never fires.
func (m *memberWriter) CloseNotify() <-chan bool { return nil }

func (m *memberWriter) status() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.code
}
