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
// The lifecycle is: serve one conversation normally, snapshot its context into
// a pool, and attach every later conversation with the same prefix to it. The
// snapshot is zero-copy on the engine side, which is why it is done from a
// session that has already run rather than by sending the prefix again.

// poolCreateTimeout bounds a pool creation. It runs after a response has
// already been delivered, so it must never be able to hold anything up.
const poolCreateTimeout = 30 * time.Second

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
// to make room, which is a decision made here rather than left to the engine's
// idle sweep: the sweep would drop a pool while xollama still had its id, and
// the next attach would silently fall back to a full reprocess with nothing
// saying so.
type poolRegistry struct {
	mu    sync.Mutex
	max   int
	clock uint64
	byKey map[string]*poolEntry
	// creating dedupes in-flight creations, so a burst of first requests for
	// one prefix does not spend several pool seats on the same prefix.
	creating map[string]bool
}

type poolEntry struct {
	id   int
	used uint64
}

func newPoolRegistry(max int) *poolRegistry {
	return &poolRegistry{max: max, byKey: map[string]*poolEntry{}, creating: map[string]bool{}}
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

// claim reserves the right to create a pool for this prefix, so only one of a
// burst of simultaneous first requests does.
func (r *poolRegistry) claim(key string) bool {
	if r == nil || key == "" || r.max <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byKey[key]; ok {
		return false
	}
	if r.creating[key] {
		return false
	}
	r.creating[key] = true
	return true
}

// remember records a created pool and returns any pool ids that must be
// released to stay within the bound.
func (r *poolRegistry) remember(key string, id int) []int {
	if r == nil || key == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.creating, key)
	r.clock++
	r.byKey[key] = &poolEntry{id: id, used: r.clock}

	var evict []int
	for len(r.byKey) > r.max && r.max > 0 {
		var oldestKey string
		var oldest *poolEntry
		for k, e := range r.byKey {
			if oldest == nil || e.used < oldest.used {
				oldestKey, oldest = k, e
			}
		}
		evict = append(evict, oldest.id)
		delete(r.byKey, oldestKey)
	}
	return evict
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
	sort.Ints(ids)
	return ids
}

// poolCreateResponse is the part of the engine's pool JSON this needs. The
// engine sends a good deal more; everything else is its business.
type poolCreateResponse struct {
	PoolID int `json:"pool_id"`
}

// createPoolFromSession snapshots a session that has already run into a pool.
//
// pin is set because these pools are ours to manage. An unpinned pool created
// from a session is swept after a minute of no attaches, which would leave
// xollama holding an id the engine has forgotten -- and a stale id is not an
// error, it is a silent full reprocess, so the sharing would simply stop with
// nothing to see. Pinning makes the lifetime ours, and drain releases them.
func (s *llamaServerRunner) createPoolFromSession(ctx context.Context, sessionID string) (int, error) {
	body, err := json.Marshal(map[string]any{"from_session": sessionID, "pin": true})
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

// capturePool snapshots a finished session into a pool, in the background.
//
// It runs after the response has been delivered, on its own context, because it
// exists to make the NEXT conversation cheaper and must never delay or fail
// this one. Everything it can go wrong with is logged and dropped: a load that
// cannot pool is a load that works exactly as it did before pooling existed.
func (s *llamaServerRunner) capturePool(key, sessionID string) {
	if key == "" || sessionID == "" || !s.usedOpencoti || !s.pools.claim(key) {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), poolCreateTimeout)
		defer cancel()

		id, err := s.createPoolFromSession(ctx, sessionID)
		if err != nil {
			slog.Warn("could not create a shared prefix pool; this model keeps a private copy of the prefix per conversation",
				"model", s.modelPath, "error", err)
			s.pools.abandon(key)
			return
		}

		slog.Info("shared prefix pool created", "model", s.modelPath, "pool_id", id)
		for _, old := range s.pools.remember(key, id) {
			if err := s.releasePool(ctx, old); err != nil {
				slog.Warn("could not release a superseded prefix pool", "pool_id", old, "error", err)
			}
		}
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
