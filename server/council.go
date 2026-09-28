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
	"slices"
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
	// A client stating its own window, or attaching to a pool of its own,
	// is driving the engine itself: a refusal comes back at once -- with the
	// largest window the engine would admit when it says -- for the client to
	// act on (ask smaller, grow its owner), never queued.
	if req.Placement.NumCtx > 0 || (req.Placement.PoolID != nil && *req.Placement.PoolID >= 0) {
		c.Request = c.Request.WithContext(llm.WithNegotiation(c.Request.Context()))
	}
}

// councilServes reports whether this chat turn goes to the council.
//
// A response format is the client driving the model's output itself: a
// council would answer something other than what was asked, so the model
// answers as an ordinary chat. Tools go to the council only for a client that
// carries its state (council_chat_state): a member that calls one is
// suspended into that state, and without it the turn could not come back
// (plans/agentic-council-chat.md, 9.5); for any other client they stay a
// plain chat, as before. So does a load-only or unload request,
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
	return len(req.Messages) > 0 && (len(req.Tools) == 0 || req.CouncilChatState != nil) && len(req.Format) == 0 && !req.DebugRenderOnly
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

	// Tools (9.5): the members carry the client's and the council's own
	// evidence lookup, one list for all, so the shared prefix holds it once.
	req.Tools = council.WithEvidence(req.Tools)
	if cfg.Broadcast {
		req.Tools = council.WithBroadcast(req.Tools) // 10.6, behind council.broadcast
	}
	// 11.5: the synthesizer takes a tool turn's request first, and forwards it
	// or has the council rebuilt with these.
	req.Tools = council.WithRouting(req.Tools)
	if cfg.Critics > 0 {
		// 11.9: the synthesizer sends its checks to the critics with it.
		req.Tools = council.WithReview(req.Tools)
	}

	conv, system := councilConversation(m, req.Messages)
	cfg.System = system
	members := &councilMembers{
		s:       s,
		base:    req,
		session: sessionIDForRequest(req.SessionID, m, conv, nil),
		cloud:   councilCloud.slots(req.Model, councilCloudParallel(m)),
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
		// The turn runs inside the owner's window: report it as the turn's
		// X-Context-Window. begin read it from the engine on every turn after
		// the first; on the first, the planner's own admission reports it.
		if g := tree.ownerGrant(); g > 0 {
			llm.ReportContextWindow(ctx, g)
		}
		// A member's own tool results take at most one and a half times the
		// window in characters -- about half of it in tokens -- beside the
		// conversation, its stage and its reply. Measured live on b137 at
		// three quarters: a synthesizer's own 16 KB file folded at 16k, and it
		// paged the file it had to edit 50 lines at a time.
		cfg.ResultBudget = tree.window * 3 / 2
	}
	// Compaction (Phase 8, Cerebriline's): the record carried from the last
	// fold is applied, and the conversation folded again if a trigger fires.
	// A client's state (council_chat_state_v1) restores the compaction record
	// this server may have lost, and resumes the turn it was made for.
	from, turnHistory, turnHash, previous := councilResume(req, members.session)
	cfg.Previous = previous // xollama: the council kept across turns (council_continue.go)
	// Tools (9.5): every member carries them; a resumed turn's own calls and
	// results leave the conversation for the members that made them.
	cfg.Tools, members.tools = req.Tools, req.Tools
	cfg.Turn = fmt.Sprintf("%x", turnHash)
	if len(req.Tools) > 0 && cfg.Critics > 0 && members.session != "" {
		cfg.Reviews = councilDesks.get(members.session, cfg, members)
	}
	if from.Route != "" {
		all := conv
		conv, cfg.Results = councilToolTurn(conv)
		cfg.Reads = council.SharedReads(cfg.Tools, all[len(conv):])
		full = conv
	}
	// The earlier turns as the members read them: each forwarded call under
	// the member that made it, without its working notes (internal/council
	// History).
	conv = council.History(conv)
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
		var checkpoint func(council.Progress)
		var kept *keptTurn
		state := func(p council.Progress) string { return "" }
		if req.CouncilChatState != nil {
			state = func(p council.Progress) string {
				blob, err := sealCouncilState(councilState{history: turnHistory, turn: turnHash, progress: p, record: councilCompactions.get(members.session), kept: kept})
				if err != nil {
					slog.Warn("council: could not seal the turn's state", "error", err)
				}
				return blob
			}
			checkpoint = func(p council.Progress) {
				if blob := state(p); blob != "" {
					select {
					case ch <- api.ChatResponse{Model: req.Model, CreatedAt: time.Now().UTC(), Message: api.Message{Role: "assistant"}, CouncilChatState: blob}:
					case <-c.Request.Context().Done():
					}
				}
			}
		}
		res, err := council.RunFrom(c.Request.Context(), cfg, members, conv, from, checkpoint, func(e council.Event) {
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
				if c.Request.Context().Err() != nil {
					members.closeSessions() // the client left
					councilDesks.close(members.session)
				}
				return
			}
			select {
			case ch <- gin.H{"error": err.Error(), "status": members.status(err)}:
			case <-c.Request.Context().Done():
			}
			return
		}
		// A turn whose members wait on tools ends with their calls, as a
		// model's turn does, and carries what it resumes from; an answered
		// one carries only the record.
		var carry council.Progress
		if len(res.Calls) > 0 {
			carry = res.Progress
			select {
			case ch <- api.ChatResponse{Model: req.Model, CreatedAt: time.Now().UTC(), Message: api.Message{Role: "assistant", ToolCalls: res.Calls}}:
			case <-c.Request.Context().Done():
				return
			}
		} else {
			answer.Store(&res.Answer)
			kept = keepDeliberation(members.session, req.Messages, res.Kept)
		}
		slog.Info("council turn", "model", req.Model, "route", res.Route, "rounds", res.Rounds,
			"members", members.calls.Load(), "tool_calls", len(res.Calls), "duration", time.Since(start),
			"seeds", councilSeeds(res.Draws))
		final := api.ChatResponse{
			Model: req.Model, CreatedAt: time.Now().UTC(),
			Message: api.Message{Role: "assistant"}, Done: true, DoneReason: "stop",
		}
		final.Metrics = members.metrics(time.Since(start))
		final.CouncilUsage = append(members.usage.take(), councilDesks.usage(members.session)...)
		final.CouncilChatState = state(carry)
		select {
		case ch <- final:
		case <-c.Request.Context().Done():
		}
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
	return newCouncilCompactor(members, nil, cc, cfg, councilRenderer(m2, r, opts, req.Tools), r.Tokenize, opts.NumCtx, reserve)
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
	if e.Role == council.Researcher || e.Role == council.Critic || e.Role == council.Reviewer {
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

	// tools are the client's, on every member's request of a turn that has
	// them, so the prefix every member shares holds them too.
	tools api.Tools

	calls  atomic.Int32
	mu     sync.Mutex
	m      api.Metrics
	cached int // prompt tokens served from cache or a pool, over all members
	// usage is what each role spent (council_usage.go).
	usage usageBook
	last  int // the HTTP status of the last member error
	// sessions are the worker sessions this turn's members ran on.
	sessions map[string]bool
	// cloud counts the members on a cloud model running at once
	// (council_cloud.go); nil counts nothing.
	cloud chan struct{}
	// reviewWindow bounds a background reviewer's window on opencoti
	// (council_review.go); 0 states none.
	reviewWindow int
}

func (cm *councilMembers) opened(id string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.sessions == nil {
		cm.sessions = map[string]bool{}
	}
	cm.sessions[id] = true
}

// closeSessions ends the member sessions of a turn whose client left: the
// council lives until then.
func (cm *councilMembers) closeSessions() {
	if cm.tree == nil {
		return
	}
	cm.mu.Lock()
	ids := slices.Sorted(maps.Keys(cm.sessions))
	cm.mu.Unlock()
	for _, id := range ids {
		cm.tree.closeSession(id)
	}
}

func (cm *councilMembers) Stream(ctx context.Context, r council.Request, onToken func(string)) (string, error) {
	rep, err := cm.StreamTools(ctx, r, onToken)
	return rep.Content, err
}

// StreamTools is Stream with the tools the member calls (council.ToolModel).
func (cm *councilMembers) StreamTools(ctx context.Context, r council.Request, onToken func(string)) (council.Reply, error) {
	out, calls, err := cm.stream(ctx, r, onToken)
	return council.Reply{Content: out, Calls: calls}, err
}

func (cm *councilMembers) stream(ctx context.Context, r council.Request, onToken func(string)) (string, []api.ToolCall, error) {
	cm.calls.Add(1)
	began := time.Now()
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
		// A member on another server shares no prefix and calls nothing.
		out, err := cm.remote(ctx, r, req, onToken)
		return out, nil, err
	}
	release, err := cm.takeCloud(ctx, req.Model)
	if err != nil {
		return "", nil, err
	}
	defer release()
	req.Tools = cm.tools
	placement, worker, done := cm.place(ctx, r, &req)
	defer done()
	body, err := json.Marshal(req)
	if err != nil {
		return "", nil, err
	}

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", nil, err
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
	// A council is a living thing until its client leaves (the owner's ruling
	// 2026-09-28): a member's session stays open across its calls, trips and
	// turns, and the engine resumes it from its own cache (opencoti #526;
	// closed after every call, a resumed synthesizer re-prefilled ~18k of 21k
	// tokens on every step, ab-4). Only a client that leaves closes them
	// (closeSessions).
	if worker != "" {
		cm.opened(worker)
		defer cm.tree.leaveWorker(worker)
	}
	go func() {
		cm.s.ChatHandler(gc)
		pw.Close()
	}()
	defer pr.Close()
	// The scheduler drops a request whose context has ended without
	// answering it (processPending skips it), so a member's handler can wait
	// forever once the client leaves. The read ends with the context, not
	// with the handler: a turn left hanging holds its root, and the next turn
	// on the conversation waits on that.
	stop := context.AfterFunc(ctx, func() { pr.CloseWithError(ctx.Err()) })
	defer stop()

	var out strings.Builder
	var calls []api.ToolCall
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var line struct {
			api.ChatResponse
			Error string `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return out.String(), nil, fmt.Errorf("council %s: %w", r.Role, err)
		}
		if line.Error != "" {
			cm.mu.Lock()
			cm.last = w.status()
			cm.mu.Unlock()
			return out.String(), nil, fmt.Errorf("council %s: %s", r.Role, line.Error)
		}
		if t := line.Message.Content; t != "" {
			out.WriteString(t)
			onToken(t)
		}
		calls = append(calls, line.Message.ToolCalls...)
		if line.Done {
			cached := 0
			if line.PromptEvalCachedCount != nil {
				cached = *line.PromptEvalCachedCount
			}
			cm.usage.add(r, line.Metrics, cached, time.Since(began))
			cm.mu.Lock()
			cm.m.PromptEvalCount += line.PromptEvalCount
			cm.cached += cached
			cm.m.PromptEvalDuration += line.PromptEvalDuration
			cm.m.EvalCount += line.EvalCount
			cm.m.EvalDuration += line.EvalDuration
			cm.m.LoadDuration += line.LoadDuration
			cm.mu.Unlock()
		}
	}
	if err := ctx.Err(); err != nil {
		return out.String(), nil, err
	}
	// The whole exchange, for reading a turn back member by member.
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		msgs, _ := json.Marshal(r.Messages)
		reply, _ := json.Marshal(calls)
		slog.Debug("council member", "role", r.Role, "index", r.Index, "round", r.Round, "session", req.SessionID,
			"messages", string(msgs), "reply", out.String(), "calls", string(reply))
	}
	return out.String(), calls, sc.Err()
}

// memberSession gives each member its own engine session, so parallel members
// never contend for one slot. The planner keeps the conversation's own id: its
// decision, a direct answer and the plan continue the conversation, and a
// direct answer is then served from the same KV a plain chat would have used.
func (cm *councilMembers) memberSession(r council.Request) string {
	if cm.session == "" || r.Role == council.Planner || r.Role == council.Front || r.Role == roleCompactWriter {
		return cm.session
	}
	id := cm.session + "~" + string(r.Role)
	if r.Role == council.Researcher || r.Role == council.Critic || r.Role == council.Reviewer {
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

// councilResume reads a client's council_chat_state. It restores the
// conversation's compaction record when this server holds none or an older
// one -- apply still uses it only where its hash matches the conversation --
// and returns the progress to resume when the state was made for this very
// turn: the same history, the same user message. Anything else is a fresh
// start. It also returns the turn's binding, for the states this turn sends.
func councilResume(req api.ChatRequest, key string) (council.Progress, []byte, []byte, *council.Progress) {
	history, turn := councilTurnHashes(req.Messages)
	if req.CouncilChatState == nil || *req.CouncilChatState == "" {
		return council.Progress{}, history, turn, previousDeliberation(key, req.Messages, nil)
	}
	st, err := openCouncilState(*req.CouncilChatState)
	if err != nil {
		slog.Info("council: the client's state was not used; starting fresh", "error", err)
		return council.Progress{}, history, turn, previousDeliberation(key, req.Messages, nil)
	}
	prev := previousDeliberation(key, req.Messages, st.kept)
	if st.record != nil && key != "" {
		if cur := councilCompactions.get(key); cur == nil || cur.gen < st.record.gen {
			councilCompactions.put(key, st.record)
			slog.Info("council: compaction record restored from the client's state", "session", key, "generation", st.record.gen)
		}
	}
	if !bytes.Equal(st.history, history) || !bytes.Equal(st.turn, turn) {
		return council.Progress{}, history, turn, prev
	}
	if st.progress.Route != "" {
		slog.Info("council: resuming the turn from the client's state", "session", key, "route", st.progress.Route, "rounds", len(st.progress.Rounds))
	}
	return st.progress, history, turn, prev
}
