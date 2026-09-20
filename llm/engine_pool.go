package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
)

// xollama-hook: engine-session
//
// Shared prefix pools: one physical copy of a system prompt, not one per
// conversation.
//
// Agents built on one model overwhelmingly share their opening: the same system
// prompt, the same tool definitions, maybe a document. Served normally, every
// conversation stores and re-attends its own copy of that, so N agents cost N
// copies of a prefix that is byte-identical across all of them.
//
// A pool is that prefix, once. Conversations attach to it and keep only their
// own divergent suffix. The engine works out how much of an incoming prompt
// matches the pool by itself, token-exactly, so nothing here has to compute or
// declare a shared length -- getting that number wrong is the failure mode this
// design avoids by not having the number.
//
// The lifecycle is: watch two conversations that share a prefix, take the part
// they actually have in common, and materialise a pool from exactly those
// tokens. Later conversations with the same prefix attach to it.
//
// It used to be done by snapshotting a finished session instead, which was
// simpler and wrong in two ways. A session pool holds the WHOLE conversation --
// prefix, first question and first answer -- so most of what it pins can never
// match anything; measured on a live engine, a pool of 50 tokens against a
// shared prefix of 29. And on a sliding-window model an over-long pool is not
// merely wasteful: once the session that built it moves on, the pool is sole
// owner of its cells and the ones below its window become reclaimable, so a
// later conversation can attach over a hole and get quietly degraded attention
// with no error and no warning.
//
// Building the pool from the shared prefix itself removes both. The pool is
// exactly as long as the thing being shared, so the cells a sharer needs are
// the ones the pool's own window keeps alive.

// poolCreateTimeout bounds a pool creation. It runs after a response has
// already been delivered, so it must never be able to hold anything up.
const poolCreateTimeout = 30 * time.Second

// minPoolPrefixTokens is the shortest prefix worth a pool.
//
// A pool costs a reserved sequence for the life of the runner, and on a
// sliding-window model a reserved sequence costs its own window. Sharing a
// couple of hundred tokens does not repay that; sharing a system prompt and a
// set of tool schemas -- which is what this feature is for -- repays it many
// times over.
const minPoolPrefixTokens = 256

// maxPoolPrefixTokens caps both what is remembered while waiting for a second
// conversation and what is ultimately pooled, so a very long prompt cannot make
// this hold an unbounded amount of memory. A prefix longer than this is still
// pooled, just truncated to it.
const maxPoolPrefixTokens = 8192

// maxPendingPrefixes bounds how many first-sightings are held at once, for the
// same reason.
const maxPendingPrefixes = 8

// DerivePoolKey identifies the prefix a set of conversations would share.
//
// It is deliberately NOT DeriveSessionID. A session id identifies one
// conversation and therefore includes its first user message; a pool key
// identifies what several conversations have in common and must exclude it.
// Using the session id here would give every conversation its own pool, which
// is the situation pooling exists to fix.
//
// Returns "" when there is nothing worth pooling -- no system prompt and no
// tools means the shared prefix is empty, and a pool over nothing costs a
// reserved sequence to save nothing.
func DerivePoolKey(model string, messages []api.Message, tools api.Tools) string {
	h := sha256.New()
	fmt.Fprintf(h, "model:%s\n", model)

	var shared bool
	for _, m := range messages {
		if m.Role == "system" && m.Content != "" {
			fmt.Fprintf(h, "system:%s\n", m.Content)
			shared = true
		}
	}
	// Tool names rather than whole schemas: the names are what a caller keeps
	// stable, and a schema reordered by a JSON round trip would otherwise split
	// one pool into two.
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.Function.Name != "" {
			names = append(names, t.Function.Name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(h, "tool:%s\n", n)
		}
		shared = true
	}

	if !shared {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// poolRegistry remembers which pool holds which prefix, for one runner.
//
// It is bounded by the same number passed to the engine as
// --polykv-max-pools, because a pool id the engine has no seat for cannot be
// created. When the bound is reached the least recently used pool is released
// to make room -- before the create that needs the room, see claim -- which is
// a decision made here rather than left to the engine's idle sweep: the sweep
// would drop a pool while xollama still had its id, and the next attach would
// silently fall back to a full reprocess with nothing saying so.
type poolRegistry struct {
	mu    sync.Mutex
	max   int
	clock uint64
	byKey map[string]*poolEntry
	// creating dedupes in-flight creations, so a burst of first requests for
	// one prefix does not spend several pool seats on the same prefix.
	creating map[string]bool
	// pending holds the tokens of the FIRST conversation seen for a prefix,
	// until a second one arrives to be compared against it. The shared prefix
	// is what the two actually have in common, which is a thing to observe
	// rather than a thing to infer.
	pending map[string]*pendingPrefix
	seen    uint64
}

// pendingPrefix is one first-sighting waiting for its second.
type pendingPrefix struct {
	tokens []int
	seen   uint64
}

type poolEntry struct {
	id   int
	used uint64
}

func newPoolRegistry(max int) *poolRegistry {
	return &poolRegistry{
		max:      max,
		byKey:    map[string]*poolEntry{},
		creating: map[string]bool{},
		pending:  map[string]*pendingPrefix{},
	}
}

// offer reports what this conversation and an earlier one with the same prefix
// have in common, or nil when there is nothing to do yet.
//
// The first conversation for a prefix is remembered and nothing is created: one
// conversation is not evidence of a shared anything, and a pool for a prefix
// that never recurs is a reserved sequence spent on nobody. The second one is
// compared against it, and what they agree on -- token for token, from the
// start -- is the prefix worth pooling.
func (r *poolRegistry) offer(key string, tokens []int) []int {
	if r == nil || key == "" || len(tokens) == 0 {
		return nil
	}
	if len(tokens) > maxPoolPrefixTokens {
		tokens = tokens[:maxPoolPrefixTokens]
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byKey[key]; ok {
		return nil
	}
	if r.creating[key] {
		return nil
	}

	prev, ok := r.pending[key]
	if !ok {
		r.seen++
		r.pending[key] = &pendingPrefix{tokens: slices.Clone(tokens), seen: r.seen}
		r.evictPendingLocked()
		return nil
	}

	delete(r.pending, key)
	shared := commonTokenPrefix(prev.tokens, tokens)
	if len(shared) < minPoolPrefixTokens {
		return nil
	}
	return shared
}

// evictPendingLocked keeps the first-sighting map bounded. Dropping the oldest
// only costs the pool another pair of conversations to be noticed again.
func (r *poolRegistry) evictPendingLocked() {
	for len(r.pending) > maxPendingPrefixes {
		var oldestKey string
		var oldest *pendingPrefix
		for k, p := range r.pending {
			if oldest == nil || p.seen < oldest.seen {
				oldestKey, oldest = k, p
			}
		}
		if oldest == nil {
			return
		}
		delete(r.pending, oldestKey)
	}
}

// commonTokenPrefix is the longest run both token sequences begin with.
//
// Comparing TOKENS rather than the rendered strings is what makes the result
// safe to hand to the engine. A common string prefix can end in the middle of
// what the tokeniser treats as one unit, and the pool would then hold a last
// token that does not reappear when the same text is tokenised inside a longer
// prompt -- so the share would silently stop a token or two early, or not
// happen. A run of whole tokens that two real prompts both start with is, by
// construction, a valid prefix of both.
func commonTokenPrefix(a, b []int) []int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

// lookup returns the pool holding this prefix, marking it recently used.
func (r *poolRegistry) lookup(key string) (int, bool) {
	if r == nil || key == "" {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.byKey[key]
	if !ok {
		return 0, false
	}
	r.clock++
	e.used = r.clock
	return e.id, true
}

// claim reserves a seat for a pool over this prefix, so only one of a burst of
// simultaneous first requests creates it, and returns the pools that must be
// released BEFORE the create is attempted.
//
// The ordering is not a preference. The engine holds exactly
// --polykv-max-pools seats, and a create past the last one is refused outright
// -- "pool capacity exhausted (--polykv-max-pools); release a pool first" --
// rather than queued, and rather than the engine evicting something itself.
// Releasing after a successful create, which is what this used to do,
// therefore never runs at the one moment it was needed: the create it was
// making room for is the create that failed.
//
// Seats are held by live pools AND by creations still in flight, so both count
// against the bound. When only in-flight creations hold them there is nothing
// safe to evict -- releasing another request's pool-to-be would move the
// failure rather than fix it -- so the claim is refused and the prefix is tried
// again on a later request.
func (r *poolRegistry) claim(key string) ([]int, bool) {
	if r == nil || key == "" || r.max <= 0 {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byKey[key]; ok {
		return nil, false
	}
	if r.creating[key] {
		return nil, false
	}
	r.creating[key] = true

	var release []int
	for len(r.byKey)+len(r.creating) > r.max {
		var oldestKey string
		var oldest *poolEntry
		for k, e := range r.byKey {
			if oldest == nil || e.used < oldest.used {
				oldestKey, oldest = k, e
			}
		}
		if oldest == nil {
			delete(r.creating, key)
			return nil, false
		}
		release = append(release, oldest.id)
		delete(r.byKey, oldestKey)
	}
	return release, true
}

// remember records a created pool. Room for it was made by claim, before the
// create ran, so nothing is evicted here.
func (r *poolRegistry) remember(key string, id int) {
	if r == nil || key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.creating, key)
	r.clock++
	r.byKey[key] = &poolEntry{id: id, used: r.clock}
}

// abandon drops a claim that did not produce a pool, so a later request may try
// again rather than the prefix being permanently un-poolable.
func (r *poolRegistry) abandon(key string) {
	if r == nil || key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.creating, key)
}

// drain empties the registry and returns every pool id in it, for shutdown.
func (r *poolRegistry) drain() []int {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]int, 0, len(r.byKey))
	for _, e := range r.byKey {
		ids = append(ids, e.id)
	}
	r.byKey = map[string]*poolEntry{}
	r.creating = map[string]bool{}
	r.pending = map[string]*pendingPrefix{}
	sort.Ints(ids)
	return ids
}

// poolCreateResponse is the part of the engine's pool JSON this needs. The
// engine sends a good deal more; everything else is its business.
type poolCreateResponse struct {
	PoolID int `json:"pool_id"`
}

// createPoolFromTokens materialises a pool from the shared prefix itself.
//
// This is the "tokens" form of the create rather than "from_session", and the
// difference is the whole point: the pool is exactly the prefix, so there is
// nothing in it that cannot be shared and nothing below a sharer's window that
// can be reclaimed out from under them. It is also the only form that is
// correct on every architecture rather than on attention-only ones.
//
// pin is set because these pools are ours to manage: an unpinned pool is swept
// once it goes quiet, which would leave xollama holding an id the engine has
// forgotten -- and a stale id is not an error, it is a silent full reprocess,
// so the sharing would just stop with nothing to see. ephemeral is cleared for
// the same reason; a pinned pool is never reaped whatever it says, but saying
// the true thing costs nothing and reads correctly in the engine's own JSON.
func (s *llamaServerRunner) createPoolFromTokens(ctx context.Context, tokens []int) (int, error) {
	if len(tokens) == 0 {
		return 0, fmt.Errorf("refusing to create a pool over no tokens")
	}

	body, err := json.Marshal(map[string]any{
		"tokens":    tokens,
		"pin":       true,
		"ephemeral": false,
	})
	if err != nil {
		return 0, err
	}

	status, out, err := s.postPool(ctx, "/polykv/pools", body)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("engine refused to create a prefix pool: %s: %s", http.StatusText(status), bytes.TrimSpace(out))
	}

	var res poolCreateResponse
	if err := json.Unmarshal(out, &res); err != nil {
		return 0, fmt.Errorf("engine returned an unreadable pool: %w", err)
	}
	if res.PoolID < 0 {
		return 0, fmt.Errorf("engine returned no pool id")
	}
	return res.PoolID, nil
}

// tokenizeResponse is the part of the engine's tokenize JSON this needs.
type tokenizeResponse struct {
	Tokens []int `json:"tokens"`
}

// tokenizePrompt asks the engine to tokenise a rendered prompt exactly as it
// would when serving it.
//
// add_special and parse_special are both true because that is what the engine
// does to a prompt it is about to run: tokenize_input_prompts(vocab, mctx,
// prompt, true, true) on the completion and chat paths. /tokenize defaults
// add_special to FALSE, so leaving it out would drop the leading token on any
// model whose tokeniser adds one, and a pool whose first token differs from the
// request's shares nothing at all.
func (s *llamaServerRunner) tokenizePrompt(ctx context.Context, prompt string) ([]int, error) {
	body, err := json.Marshal(map[string]any{
		"content":       prompt,
		"add_special":   true,
		"parse_special": true,
	})
	if err != nil {
		return nil, err
	}

	status, out, err := s.postPool(ctx, "/tokenize", body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("engine refused to tokenize a prompt: %s: %s", http.StatusText(status), bytes.TrimSpace(out))
	}

	var res tokenizeResponse
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("engine returned an unreadable tokenization: %w", err)
	}
	return res.Tokens, nil
}

// releasePool detaches a pool and reclaims its cells. Release is a POST, not a
// DELETE; the engine has no DELETE route.
func (s *llamaServerRunner) releasePool(ctx context.Context, id int) error {
	status, out, err := s.postPool(ctx, fmt.Sprintf("/polykv/pools/%d/release", id), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("engine refused to release pool %d: %s: %s", id, http.StatusText(status), bytes.TrimSpace(out))
	}
	return nil
}

// postPool is one write against the engine's pool registry.
func (s *llamaServerRunner) postPool(ctx context.Context, path string, body []byte) (int, []byte, error) {
	if s.port == 0 {
		return 0, nil, fmt.Errorf("engine is not listening")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", s.port, path), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := s.httpClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()

	out, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, out, nil
}

// poolFor returns the pool this request should attach to, or 0 for none.
func (s *llamaServerRunner) poolFor(key string) int {
	id, ok := s.pools.lookup(key)
	if !ok {
		return 0
	}
	return id
}

// poolSource is where the rendered prompt for a prefix comes from.
//
// The two request paths render in different places, and this has to use
// whichever one actually produced the tokens the engine will see. On the
// Completion path ollama renders the prompt itself and it is right here. On the
// Chat path the ENGINE owns the template, so the only faithful rendering is the
// engine's own -- asking it is not a round trip we could skip by guessing.
type poolSource struct {
	prompt string
	chat   *ChatRequest
}

// render returns the prompt the engine will tokenise for this request.
func (s *llamaServerRunner) render(ctx context.Context, src poolSource) (string, error) {
	if src.prompt != "" {
		return src.prompt, nil
	}
	if src.chat == nil {
		return "", nil
	}
	return s.ApplyChatTemplate(ctx, *src.chat)
}

// capturePool works towards a pool for this prefix, in the background.
//
// It runs after the response has been delivered, on its own context, because it
// exists to make LATER conversations cheaper and must never delay or fail this
// one. Everything it can go wrong with is logged and dropped: a load that
// cannot pool is a load that works exactly as it did before pooling existed.
//
// The first conversation for a prefix only gets remembered. The second is
// compared against it, and the tokens they agree on become the pool. That is a
// request later than it used to be, deliberately: it means a prefix that turns
// up once never costs a reserved sequence, and it means the pool is built from
// a prefix two real conversations were observed to share rather than one we
// worked out they ought to.
func (s *llamaServerRunner) capturePool(key string, src poolSource) {
	if key == "" || !s.usedOpencoti || s.pools == nil {
		return
	}
	if src.prompt == "" && src.chat == nil {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), poolCreateTimeout)
		defer cancel()

		prompt, err := s.render(ctx, src)
		if err != nil || prompt == "" {
			slog.Debug("could not render a prompt for prefix pooling", "model", s.modelPath, "error", err)
			return
		}

		tokens, err := s.tokenizePrompt(ctx, prompt)
		if err != nil {
			slog.Debug("could not tokenize a prompt for prefix pooling", "model", s.modelPath, "error", err)
			return
		}

		shared := s.pools.offer(key, tokens)
		if len(shared) == 0 {
			return
		}

		release, ok := s.pools.claim(key)
		if !ok {
			return
		}

		// Release first. The seat has to be free before the create, not after
		// it: see claim.
		for _, old := range release {
			if err := s.releasePool(ctx, old); err != nil {
				slog.Warn("could not release a superseded prefix pool; the engine may now refuse the pool replacing it",
					"pool_id", old, "error", err)
			}
		}

		id, err := s.createPoolFromTokens(ctx, shared)
		if err != nil {
			slog.Warn("could not create a shared prefix pool; this model keeps a private copy of the prefix per conversation",
				"model", s.modelPath, "error", err)
			s.pools.abandon(key)
			return
		}

		slog.Info("shared prefix pool created", "model", s.modelPath, "pool_id", id, "prefix_tokens", len(shared))
		s.pools.remember(key, id)
	}()
}

// releasePools drops every pool this runner holds. Called on shutdown, because
// the pools are pinned and a pinned pool is never swept.
func (s *llamaServerRunner) releasePools() {
	ids := s.pools.drain()
	if len(ids) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), poolCreateTimeout)
	defer cancel()

	for _, id := range ids {
		if err := s.releasePool(ctx, id); err != nil {
			slog.Debug("could not release a prefix pool at shutdown", "pool_id", id, "error", err)
		}
	}
}
