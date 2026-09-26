package llm

// xollama: the engine side of a council turn on PolyKV -- see
// plans/agentic-council-chat.md ("PolyKV layout for this flow", "Context,
// pressure and compaction") and /shared/dev/docs/cerebriline-polykv-integration.md.
// Additive; the wiring is the `council` hook in llama_server.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Placement puts one council call on the engine: the pool it attaches to, and
// for the call that opens the owner session, the window it books. Applied only
// on an engine with session affinity; on any other engine nothing is sent.
type Placement struct {
	// PoolID attaches the call to a pool the council created. It wins over the
	// automatic prefix pool and is sent whether or not automatic pooling is on.
	PoolID *int
	// NumCtx and NumCtxMin book the session's window: the owner states them,
	// a worker never does (it is charged against its owner).
	NumCtx    int
	NumCtxMin int
}

// placementFields decides what a placed call carries, given the engine.
func placementFields(engineHasSessions bool, cfg LlamaServerConfig, p *Placement) (pool *int, numCtx, numCtxMin int) {
	if p == nil || !engineHasSessions || !resolveSessionSettings(cfg).Affinity {
		return nil, 0, 0
	}
	if p.PoolID != nil && *p.PoolID >= 0 {
		pool = p.PoolID
	}
	if p.NumCtx > 0 {
		numCtx, numCtxMin = p.NumCtx, min(max(p.NumCtxMin, 0), p.NumCtx)
	}
	return pool, numCtx, numCtxMin
}

// PolyKV is what a runner on opencoti offers a council: pools it owns, the
// sessions it opened, and the engine's view of the cache. A runner that does
// not implement it, or whose PolyKV reports false, has none of this.
type PolyKV interface {
	// PolyKV reports whether this load can carry a council's pool tree: the
	// engine is opencoti, sessions are on, seats were reserved and the engine
	// advertises the pool and session features.
	PolyKV(ctx context.Context) bool
	// Features is the engine's /props feature set.
	Features(ctx context.Context) map[string]bool
	// CreatePool makes a pinned pool over prompt, owned by session; with a
	// parent it forks that pool instead of starting from nothing.
	CreatePool(ctx context.Context, session string, parent *int, prompt string) (PoolInfo, error)
	ReleasePool(ctx context.Context, id int) error
	// CloseSession ends a session and frees its booking. An unknown session is
	// not an error: the engine may have reclaimed it already.
	CloseSession(ctx context.Context, id string) error
	// KV is the engine's cache state: the allocations and the pressure.
	KV(ctx context.Context) (KVStatus, error)
	// Resize moves a session's booking between requests (kv_resize_v1).
	Resize(ctx context.Context, id string, numCtx int, deferred bool) (ResizeResult, error)
}

// PoolInfo is a created pool.
type PoolInfo struct {
	ID int `json:"pool_id"`
	// Len is the tokens the pool holds; OwnLen what it added to its parent.
	Len    int    `json:"prefix_len"`
	OwnLen int    `json:"own_len"`
	Warn   string `json:"warning"`
}

// KVStatus is GET /kv, the parts a council reads.
type KVStatus struct {
	Allocations []KVAllocation `json:"allocations"`
	Pressure    *KVPressure    `json:"pressure"`
	// RS is the recurrent-state cache, on a model that keeps one per
	// sequence (hybrids: Qwen3.5, Qwen3-Next, LFM2, Nemotron-H). Nil otherwise.
	RS *KVRecurrent `json:"rs"`
}

// KVRecurrent is /kv's rs block: every live sequence, pool or slot, holds one
// state cell, and an elastic cache commits them in chunks up to its cap.
type KVRecurrent struct {
	CellsCommitted int `json:"cells_committed"`
	CellsCap       int `json:"cells_cap"`
	CellsFree      int `json:"cells_free"`
}

// KVAllocation is one session's booking.
type KVAllocation struct {
	SessionID string  `json:"session_id"`
	Window    int     `json:"window"`
	Cells     int     `json:"cells"`
	Used      int     `json:"used"`
	Pressure  float64 `json:"pressure"`
	// ResizePending is a deferred resize not yet applied (kv_resize_deferred_v1).
	ResizePending int `json:"resize_pending"`
}

// KVPressure is the engine-wide refusal record (kv_pressure_v1).
type KVPressure struct {
	WindowS             float64 `json:"window_s"`
	Refused60s          int     `json:"refused_60s"`
	RefusedNeededMax60s int     `json:"refused_needed_max_60s"`
	LastRefusalAgeS     float64 `json:"last_refusal_age_s"`
}

// Active reports whether others are being refused right now: a refusal inside
// the engine's own window, counted from the read. An absent field is not
// pressure.
func (p *KVPressure) Active() bool {
	if p == nil || p.Refused60s == 0 {
		return false
	}
	window := p.WindowS
	if window <= 0 {
		window = 60
	}
	return p.LastRefusalAgeS >= 0 && p.LastRefusalAgeS < window
}

// Session returns the allocation of one session, if the engine holds it.
func (k KVStatus) Session(id string) (KVAllocation, bool) {
	for _, a := range k.Allocations {
		if a.SessionID == id {
			return a, true
		}
	}
	return KVAllocation{}, false
}

// ResizeResult is what a resize answered.
type ResizeResult struct {
	// Applied is the new window; zero when the resize was queued or refused.
	Applied int
	// Queued is a deferred resize the engine will apply once the session idles.
	Queued bool
	// Refusal is the engine's reason (session_busy, used_exceeds_window,
	// per_request_session, ...), empty on success.
	Refusal string
	// LargestAdmissible is the most a refused grow could have had.
	LargestAdmissible int
}

// councilFeatures are the flags a council's pool tree needs from the engine.
var councilFeatures = []string{"polykv_subpools_v1", "kv_status_v1"}

func (s *llamaServerRunner) PolyKV(ctx context.Context) bool {
	if !s.usedOpencoti || s.launch.config.CouncilPools <= 0 || !resolveSessionSettings(s.launch.config).Affinity {
		return false
	}
	f := s.Features(ctx)
	for _, want := range councilFeatures {
		if !f[want] {
			return false
		}
	}
	return true
}

func (s *llamaServerRunner) Features(ctx context.Context) map[string]bool {
	s.featuresOnce.Do(func() {
		s.features = map[string]bool{}
		status, out, err := s.engineRequest(ctx, http.MethodGet, "/props", nil)
		if err != nil || status != http.StatusOK {
			return
		}
		var props struct {
			Features []string `json:"features"`
		}
		if json.Unmarshal(out, &props) == nil {
			for _, f := range props.Features {
				s.features[f] = true
			}
		}
	})
	return s.features
}

func (s *llamaServerRunner) CreatePool(ctx context.Context, session string, parent *int, prompt string) (PoolInfo, error) {
	if prompt == "" {
		return PoolInfo{}, errors.New("refusing to create a pool over no text")
	}
	// Tokens, not text: the engine's own tokenization of the prompt with the
	// flags a completion uses, as the automatic pools do, so a member's prompt
	// and its pool agree token for token. A fork takes the child's FULL prefix.
	tokens, err := s.tokenizePrompt(ctx, prompt)
	if err != nil {
		return PoolInfo{}, err
	}
	fields := map[string]any{"tokens": tokens, "pin": true}
	if session != "" {
		fields["session_id"] = session
	} else {
		// No owner (pool_unowned_v1): charged to the base, and each request
		// that attaches books its own window.
		fields["unowned"] = true
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return PoolInfo{}, err
	}
	path := "/polykv/pools"
	if parent != nil {
		path = fmt.Sprintf("/polykv/pools/%d/fork", *parent)
	}
	status, out, err := s.engineRequestQueued(ctx, http.MethodPost, path, body)
	if err != nil {
		return PoolInfo{}, err
	}
	if status != http.StatusOK {
		return PoolInfo{}, fmt.Errorf("engine refused the council pool: %s: %s", http.StatusText(status), bytes.TrimSpace(out))
	}
	var p PoolInfo
	if err := json.Unmarshal(out, &p); err != nil {
		return PoolInfo{}, fmt.Errorf("engine returned an unreadable pool: %w", err)
	}
	// The first pool is numbered 0, so presence is read through a pointer: a
	// missing id must not read as pool 0.
	var id struct {
		ID *int `json:"pool_id"`
	}
	if json.Unmarshal(out, &id) != nil || id.ID == nil || *id.ID < 0 {
		return PoolInfo{}, errors.New("engine returned no pool id")
	}
	return p, nil
}

func (s *llamaServerRunner) ReleasePool(ctx context.Context, id int) error {
	return s.releasePool(ctx, id)
}

func (s *llamaServerRunner) CloseSession(ctx context.Context, id string) error {
	path, body := sessionPath(id, "close", nil)
	status, out, err := s.engineRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		return fmt.Errorf("engine refused to close session %q: %s: %s", id, http.StatusText(status), bytes.TrimSpace(out))
	}
	return nil
}

func (s *llamaServerRunner) KV(ctx context.Context) (KVStatus, error) {
	status, out, err := s.engineRequest(ctx, http.MethodGet, "/kv", nil)
	if err != nil {
		return KVStatus{}, err
	}
	if status != http.StatusOK {
		return KVStatus{}, fmt.Errorf("engine /kv: %s", http.StatusText(status))
	}
	var k KVStatus
	return k, json.Unmarshal(out, &k)
}

func (s *llamaServerRunner) Resize(ctx context.Context, id string, numCtx int, deferred bool) (ResizeResult, error) {
	fields := map[string]any{"num_ctx": numCtx}
	if deferred && s.Features(ctx)["kv_resize_deferred_v1"] {
		fields["deferred"] = true
	}
	path, body := sessionPath(id, "resize", fields)
	status, out, err := s.engineRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return ResizeResult{}, err
	}
	return parseResize(status, out), nil
}

// parseResize reads a resize answer: 200 applied, 202 queued, 4xx refused.
func parseResize(status int, out []byte) ResizeResult {
	var r struct {
		NumCtx            int    `json:"num_ctx"`
		Window            int    `json:"window"`
		LargestAdmissible int    `json:"largest_admissible"`
		Reason            string `json:"reason"`
		Error             struct {
			Type              string `json:"type"`
			Reason            string `json:"reason"`
			LargestAdmissible int    `json:"largest_admissible"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &r)
	switch status {
	case http.StatusOK:
		return ResizeResult{Applied: max(r.Window, r.NumCtx)}
	case http.StatusAccepted:
		return ResizeResult{Queued: true}
	}
	reason := firstNonEmpty(r.Error.Reason, r.Error.Type, r.Reason, http.StatusText(status))
	return ResizeResult{Refusal: reason, LargestAdmissible: max(r.LargestAdmissible, r.Error.LargestAdmissible)}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// sessionPath routes a session operation. An id the engine's path router can
// carry goes in the path; anything else in the body of the flat route.
func sessionPath(id, op string, fields map[string]any) (string, []byte) {
	if fields == nil {
		fields = map[string]any{}
	}
	if isPathSafe(id) {
		b, _ := json.Marshal(fields)
		return "/sessions/" + url.PathEscape(id) + "/" + op, b
	}
	fields["session_id"] = id
	b, _ := json.Marshal(fields)
	return "/sessions/" + op, b
}

func isPathSafe(id string) bool {
	return id != "" && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) < 0
}

// engineRequestQueued is engineRequest with the engine's 429 read as a queue:
// it waits Retry-After and asks again, until ctx ends.
func (s *llamaServerRunner) engineRequestQueued(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	for {
		status, out, retry, err := s.engineRequestRetry(ctx, method, path, body)
		if err != nil || status != http.StatusTooManyRequests {
			return status, out, err
		}
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-time.After(retry):
		}
	}
}

func (s *llamaServerRunner) engineRequest(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	status, out, _, err := s.engineRequestRetry(ctx, method, path, body)
	return status, out, err
}

func (s *llamaServerRunner) engineRequestRetry(ctx context.Context, method, path string, body []byte) (int, []byte, time.Duration, error) {
	if s.port == 0 {
		return 0, nil, 0, errors.New("engine is not listening")
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", s.port, path), rd)
	if err != nil {
		return 0, nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.httpClient().Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	retry := time.Second
	if v, perr := strconv.Atoi(res.Header.Get("Retry-After")); perr == nil && v > 0 {
		retry = time.Duration(v) * time.Second
	}
	return res.StatusCode, out, retry, err
}

// polykvState is the per-process PolyKV state a runner keeps.
type polykvState struct {
	featuresOnce sync.Once
	features     map[string]bool
}

// enginePoolSeats is --polykv-max-pools: the automatic prefix pools plus the
// seats a council reserved. A multimodal load has neither.
func enginePoolSeats(cfg LlamaServerConfig, multimodal bool) int {
	if multimodal {
		return 0
	}
	return effectivePoolCount(cfg, false) + max(cfg.CouncilPools, 0)
}
