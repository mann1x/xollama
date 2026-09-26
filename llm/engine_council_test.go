package llm

import (
	"net/http"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func TestAPlacementReachesOnlyAnEngineWithSessions(t *testing.T) {
	zero := 0
	p := &Placement{PoolID: &zero, NumCtx: 16384, NumCtxMin: 32768}

	if pool, n, _ := placementFields(false, LlamaServerConfig{}, p); pool != nil || n != 0 {
		t.Error("stock llama.cpp must receive nothing")
	}
	no := false
	off := LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Session: &xollama.Session{Affinity: &no}}}
	if pool, n, _ := placementFields(true, off, p); pool != nil || n != 0 {
		t.Error("affinity off must drop the placement")
	}
	pool, n, nmin := placementFields(true, LlamaServerConfig{}, p)
	if pool == nil || *pool != 0 {
		t.Error("pool 0 is a pool: the engine numbers its first pool zero")
	}
	if n != 16384 || nmin != 16384 {
		t.Errorf("window %d/%d, want the floor clamped to the ask 16384/16384", n, nmin)
	}
	if pool, n, _ := placementFields(true, LlamaServerConfig{}, nil); pool != nil || n != 0 {
		t.Error("no placement, nothing sent")
	}
}

func TestResizeAnswers(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   ResizeResult
	}{
		{http.StatusOK, `{"window":8192}`, ResizeResult{Applied: 8192}},
		// opencoti's own answer: "window" is the window it replaced.
		{http.StatusOK, `{"found":true,"window":6656,"cells":6656,"used":5637,"sequences":1,"ok":true,"window_new":16384,"cells_new":16384,"cells_delta":9728}`, ResizeResult{Applied: 16384}},
		{http.StatusAccepted, `{}`, ResizeResult{Queued: true}},
		{http.StatusConflict, `{"error":{"type":"session_busy"}}`, ResizeResult{Refusal: "session_busy"}},
		{http.StatusTooManyRequests, `{"error":{"reason":"insufficient","largest_admissible":24576}}`, ResizeResult{Refusal: "insufficient", LargestAdmissible: 24576}},
		{http.StatusNotFound, ``, ResizeResult{Refusal: "Not Found"}},
		// opencoti's own refusal: the kind is error_kind, the type is generic.
		{http.StatusConflict, `{"error":{"code":409,"message":"the session has 2 active and 0 pending task(s)","type":"unavailable_error","error_kind":"session_busy","window":16384}}`, ResizeResult{Refusal: "session_busy"}},
	} {
		if got := parseResize(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%d %s: %+v, want %+v", tc.status, tc.body, got, tc.want)
		}
	}
}

// Ids the engine's router can carry go in the path; others (the council's
// `~` ids among them) in the body of the flat route.
func TestSessionOperationsRoute(t *testing.T) {
	if path, _ := sessionPath("xs-0123abcd", "close", nil); path != "/sessions/xs-0123abcd/close" {
		t.Errorf("plain id: %s", path)
	}
	path, body := sessionPath("xs-0123abcd~researcher-1", "resize", map[string]any{"num_ctx": 4096})
	if path != "/sessions/resize" || string(body) != `{"num_ctx":4096,"session_id":"xs-0123abcd~researcher-1"}` {
		t.Errorf("tilde id: %s %s", path, body)
	}
}

func TestPressureIsARecentRefusal(t *testing.T) {
	for _, tc := range []struct {
		p    *KVPressure
		want bool
	}{
		{nil, false},
		{&KVPressure{}, false},
		{&KVPressure{WindowS: 60, Refused60s: 3, LastRefusalAgeS: 12}, true},
		{&KVPressure{WindowS: 60, Refused60s: 3, LastRefusalAgeS: 75}, false},
		{&KVPressure{Refused60s: 1, LastRefusalAgeS: 5}, true},
	} {
		if got := tc.p.Active(); got != tc.want {
			t.Errorf("%+v: active %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestCouncilSeatsRideOnThePoolFlag(t *testing.T) {
	t.Setenv("XOLLAMA_SESSION_POOL", "false")
	if got := enginePoolSeats(LlamaServerConfig{CouncilPools: 4}, false); got != 4 {
		t.Errorf("council seats alone = %d, want 4", got)
	}
	if got := enginePoolSeats(LlamaServerConfig{CouncilPools: 4}, true); got != 0 {
		t.Errorf("multimodal = %d, want 0", got)
	}
	if got := enginePoolSeats(LlamaServerConfig{}, false); got != 0 {
		t.Errorf("no council, no pooling = %d, want 0", got)
	}
}
