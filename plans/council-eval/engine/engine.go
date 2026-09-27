// Package engine is council.Model over an opencoti (llama-server) OpenAI
// endpoint: streamed /v1/chat/completions, one engine session per member,
// closed when the member finishes. No pools: sharing is Phase 4.
package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"councileval/council"
)

// Model talks to one engine.
type Model struct {
	URL    string       // e.g. http://127.0.0.1:38311
	Client *http.Client // nil: http.DefaultClient
	// Session prefixes every member's engine session id; ids never hold '/'.
	Session string
	// NumCtx is stated on every member session. Left out, each member books
	// the engine's whole session_ctx_max for its request (measured on b65:
	// 65,536 cells), and the second parallel member is refused.
	NumCtx int

	PromptTokens, CachedTokens, Completion atomic.Int64
	Queued                                 atomic.Int64 // 429s waited out

	keep   sync.Map // sessions held until CloseAll
	Closes sync.Map // session id -> the engine's answer to its close

}

var routeSchema = map[string]any{"type": "object", "properties": map[string]any{
	"route": map[string]any{"type": "string", "enum": []string{"direct", "council"}}}, "required": []string{"route"}}

func planSchema(n int) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"plan":   map[string]any{"type": "string"},
		"briefs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": n, "maxItems": n}},
		"required": []string{"plan", "briefs"}}
}

func (m *Model) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return http.DefaultClient
}

// Stream implements council.Model.
func (m *Model) Stream(ctx context.Context, req council.Request, onToken func(string)) (string, error) {
	// The planner's calls (decision, direct answer, plan) share one session,
	// so each reuses the previous one's slot cache; it stays open until
	// CloseAll, because a close drops the KV. Every other member closes the
	// moment it finishes (rule 7).
	sid := fmt.Sprintf("%s~%s%d.%d", m.Session, req.Role, req.Index, req.Round)
	if req.Role == council.Planner {
		m.keep.Store(sid, true)
	} else {
		defer m.close(sid)
	}
	body := map[string]any{"messages": req.Messages, "stream": true, "seed": req.Seed,
		"temperature": req.Temperature, "max_tokens": req.MaxTokens, "session_id": sid,
		"stream_options": map[string]any{"include_usage": true}}
	if m.NumCtx > 0 {
		body["num_ctx"] = m.NumCtx
	}
	switch {
	case req.RouteOnly:
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "route", "schema": routeSchema}}
	case req.WantBriefs > 0:
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "plan", "schema": planSchema(req.WantBriefs)}}
	}
	buf, _ := json.Marshal(body)
	var resp *http.Response
	for {
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, m.URL+"/v1/chat/completions", bytes.NewReader(buf))
		if err != nil {
			return "", err
		}
		hr.Header.Set("Content-Type", "application/json")
		if resp, err = m.client().Do(hr); err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}
		// A 429 is a queue, not a failure (integration guide rule 9).
		resp.Body.Close()
		m.Queued.Add(1)
		wait := 2 * time.Second
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("engine: %s: %s", resp.Status, b)
	}
	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
				Details    struct {
					Cached int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		for _, c := range ev.Choices {
			if c.Delta.Content != "" {
				onToken(c.Delta.Content)
				out.WriteString(c.Delta.Content)
			}
		}
		if ev.Usage != nil {
			m.PromptTokens.Add(ev.Usage.Prompt)
			m.CachedTokens.Add(ev.Usage.Details.Cached)
			m.Completion.Add(ev.Usage.Completion)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return out.String(), sc.Err()
}

// CloseAll releases the sessions kept open across calls.
func (m *Model) CloseAll() {
	m.keep.Range(func(k, _ any) bool { m.close(k.(string)); m.keep.Delete(k); return true })
}

// close releases the member's session (rule 7); best effort, detached from
// the member's context so a cancelled member still gives its window back.
// Every answer is kept in Closes, so a run can prove nothing leaked.
func (m *Model) close(sid string) {
	r, err := http.NewRequest(http.MethodPost, m.URL+"/sessions/"+sid+"/close", strings.NewReader("{}"))
	if err != nil {
		return
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := m.client().Do(r)
	if err != nil {
		m.Closes.Store(sid, "error: "+err.Error())
		return
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	m.Closes.Store(sid, fmt.Sprintf("%d %s", resp.StatusCode, b))
}
