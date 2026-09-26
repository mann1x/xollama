package server

// xollama: the council's compaction -- plans/agentic-council-chat.md, Phase 8.
// Additive; reached from server/council.go before a turn and after its answer.
//
// A port of Cerebriline's agentic council compaction (/shared/dev/cline,
// sdk/packages/core/src/extensions/context/; guide §2.7, §6.5, §11 e, §11 i,
// §11 j) onto the council's server side. The client resends the whole
// conversation every turn and never sees a summary, so what Cerebriline keeps
// in its transcript is kept here as a record per conversation, applied to
// every turn's history before anything else:
//
//	[system] [summary: the user's requests, a retrospective, the replay]
//	[the pinned request, if any] [the turns kept verbatim ...]
//
// A fold is a council of its own: a writer replays the folded turns as the
// conversation's next turn (on PolyKV, attached to the conversation's root),
// two critics rewrite the replay's halves against the conversation, and a
// synthesizer joins them. It is incremental: the writer sees the previous
// summary in its context and replays it with what follows it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/types/xollama"
)

// Cerebriline's numbers, by the name each has there.
const (
	compactionInputRatio      = 0.9    // CONTEXT_WINDOW_INPUT_RATIO
	compactionTriggerRatio    = 0.9    // COMPACTION_TRIGGER_RATIO
	compactionMinTriggerShare = 0.5    // MIN_TRIGGER_WINDOW_SHARE
	compactionTargetShare     = 0.25   // COMPACTION_TARGET_CONTENT_SHARE
	compactionPreserveAt128k  = 20_000 // resolvePreserveRecentTokens
	compactionPreserveCap     = 0.6    // of the target
	compactionRecentRatio     = 0.25   // DEFAULT_PRESERVE_RECENT_MESSAGES_RATIO
	compactionLastTurnCeiling = 0.66   // LAST_TURN_PRESERVE_CEILING_RATIO
	compactionPinnedFold      = 0.5    // PINNED_PROMPT_FOLD_RATIO
	compactionSummaryShare    = 0.7    // SUMMARY_BUDGET_SHARE
	compactionMinBudget       = 4096   // the floor of the combined and summary budgets
	compactionAttempts        = 3      // SUMMARY_ATTEMPTS
	compactionHalfBand        = 0.3    // HALF_MARKER_BAND
	compactionMinMerge        = 0.5    // COUNCIL_MIN_MERGE_RATIO
	compactionRequestShare    = 0.15   // USER_REQUEST_BUDGET_SHARE
	compactionMaxRequestChars = 2000   // MAX_USER_REQUEST_CHARS
	compactionMinRequestChars = 200
	compactionMaxBlockChars   = 1200 // REPLAY_BLOCK_LIMITS.maxBlockChars
	compactionReasoningChars  = 6000
	compactionTextChars       = 2000 // TOOL_RESULT_CHAR_LIMIT, for the text path's turns
	compactionRefusalRatio    = 1.25 // KV_PRESSURE_COMPACTION_MIN_RATIO
	compactionRefusalGain     = 0.25 // KV_PRESSURE_COMPACTION_MIN_GAIN_SHARE
)

// compactionBudgetLadder is COMPACTION_BUDGET_LADDER: the share of the target
// the summary and the retrospective may use together, by generation.
var compactionBudgetLadder = []float64{0.33, 0.40, 0.45, 0.50, 0.55}

// The compaction's members. The writer continues the conversation on its
// owner; the critics and the synthesizer are workers on P′; the retrospective
// and the text path read no pool.
const (
	roleCompactWriter      council.Role = "compaction-writer"
	roleCompactCritic1     council.Role = "compaction-critic-1"
	roleCompactCritic2     council.Role = "compaction-critic-2"
	roleCompactSynthesizer council.Role = "compaction-synthesizer"
	roleCompactRetro       council.Role = "compaction-retrospective"
	roleCompactText        council.Role = "compaction-text" // prefix of the text path's roles
)

// compactionUnpooled reports a compaction member that attaches to no pool.
func compactionUnpooled(r council.Role) bool {
	return r == roleCompactRetro || strings.HasPrefix(string(r), string(roleCompactText))
}

// councilCompactor compacts one council's conversation.
type councilCompactor struct {
	members  *councilMembers
	tree     *councilTree // nil off PolyKV
	render   func(context.Context, []api.Message) (string, error)
	tokenize func(context.Context, string) ([]int, error)
	numCtx   int
	reserve  int    // the turn's room for its replies: the output room
	key      string // the conversation: its owner session

	compactAt, idleCompactAt float64
	basic                    bool
	review, retrospective    bool
	think                    string // the planner's, for the writer and the critics
	temperature              float64

	mu      sync.Mutex
	perChar float64 // tokens per character, from the last measure
}

func newCouncilCompactor(members *councilMembers, tree *councilTree, cc *xollama.CouncilContext, cfg council.Config, render func(context.Context, []api.Message) (string, error), tokenize func(context.Context, string) ([]int, error), numCtx, reserve int) *councilCompactor {
	_, _, compactAt := councilWindow(cc, numCtx)
	c := &councilCompactor{
		members: members, tree: tree, render: render, tokenize: tokenize,
		numCtx: numCtx, reserve: reserve, key: members.session,
		compactAt: compactAt, idleCompactAt: councilIdleCompactAt(cc, compactAt),
		review: true, retrospective: true,
		think: cfg.Think[council.Planner], temperature: cfg.Temperature,
		perChar: 0.3,
	}
	if cc != nil {
		c.basic = cc.Compaction == xollama.CouncilCompactionBasic
		if cc.Review != nil {
			c.review = *cc.Review
		}
		if cc.Retrospective != nil {
			c.retrospective = *cc.Retrospective
		}
	}
	return c
}

// window is what every size reads (resolveGrantedContextWindow): the owner's
// grant when it is smaller than the conversation's num_ctx. An unowned tree's
// owner books per request, so its grant is one request's, not the
// conversation's window.
func (c *councilCompactor) window() int {
	w := c.numCtx
	if c.tree != nil && !c.tree.unowned {
		c.tree.mu.Lock()
		g := c.tree.grant
		c.tree.mu.Unlock()
		if g > 0 && (w <= 0 || g < w) {
			w = g
		}
	}
	return w
}

// compactionSizes are one conversation's sizes, in tokens.
type compactionSizes struct {
	window, usable, overhead int
	trigger, target          int
	preserve, lastTurn       int
	tokens                   int     // the conversation as the members send it
	perChar                  float64 // tokens per character, for one message's share
}

// compactionSizesFor is Cerebriline's arithmetic: resolveCompactionTriggerTokens,
// resolveMessageTargetTokens, resolvePreserveRecentTokens and the last-turn
// ceiling, with the turn's reserve as the output room.
func compactionSizesFor(window, overhead, reserve int) compactionSizes {
	usable := int(float64(window) * compactionInputRatio)
	room := max(window-reserve, int(float64(window)*compactionMinTriggerShare))
	messages := max(0, usable-overhead)
	target := int(float64(messages) * compactionTargetShare)
	preserve := int(math.Round(compactionPreserveAt128k * math.Pow(float64(window)/131072, 2.0/3.0)))
	return compactionSizes{
		window: window, usable: usable, overhead: overhead,
		trigger:  min(int(float64(usable)*compactionTriggerRatio), room),
		target:   target,
		preserve: min(preserve, int(float64(target)*compactionPreserveCap)),
		lastTurn: int(float64(messages) * compactionLastTurnCeiling),
	}
}

// measure sizes conv. The whole conversation and its system message are
// counted by the engine's tokenizer; one message is its share by characters.
func (c *councilCompactor) measure(ctx context.Context, conv []api.Message) (compactionSizes, error) {
	full, err := c.render(ctx, conv)
	if err != nil {
		return compactionSizes{}, err
	}
	toks, err := c.tokenize(ctx, full)
	if err != nil {
		return compactionSizes{}, err
	}
	overhead := 0
	if sys, err := c.render(ctx, conv[:1]); err == nil {
		if t, err := c.tokenize(ctx, sys); err == nil {
			overhead = len(t)
		}
	}
	z := compactionSizesFor(c.window(), overhead, c.reserve)
	z.tokens = len(toks)
	z.perChar = float64(len(toks)) / float64(max(1, len(full)))
	c.mu.Lock()
	c.perChar = z.perChar
	c.mu.Unlock()
	return z, nil
}

func (c *councilCompactor) estimate(s string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int(math.Ceil(float64(len(s)) * c.perChar))
}

func messageTokens(m api.Message, perChar float64) int {
	return int(math.Ceil(float64(len(m.Content)+len(m.Thinking)+16) * perChar))
}

// compactionRecord is one conversation's compaction, carried forward.
type compactionRecord struct {
	n        int           // client messages, after the system message, that head replaces
	hash     string        // of those messages
	head     []api.Message // the summary message, then the pinned request if any
	gen      int
	requests []string
	retro    string
	replay   string
	how      string // agentic, text or basic
}

// councilCompactions holds each conversation's record, by owner session. In
// memory only: after a restart the raw history is compacted again if needed.
var councilCompactions = &compactionStore{m: map[string]*compactionRecord{}}

type compactionStore struct {
	mu    sync.Mutex
	m     map[string]*compactionRecord
	order []string
}

func (s *compactionStore) get(key string) *compactionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}

func (s *compactionStore) put(key string, r *compactionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; !ok {
		s.order = append(s.order, key)
	}
	s.m[key] = r
	for len(s.order) > 64 {
		delete(s.m, s.order[0])
		s.order = s.order[1:]
	}
}

func (s *compactionStore) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

func (s *compactionStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m, s.order = map[string]*compactionRecord{}, nil
}

func compactionHash(msgs []api.Message) string {
	h := sha256.New()
	for _, m := range msgs {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// apply is the conversation the members are sent: the record's head in place
// of the messages it replaces. A client that edited or cut those messages
// gets its raw history, and the record goes.
func (c *councilCompactor) apply(conv []api.Message) ([]api.Message, *compactionRecord) {
	rec := councilCompactions.get(c.key)
	if rec == nil || len(conv) == 0 {
		return conv, nil
	}
	turns := conv[1:]
	if len(turns) < rec.n || compactionHash(turns[:rec.n]) != rec.hash {
		councilCompactions.drop(c.key)
		slog.Info("council: compaction dropped: the conversation no longer starts with what it replaced", "session", c.key)
		return conv, nil
	}
	out := append([]api.Message{conv[0]}, rec.head...)
	return append(out, turns[rec.n:]...), rec
}

// compactionPlan says what a fold keeps: conv[cut:], and conv[pin] when pin
// is set. Everything else after the system message is folded.
type compactionPlan struct {
	cut, pin int
}

// planCompaction is findCutPlan: the recency candidate, the last turn's
// ceiling and the pinned cut. fresh is the first message newer than the
// summary (2 with one, 1 without); a plan must fold at least one of them.
func planCompaction(conv []api.Message, z compactionSizes, keepTail bool, fresh int) (compactionPlan, bool) {
	tok := func(i int) int { return messageTokens(conv[i], z.perChar) }
	lastTurn := -1
	for i := len(conv) - 1; i >= fresh; i-- {
		if conv[i].Role == "user" {
			lastTurn = i
			break
		}
	}
	folds := func(p compactionPlan) bool {
		for i := fresh; i < p.cut; i++ {
			if i != p.pin {
				return true
			}
		}
		return false
	}
	if !keepTail {
		p := compactionPlan{cut: len(conv), pin: lastTurn}
		return p, folds(p)
	}
	floor, ceiling := min(z.preserve, z.target), z.target
	if ceiling <= 0 {
		ceiling = floor
	}
	pinned := func() (compactionPlan, bool) {
		if lastTurn < fresh {
			return compactionPlan{}, false
		}
		after := 0
		for i := lastTurn + 1; i < len(conv); i++ {
			after += tok(i)
		}
		keep := min(max(int(float64(after)*compactionPinnedFold), min(floor, after)), ceiling)
		cut, acc := len(conv), 0
		for i := len(conv) - 1; i > lastTurn && acc < keep; i-- {
			acc += tok(i)
			cut = i
		}
		cut = safeBoundary(conv, cut, lastTurn+1)
		p := compactionPlan{cut: cut, pin: lastTurn}
		return p, folds(p)
	}
	if lastTurn >= fresh {
		tail := 0
		for i := lastTurn; i < len(conv); i++ {
			tail += tok(i)
		}
		if tail > z.lastTurn {
			return pinned()
		}
	}
	n := len(conv) - 1
	minKept := int(math.Ceil(float64(n) * compactionRecentRatio))
	candidate, total := 0, 0
	for i := len(conv) - 1; i >= 1; i-- {
		total += tok(i)
		if total >= ceiling || (total >= floor && len(conv)-i >= minKept) {
			candidate = i
			break
		}
	}
	if candidate <= 1 {
		return compactionPlan{}, false
	}
	cut := candidate
	if lastTurn > 0 && lastTurn < cut {
		cut = lastTurn
	}
	cut = safeBoundary(conv, cut, fresh)
	if p := (compactionPlan{cut: cut, pin: -1}); folds(p) {
		return p, true
	}
	return pinned()
}

// safeBoundary walks a cut back off a tool result, never below low.
func safeBoundary(conv []api.Message, cut, low int) int {
	for cut > low && cut < len(conv) && conv[cut].Role == "tool" {
		cut--
	}
	return cut
}

// councilCompacting joins the callers folding the same conversation: the idle
// council and the next turn never fold it twice.
var councilCompacting singleflight.Group

// councilIdle tracks the idle work still running after its turn, so a test
// can wait for it.
var councilIdle sync.WaitGroup

// compact returns the conversation the members are sent: the record applied,
// and folded again when a trigger fires. force names a reason that needs no
// trigger (the engine said "compact the session"). idle is the council
// between an answer and the next message, which folds a little earlier.
// pressure is the owner's raw /kv pressure, 0 off PolyKV.
func (c *councilCompactor) compact(ctx context.Context, conv []api.Message, force string, idle bool, pressure float64) []api.Message {
	applied, rec := c.apply(conv)
	if len(applied) < 3 {
		return applied
	}
	z, err := c.measure(ctx, applied)
	if err != nil {
		slog.Debug("council: could not size the conversation", "error", err)
		return applied
	}
	why := force
	if why == "" {
		why = c.trigger(z, idle, pressure)
	}
	if why == "" {
		return applied
	}
	_, _, _ = councilCompacting.Do(c.key, func() (any, error) {
		// Another caller may have folded it while this one waited.
		applied, rec = c.apply(conv)
		if force == "" && councilCompactions.get(c.key) != nil {
			if z2, err := c.measure(ctx, applied); err == nil && c.trigger(z2, idle, 0) == "" {
				return nil, nil
			}
		}
		c.fold(ctx, conv, applied, rec, why)
		return nil, nil
	})
	out, _ := c.apply(conv)
	return out
}

// trigger says why a conversation of sizes z compacts, or "" when it does not.
// Pressure only adds reasons; it never vetoes the arithmetic.
func (c *councilCompactor) trigger(z compactionSizes, idle bool, pressure float64) string {
	scale, at := 1.0, c.compactAt
	if idle {
		scale, at = c.idleCompactAt/c.compactAt, c.idleCompactAt
	}
	switch {
	case z.tokens >= int(float64(z.trigger)*scale):
		return "size"
	case pressure > 0 && pressure >= at:
		return "pressure"
	case !idle && c.refusalsCompact(z):
		return "refusals"
	}
	return ""
}

// refusalsCompact is kvPressureCompaction: while the engine refuses others,
// an owner above its floor compacts when the conversation is well above what
// compaction leaves and compacting lets its booking shrink by a quarter.
func (c *councilCompactor) refusalsCompact(z compactionSizes) bool {
	t := c.tree
	if t == nil || t.unowned {
		return false
	}
	t.mu.Lock()
	refusing, grant, floor := t.refusing, t.grant, t.floor
	t.mu.Unlock()
	if !refusing || grant <= floor {
		return false
	}
	after := z.overhead + z.target
	if float64(z.tokens) < compactionRefusalRatio*float64(after) {
		return false
	}
	shrunk := max(floor, int(float64(after+c.reserve)/0.6))
	return float64(grant-shrunk) >= compactionRefusalGain*float64(grant)
}

// fold makes and stores a new record for conv, whose applied form is applied.
func (c *councilCompactor) fold(ctx context.Context, conv, applied []api.Message, rec *compactionRecord, why string) {
	start := time.Now()
	z, err := c.measure(ctx, applied)
	if err != nil {
		return
	}
	next, err := c.foldWith(ctx, conv, applied, rec, z, true)
	if err == nil && next != nil && !c.basic {
		// A replay that keeps the tail and still leaves the conversation over
		// its trigger is rescued without the tail (compaction.ts, no-tail).
		if short, _ := c.applyRecord(conv, next); len(short) > 0 {
			if zs, err := c.measure(ctx, short); err == nil && zs.tokens >= zs.trigger {
				if full, err := c.foldWith(ctx, conv, applied, rec, z, false); err == nil && full != nil {
					if fshort, _ := c.applyRecord(conv, full); len(fshort) > 0 {
						if zf, err := c.measure(ctx, fshort); err == nil && zf.tokens < zs.tokens {
							next = full
						}
					}
				}
			}
		}
	}
	if next == nil {
		slog.Info("council: nothing to compact", "session", c.key, "trigger", why, "error", err)
		return
	}
	after, _ := c.applyRecord(conv, next)
	za, err := c.measure(ctx, after)
	if err != nil || za.tokens >= z.tokens {
		// A fold that does not shrink the conversation is worse than none:
		// the summary quotes every request and replays what it folds, which
		// can outweigh a short span.
		slog.Info("council: compaction discarded: it did not shrink the conversation", "session", c.key,
			"trigger", why, "tokens_before", z.tokens, "tokens_after", za.tokens, "error", err)
		return
	}
	councilCompactions.put(c.key, next)
	slog.Info("council: conversation compacted", "session", c.key, "trigger", why, "how", next.how,
		"generation", next.gen, "tokens_before", z.tokens, "tokens_after", za.tokens,
		"window", z.window, "trigger_tokens", z.trigger, "replaced", next.n, "wall", time.Since(start).Round(time.Millisecond))
}

// applyRecord is apply with a given record rather than the stored one.
func (c *councilCompactor) applyRecord(conv []api.Message, rec *compactionRecord) ([]api.Message, bool) {
	turns := conv[1:]
	if len(turns) < rec.n {
		return nil, false
	}
	out := append([]api.Message{conv[0]}, rec.head...)
	return append(out, turns[rec.n:]...), true
}

// foldWith plans and runs one fold. keepTail false is the no-tail form: the
// full prompt, only the latest request kept.
func (c *councilCompactor) foldWith(ctx context.Context, conv, applied []api.Message, rec *compactionRecord, z compactionSizes, keepTail bool) (*compactionRecord, error) {
	headLen, prevN, fresh := 0, 0, 1
	var prev compactionRecord
	if rec != nil {
		prev = *rec
		headLen, prevN, fresh = len(rec.head), rec.n, 2
	}
	plan, ok := planCompaction(applied, z, keepTail, fresh)
	if !ok {
		return nil, errors.New("no cut folds anything")
	}
	// The client messages the new head replaces: those the old head did, and
	// the applied ones folded after it.
	n := prevN + max(0, plan.cut-1-headLen)

	var folded []api.Message // newer than the summary, the pin aside
	for i := fresh; i < plan.cut; i++ {
		if i != plan.pin {
			folded = append(folded, applied[i])
		}
	}
	requests := slices.Clone(prev.requests)
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(requests, s) {
			requests = append(requests, s)
		}
	}
	for _, m := range folded {
		if m.Role == "user" {
			add(m.Content)
		}
	}
	if plan.pin >= 0 {
		add(applied[plan.pin].Content)
	}

	gen := prev.gen + 1
	share := compactionBudgetLadder[min(gen, len(compactionBudgetLadder))-1]
	combined := max(compactionMinBudget, int(float64(z.target)*share))
	budget := max(compactionMinBudget, int(float64(combined)*compactionSummaryShare))
	requestsBlock := compactionRequests(requests, int(float64(z.target)*compactionRequestShare/max(z.perChar, 0.01)))

	out := &compactionRecord{n: n, gen: gen, requests: requests, retro: prev.retro}
	if c.basic {
		out.how = "basic"
		out.replay = compactionBasic(prev.replay, folded, int(float64(budget)/max(z.perChar, 0.01)))
	} else {
		replay, retro, how, err := c.agentic(ctx, applied, plan, folded, prev, requestsBlock, keepTail, budget, combined)
		switch {
		case err == nil:
			out.replay, out.retro, out.how = replay, retro, how
		case ctx.Err() != nil:
			return nil, ctx.Err()
		default:
			slog.Info("council: agentic compaction failed; basic instead", "error", err)
			out.how = "basic"
			out.replay = compactionBasic(prev.replay, folded, int(float64(budget)/max(z.perChar, 0.01)))
		}
	}
	summary := requestsBlock
	if strings.TrimSpace(out.retro) != "" {
		summary += "\n\n" + compactionRetrospectiveHeading + "\n\n" + strings.TrimSpace(out.retro)
	}
	summary += "\n\n" + compactionSummaryHeading + "\n\n" + out.replay
	out.head = []api.Message{{Role: "user", Content: summary}}
	if plan.pin >= 0 {
		out.head = append(out.head, applied[plan.pin])
	}
	out.hash = compactionHash(conv[1 : 1+n])
	return out, nil
}

// agentic is runAgenticCompaction with runCouncilReview: the writer, then the
// retrospective beside the two critics, then the synthesizer.
func (c *councilCompactor) agentic(ctx context.Context, applied []api.Message, plan compactionPlan, folded []api.Message, prev compactionRecord, requestsBlock string, keepTail bool, budget, combined int) (replay, retro, how string, err error) {
	prompt, role := compactionReplayPrompt, compactionTailRole
	if !keepTail {
		prompt, role = compactionFullPrompt, compactionFullRole
	}
	if c.review {
		prompt += "\n\n" + compactionWriterMarker
	}
	instruction := role + "\n\n" + prompt + "\n\n" + requestsBlock + "\n\n" + compactionSpan(applied, plan)

	// The writer is the conversation's next turn (§11 i): same messages, one
	// instruction after them, so on PolyKV it reads the conversation's root.
	how = "pooled"
	if c.tree == nil {
		how = "continuation"
	}
	writerRole := roleCompactWriter
	base := slices.Clone(applied)
	replay, writerMsgs := c.write(ctx, writerRole, base, instruction, budget, keepTail)
	if replay == "" {
		if err := ctx.Err(); err != nil {
			return "", "", "", err
		}
		// The text path: the folded turns as text, for a call that shares
		// nothing with the conversation.
		how = "text"
		text := instruction
		if prev.replay != "" {
			text += "\n\nPrevious summary:\n" + prev.replay
		}
		text += "\n\nConversation:\n" + compactionTranscript(folded)
		base = []api.Message{{Role: "system", Content: role}}
		replay, writerMsgs = c.write(ctx, roleCompactText+"-writer", base, text, budget, keepTail)
		if replay == "" {
			return "", "", "", errors.New("the writer wrote nothing")
		}
	}

	retroBudget := max(combined/5, min(combined/2, combined-c.estimate(replay)))
	retro = prev.retro
	var g errgroup.Group
	if c.retrospective {
		reasoning := compactionReasoning(folded)
		if reasoning != "" || prev.retro != "" {
			g.Go(func() error {
				req := compactionRetrospectivePrompt
				if prev.retro != "" {
					req += "\n\nYour retrospective from the previous compaction. Carry forward what still holds, revise what does not, and do not simply restate it:\n" + prev.retro
				}
				if reasoning == "" {
					reasoning = "(none)"
				}
				req += "\n\nYour reasoning and what it produced:\n" + reasoning
				out, err := c.call(ctx, roleCompactRetro, []api.Message{
					{Role: "system", Content: compactionRetrospectiveRole},
					{Role: "user", Content: req},
				}, retroBudget, "")
				if err == nil && strings.TrimSpace(out) != "" {
					retro = strings.TrimSpace(out)
				}
				return nil // it never fails the compaction
			})
		}
	}

	first, second, split := splitReplay(replay)
	if !c.review || !split {
		_ = g.Wait()
		return stripMarker(replay), retro, how, nil
	}
	reviewed := append(slices.Clone(writerMsgs), api.Message{Role: "assistant", Content: replay})
	if how == "pooled" {
		release := c.tree.reviewLayer(ctx, reviewed)
		defer release()
	}
	critic := func(r council.Role, half, other, own, theirs string) string {
		req := renderCouncilPrompt(compactionCriticPrompt, map[string]string{
			"half": half, "other_half": other, "half_length": fmt.Sprint(len(own)),
		})
		req = compactionCriticRole + "\n\n" + req +
			"\n\n---\n\nThe " + half + " half of the replay — this is yours, rewrite it:\n\n" + own +
			"\n\n---\n\nThe " + other + " half of the replay — reference only, someone else owns it, do not return it:\n\n" + orNone(theirs) +
			"\n\n---\n\nThe conversation both halves were written from is the conversation above, up to where you were asked for the replay."
		out, err := c.call(ctx, c.reviewRole(r, how), append(slices.Clone(reviewed), api.Message{Role: "user", Content: req}), budget, c.think)
		out = strings.TrimSpace(out)
		if err != nil || out == "" || c.estimate(out) > budget {
			return own
		}
		return out
	}
	var firstNew, secondNew string
	g.Go(func() error { firstNew = critic(roleCompactCritic1, "first", "second", first, second); return nil })
	g.Go(func() error { secondNew = critic(roleCompactCritic2, "second", "first", second, first); return nil })
	_ = g.Wait()

	original := stripMarker(replay)
	req := renderCouncilPrompt(compactionSynthesizerPrompt, map[string]string{
		"original_length": fmt.Sprint(len(original)), "max_length": fmt.Sprint(int(math.Round(float64(len(original)) * 1.1))),
	})
	parts := []string{compactionSynthesizerRole, "", req, ""}
	if retro != "" {
		parts = append(parts,
			"Then the retrospective, which is a judgement about how the conversation went rather than a record of what happened. You are the first to see the finished replay, so you are the first who can judge it properly. Revise the retrospective against the replay you have just joined: drop a judgement the replay does not bear out, add one it makes obvious, sharpen one that is vague. Its rules hold — no names, no figures, no narration of events, and terse. If it is already right, return it unchanged.",
			"", "Answer with exactly these two sections and nothing before, between or after them:", "", "## Replay", "", "## Retrospective")
	} else {
		parts = append(parts, "Answer with exactly this section and nothing before or after it:", "", "## Replay")
	}
	parts = append(parts, "", "---", "",
		"**First half — as originally written:** your replay above, from its start to “…"+tailOf(first)+"”.", "",
		"**First half — as rewritten:**", "", firstNew, "", "---", "",
		"**Second half — as originally written:** your replay above, from “"+headOf(second)+"…” to its end.", "",
		"**Second half — as rewritten:**", "", secondNew)
	if retro != "" {
		parts = append(parts, "", "---", "", "**The retrospective, as written:**", "", retro)
	}
	synthBudget := budget
	if retro != "" {
		synthBudget += retroBudget
	}
	out, err := c.call(ctx, c.reviewRole(roleCompactSynthesizer, how), append(slices.Clone(reviewed), api.Message{Role: "user", Content: strings.Join(parts, "\n")}), synthBudget, "")
	merged, revised := parseCompactionSections(out)
	if err != nil || float64(len(merged)) < compactionMinMerge*float64(len(original)) {
		slog.Info("council: the compaction's synthesizer was not used; the writer's replay stands", "error", err, "merged", len(merged), "original", len(original))
		return original, retro, how, nil
	}
	if revised != "" {
		retro = revised
	}
	return merged, retro, how, nil
}

// reviewRole puts a text-path reviewer off the pools.
func (c *councilCompactor) reviewRole(r council.Role, how string) council.Role {
	if how == "text" {
		return council.Role(string(roleCompactText) + "-" + strings.TrimPrefix(string(r), "compaction-"))
	}
	return r
}

// write runs the writer up to compactionAttempts times: an empty answer is
// asked again, and an over-budget one with its measured size.
func (c *councilCompactor) write(ctx context.Context, role council.Role, base []api.Message, instruction string, budget int, keepTail bool) (string, []api.Message) {
	var best string
	var msgs []api.Message
	note := ""
	for range compactionAttempts {
		if ctx.Err() != nil {
			break
		}
		msgs = append(slices.Clone(base), api.Message{Role: "user", Content: instruction + note})
		out, err := c.call(ctx, role, msgs, budget, c.think)
		out = cleanReplay(out, keepTail)
		if err != nil || out == "" {
			continue
		}
		best = out
		if n := c.estimate(out); n > budget {
			note = fmt.Sprintf("\n\nYour last answer came to about %d tokens; the budget is %d. Write it again, within the budget.", n, budget)
			continue
		}
		break
	}
	return best, msgs
}

// call makes one compaction member's call through the council's members.
func (c *councilCompactor) call(ctx context.Context, role council.Role, msgs []api.Message, maxTokens int, think string) (string, error) {
	return c.members.Stream(ctx, council.Request{
		Role: role, Messages: msgs, MaxTokens: maxTokens, Think: think,
		Temperature: c.temperature, Seed: rand.Int64(),
	}, func(string) {})
}

// compactionSpan names what the replay covers by its edges
// (describeReplaySpan), rather than pasting it.
func compactionSpan(conv []api.Message, plan compactionPlan) string {
	if plan.cut >= len(conv) {
		s := "Replace the whole conversation above."
		if plan.pin >= 0 {
			s += " The request that begins “" + headOf(conv[plan.pin].Content) + "” stays as it is, after your summary."
		}
		return s
	}
	s := "Replay the conversation above from its start up to, but not including, the message that begins “" +
		headOf(conv[plan.cut].Content) + "”. That message and everything after it stay as they are."
	if plan.pin >= 0 {
		s += " So does the request that begins “" + headOf(conv[plan.pin].Content) + "”."
	}
	return s
}

// compactionRequests quotes every request, each cut to share a budget of
// chars between 200 and 2,000 characters a request; none is dropped.
func compactionRequests(requests []string, chars int) string {
	if len(requests) == 0 {
		return compactionRequestsHeading + "\n\n(none)"
	}
	each := min(compactionMaxRequestChars, max(compactionMinRequestChars, chars/len(requests)))
	var b strings.Builder
	b.WriteString(compactionRequestsHeading)
	for _, r := range requests {
		if len(r) > each {
			r = r[:each] + "…"
		}
		b.WriteString("\n\n<user_request>\n" + r + "\n</user_request>")
	}
	return b.String()
}

// compactionTranscript is the text path's view of the folded turns.
func compactionTranscript(msgs []api.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "user":
			b.WriteString("[User]: " + m.Content + "\n")
		case "assistant":
			if m.Thinking != "" {
				b.WriteString("[Bot thinking]: " + clip(m.Thinking, compactionTextChars) + "\n")
			}
			b.WriteString("[Bot]: " + m.Content + "\n")
		default:
			b.WriteString("[" + m.Role + "]: " + clip(m.Content, compactionTextChars) + "\n")
		}
	}
	return b.String()
}

// compactionReasoning is serializeReasoningWithOutcomes for a conversation:
// each answer's reasoning, and what it answered.
func compactionReasoning(msgs []api.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role != "assistant" || strings.TrimSpace(m.Thinking) == "" {
			continue
		}
		b.WriteString("Reasoning:\n" + clip(m.Thinking, compactionReasoningChars) + "\nThen answered: " + clip(m.Content, 200) + "\n\n")
	}
	return strings.TrimSpace(b.String())
}

// compactionBasic is runBasicCompaction for a conversation, with no model
// call: the earlier summary as it was, then the newest answers that fit.
func compactionBasic(prev string, folded []api.Message, chars int) string {
	var answers []string
	used := 0
	for i := len(folded) - 1; i >= 0; i-- {
		m := folded[i]
		if m.Role != "assistant" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		if used+len(m.Content) > chars {
			break
		}
		used += len(m.Content)
		answers = append(answers, m.Content)
	}
	slices.Reverse(answers)
	var parts []string
	if prev != "" {
		parts = append(parts, prev)
	}
	if len(answers) > 0 {
		parts = append(parts, "Answers given earlier, the newest that fit:\n\n"+strings.Join(answers, "\n\n---\n\n"))
	}
	if len(parts) == 0 {
		return "(The earlier turns were dropped; the requests above are what remains of them.)"
	}
	return strings.Join(parts, "\n\n")
}

var (
	echoedTranscript = regexp.MustCompile(`(?m)^(Conversation:|\[User\]:|\[Bot\]:)`)
	fencedBlock      = regexp.MustCompile("(?s)```[^\n]*\n.*?```")
	sectionHeading   = regexp.MustCompile(`(?im)^#{1,4}\s*(replay|retrospective)\s*$`)
)

// cleanReplay is cutEchoedTranscript, and trimReplayOverflow when the tail is
// kept.
func cleanReplay(s string, keepTail bool) string {
	s = strings.TrimSpace(s)
	if loc := echoedTranscript.FindStringIndex(s); loc != nil && loc[0] > 0 {
		s = strings.TrimSpace(s[:loc[0]])
	}
	if keepTail {
		s = fencedBlock.ReplaceAllStringFunc(s, func(b string) string {
			if len(b) <= compactionMaxBlockChars {
				return b
			}
			half := compactionMaxBlockChars / 2
			return b[:half] + fmt.Sprintf("\n… (%d characters elided) …\n", len(b)-2*half) + b[len(b)-half:]
		})
	}
	return s
}

// splitReplay is splitReplayAtMarker: at the marker when it falls within
// 30-70 %, else at the blank line nearest the middle, else any line.
func splitReplay(s string) (first, second string, ok bool) {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == compactionHalfway {
			a := strings.TrimSpace(strings.Join(lines[:i], "\n"))
			b := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
			if total := len(a) + len(b); total > 0 {
				if r := float64(len(a)) / float64(total); r >= compactionHalfBand && r <= 1-compactionHalfBand {
					return a, b, true
				}
			}
			break
		}
	}
	s = stripMarker(s)
	mid := len(s) / 2
	best := -1
	for _, sep := range []string{"\n\n", "\n"} {
		for i := 0; i+len(sep) <= len(s); i++ {
			if s[i:i+len(sep)] == sep && (best < 0 || abs(i-mid) < abs(best-mid)) {
				best = i
			}
		}
		if best > 0 {
			return strings.TrimSpace(s[:best]), strings.TrimSpace(s[best:]), true
		}
	}
	return "", "", false
}

func stripMarker(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != compactionHalfway {
			out = append(out, l)
		}
	}
	joined := strings.Join(out, "\n")
	for strings.Contains(joined, "\n\n\n") {
		joined = strings.ReplaceAll(joined, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(joined)
}

// parseCompactionSections reads the synthesizer's ## Replay and
// ## Retrospective, tolerant of the heading level (parseCouncilSections).
func parseCompactionSections(s string) (replay, retro string) {
	locs := sectionHeading.FindAllStringSubmatchIndex(s, -1)
	for i, loc := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := strings.TrimSpace(s[loc[1]:end])
		switch strings.ToLower(s[loc[2]:loc[3]]) {
		case "replay":
			replay = body
		case "retrospective":
			retro = body
		}
	}
	return replay, retro
}

func renderCouncilPrompt(t string, values map[string]string) string {
	for k, v := range values {
		t = strings.ReplaceAll(t, "{{"+k+"}}", v)
	}
	return t
}

func tailOf(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[max(0, len(s)-80):])
}

func headOf(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[:min(len(s), 80)])
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// reviewLayer forks the conversation's root after the writer's turn -- P′
// (§11 j) -- so the critics and the synthesizer read the conversation and the
// replay from the pool and prefill only their own request. The returned
// function lets it go. Without a root to fork, the reviewers run unpooled
// rather than build the conversation again from tokens.
func (t *councilTree) reviewLayer(ctx context.Context, reviewed []api.Message) func() {
	text, err := t.cut(ctx, reviewed)
	l := &councilLayer{text: text, ready: make(chan struct{})}
	close(l.ready)
	root := t.rootFor(ctx, reviewed[:len(reviewed)-1])
	t.mu.Lock()
	live := t.grant > 0 && !t.unowned
	t.mu.Unlock()
	switch {
	case err != nil || text == "":
		l.err = errors.New("no layer for the review")
	case root == nil:
		l.err = errors.New("no conversation root to fork for the review")
	case !live && !t.canUnown:
		l.err = errors.New("no owner and no unowned pools for the review")
	default:
		owner := ""
		if live {
			owner = t.owner
		}
		id := root.id
		p, err := t.kv.CreatePool(ctx, owner, &id, text)
		l.id, l.err = p.ID, err
	}
	key := layerKey(text)
	t.mu.Lock()
	t.layers[key] = l
	t.mu.Unlock()
	if l.err != nil {
		slog.Debug("council: compaction review runs unpooled", "error", l.err)
	}
	return func() {
		t.mu.Lock()
		if t.layers[key] == l {
			delete(t.layers, key)
		}
		t.mu.Unlock()
		if l.err == nil {
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := t.kv.ReleasePool(rctx, l.id); err != nil {
				slog.Debug("council: could not release the review's pool", "pool", l.id, "error", err)
			}
		}
	}
}

// councilIdleCompact runs after an answer, while the council waits for the
// next message (the owner's design, Phase 6): the conversation with this
// answer is compacted now if it is near its trigger, so the next message
// applies the record at once. On PolyKV the root is then built from the
// compacted conversation, for the next turn to fork. A next turn that
// arrives meanwhile waits for it (councilRoots.wait).
func (s *Server) councilIdleCompact(c *councilCompactor, full []api.Message, answer string) {
	if strings.TrimSpace(answer) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	next := append(slices.Clone(full), api.Message{Role: "assistant", Content: answer})
	pressure := 0.0
	if t := c.tree; t != nil {
		if k, err := t.kv.KV(ctx); err == nil {
			// The owner's booking may be newer than the turn's first read:
			// on a first turn it is made by the planner's first call.
			if a, ok := t.learnGrant(k); ok {
				pressure = a.Pressure
			}
		}
	}
	applied, _ := c.apply(next)
	z, err := c.measure(ctx, applied)
	if err != nil || c.trigger(z, true, pressure) == "" {
		return
	}
	var done func()
	if c.tree != nil {
		done = councilRoots.promotion(c.tree.owner)
		defer done()
	}
	before := councilCompactions.get(c.key)
	out := c.compact(ctx, next, "", true, pressure)
	if after := councilCompactions.get(c.key); after == before || after == nil {
		return
	}
	slog.Info("council: compacted while idle", "session", c.key)
	if t := c.tree; t != nil {
		if err := t.buildRoot(ctx, out); err == nil {
			t.release()
		}
	}
}
