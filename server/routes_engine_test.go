package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/llm"
)

// introspectableLlm is a runner that also answers for its engine. The split
// between this and mockLlm is the point of the optional interface: a runner
// with no HTTP surface simply does not implement it.
type introspectableLlm struct {
	*mockLlm
	engine      string
	status      int
	body        string
	contentType string
	err         error
	gotPaths    []string
	gotCalls    []engineCallSeen
}

type engineCallSeen struct {
	method, endpoint, query, body string
}

func (s *introspectableLlm) EngineName() string { return s.engine }

func (s *introspectableLlm) EngineDo(ctx context.Context, call llm.EngineCall) (*http.Response, error) {
	// The real implementation validates first; mirroring that here keeps the
	// route-table test honest about where the refusal happens.
	endpoint, err := llm.ValidEngineEndpoint(call.Method, call.Endpoint)
	if err != nil {
		return nil, err
	}
	seen := engineCallSeen{method: call.Method, endpoint: endpoint, query: call.Query.Encode()}
	if call.Body != nil {
		b, _ := io.ReadAll(call.Body)
		seen.body = string(b)
	}
	s.gotPaths = append(s.gotPaths, endpoint)
	s.gotCalls = append(s.gotCalls, seen)
	if s.err != nil {
		return nil, s.err
	}
	h := http.Header{}
	if s.contentType != "" {
		h.Set("Content-Type", s.contentType)
	}
	return &http.Response{StatusCode: s.status, Header: h, Body: io.NopCloser(strings.NewReader(s.body))}, nil
}

func engineTestServer(t *testing.T, runners map[string]*runnerRef) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	s := &Server{sched: &Scheduler{loaded: runners}}
	r := gin.New()
	r.GET("/api/engine", s.EngineHandler)
	r.POST("/api/engine", s.EngineHandler)
	r.DELETE("/api/engine", s.EngineHandler)
	return r
}

func engineRunner(name string, llama llm.LlamaServer) *runnerRef {
	return &runnerRef{
		llama: llama,
		model: &Model{Name: name, ShortName: name, ModelPath: "/tmp/" + name},
	}
}

func engineGet(t *testing.T, r *gin.Engine, query string) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/engine"+query, nil))
	return w
}

// TestEngineListSaysWhichEngineServedEachModel is the question the endpoint
// exists for at its coarsest: routing can decline a device and fall back, so
// the engine that answered is not always the one that was asked for.
func TestEngineListSaysWhichEngineServedEachModel(t *testing.T) {
	r := engineTestServer(t, map[string]*runnerRef{
		"a": engineRunner("qwen3:8b", &introspectableLlm{mockLlm: &mockLlm{}, engine: "opencoti"}),
		"b": engineRunner("gemma3:4b", &mockLlm{}),
	})

	w := engineGet(t, r, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}

	var got engineListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 {
		t.Fatalf("listed %d models, want 2: %+v", len(got.Models), got.Models)
	}

	byName := map[string]engineModelResponse{}
	for _, m := range got.Models {
		byName[m.Model] = m
	}
	if m := byName["qwen3:8b"]; m.Engine != "opencoti" || !m.Readable {
		t.Errorf("qwen3:8b = %+v, want opencoti and readable", m)
	}
	// A runner with no HTTP surface is reported as unreadable rather than
	// omitted: "this model exists and cannot be read" is the useful answer.
	if m := byName["gemma3:4b"]; m.Readable {
		t.Errorf("gemma3:4b = %+v, want not readable", m)
	}
	if len(got.Endpoints) == 0 {
		t.Error("the listing must say what may be asked for, or a caller has to guess")
	}
}

func TestEngineReadReturnsTheEngineAnswerUnaltered(t *testing.T) {
	runner := &introspectableLlm{
		mockLlm: &mockLlm{},
		engine:  "opencoti",
		status:  http.StatusOK,
		body:    `{"default_generation_settings":{"n_ctx":8192},"opencoti":{"kv":{"effective":"kvarn3"}}}`,
	}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	w := engineGet(t, r, "?model=qwen3:8b")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}

	var got engineReadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Engine != "opencoti" || got.Endpoint != "props" || got.Status != http.StatusOK {
		t.Errorf("got %+v, want opencoti/props/200", got)
	}
	// Nothing here parses or renames the engine's fields: a translation layer
	// over someone else's evolving schema goes stale without saying so.
	if string(got.Body) != runner.body {
		t.Errorf("body = %s, want it verbatim: %s", got.Body, runner.body)
	}
	if got.Text != "" {
		t.Errorf("JSON must not also be reported as text, got %q", got.Text)
	}
}

// TestEngineReadDefaultsToProps pins the default, because /props is the one
// that answers "did my setting apply?".
func TestEngineReadDefaultsToProps(t *testing.T) {
	runner := &introspectableLlm{mockLlm: &mockLlm{}, engine: "llamacpp", status: http.StatusOK, body: "{}"}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	if w := engineGet(t, r, "?model=qwen3:8b"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if len(runner.gotPaths) != 1 || runner.gotPaths[0] != "props" {
		t.Errorf("read %v, want [props]", runner.gotPaths)
	}
}

// TestEngineReadPassesThroughTheEngineStatus covers the case that makes this
// worth having on stock too: a 404 is how llama.cpp says it has no such
// feature, and flattening it into an error would hide the clearest answer
// available about an opencoti-only endpoint.
func TestEngineReadPassesThroughTheEngineStatus(t *testing.T) {
	runner := &introspectableLlm{
		mockLlm: &mockLlm{},
		engine:  "llamacpp",
		status:  http.StatusNotFound,
		body:    `{"error":"not found"}`,
	}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	w := engineGet(t, r, "?model=qwen3:8b&endpoint=polykv/pools")
	if w.Code != http.StatusOK {
		t.Fatalf("our own status = %d, want 200: the engine's 404 is the answer, not our failure", w.Code)
	}

	var got engineReadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != http.StatusNotFound {
		t.Errorf("engine status = %d, want it reported as 404", got.Status)
	}
}

// TestEngineReadCarriesNonJSONAsText covers /metrics, which is Prometheus text.
func TestEngineReadCarriesNonJSONAsText(t *testing.T) {
	runner := &introspectableLlm{
		mockLlm: &mockLlm{},
		engine:  "opencoti",
		status:  http.StatusOK,
		body:    "# HELP llamacpp:tokens_predicted_total\nllamacpp:tokens_predicted_total 42\n",
	}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	w := engineGet(t, r, "?model=qwen3:8b&endpoint=metrics")
	var got engineReadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "tokens_predicted_total") {
		t.Errorf("text = %q, want the metrics body", got.Text)
	}
	if len(got.Body) != 0 {
		t.Errorf("non-JSON must not be reported as a JSON body, got %s", got.Body)
	}
}

// TestEngineReadRefusesToDriveTheEngine is the guard on what stays out: a path
// the caller composes, and a GET on a route the engine only takes as POST.
func TestEngineReadRefusesToDriveTheEngine(t *testing.T) {
	runner := &introspectableLlm{mockLlm: &mockLlm{}, engine: "opencoti", status: http.StatusOK, body: "{}"}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	for _, endpoint := range []string{
		"completion",
		"slots/0?action=restore",
		"../../../etc/passwd",
		"health%0d%0aX-Injected:%201",
		"apply-template",
	} {
		t.Run(endpoint, func(t *testing.T) {
			w := engineGet(t, r, "?model=qwen3:8b&endpoint="+endpoint)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %q; body = %s", w.Code, endpoint, w.Body)
			}
			if len(runner.gotPaths) != 0 {
				t.Fatalf("reached the engine with %v", runner.gotPaths)
			}
		})
	}
}

// TestEngineReadOnAModelThatIsNotLoaded is deliberately distinct from "no such
// model": a model that exists but is not running has no engine to ask.
func TestEngineReadOnAModelThatIsNotLoaded(t *testing.T) {
	r := engineTestServer(t, map[string]*runnerRef{
		"a": engineRunner("qwen3:8b", &introspectableLlm{mockLlm: &mockLlm{}, engine: "opencoti"}),
	})

	w := engineGet(t, r, "?model=llama3:70b")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not loaded") {
		t.Errorf("body = %s, want it to say the model is not loaded", w.Body)
	}
}

// TestEngineReadOnARunnerWithNoSurface covers the MLX path, which has no HTTP
// surface to read at all.
func TestEngineReadOnARunnerWithNoSurface(t *testing.T) {
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", &mockLlm{})})

	if w := engineGet(t, r, "?model=qwen3:8b"); w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func engineCall(t *testing.T, r *gin.Engine, method, query, body string) *httptest.ResponseRecorder {
	t.Helper()

	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/engine"+query, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r.ServeHTTP(w, req)
	return w
}

// TestEngineControlReachesTheEngine: every opencoti control route is callable
// through xollama, body and query forwarded, and the engine's answer returned
// as a read's is.
func TestEngineControlReachesTheEngine(t *testing.T) {
	for _, tc := range []struct {
		method, endpoint, body string
	}{
		{http.MethodPost, "polykv/pools", `{"tokens":[1,2,3],"session_id":"s","pin":true}`},
		{http.MethodPost, "polykv/pools/0/fork", `{"tokens":[1,2,3,4]}`},
		{http.MethodDelete, "polykv/pools/0", ""},
		{http.MethodPost, "sessions/chat-1/resize", `{"num_ctx":8192,"deferred":true}`},
		{http.MethodPost, "sessions/close", `{"session_id":"a/b"}`},
		{http.MethodDelete, "sessions/chat-1", ""},
		{http.MethodPost, "elastic", `{"budget":4}`},
		{http.MethodPost, "apply-template", `{"messages":[]}`},
		{http.MethodPost, "lock/acquire", `{"name":"x"}`},
	} {
		t.Run(tc.method+" "+tc.endpoint, func(t *testing.T) {
			runner := &introspectableLlm{mockLlm: &mockLlm{}, engine: "opencoti", status: http.StatusAccepted, body: `{"queued":true}`}
			r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

			w := engineCall(t, r, tc.method, "?model=qwen3:8b&endpoint="+tc.endpoint, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body)
			}
			if len(runner.gotCalls) != 1 {
				t.Fatalf("engine saw %d calls, want 1", len(runner.gotCalls))
			}
			got := runner.gotCalls[0]
			if got.method != tc.method || got.endpoint != tc.endpoint || got.body != tc.body {
				t.Errorf("engine saw %+v, want %s %s %s", got, tc.method, tc.endpoint, tc.body)
			}
			var res engineReadResponse
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if res.Status != http.StatusAccepted || res.Method != tc.method || string(res.Body) != runner.body {
				t.Errorf("answer %+v, want the engine's 202 verbatim", res)
			}
		})
	}
}

// TestEngineCallForwardsTheQueryButNotOurOwn: ?live=1 reaches the engine;
// model and endpoint are ours and do not.
func TestEngineCallForwardsTheQueryButNotOurOwn(t *testing.T) {
	runner := &introspectableLlm{mockLlm: &mockLlm{}, engine: "opencoti", status: http.StatusOK, body: "{}"}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	engineGet(t, r, "?model=qwen3:8b&endpoint=kv&live=1")
	if len(runner.gotCalls) != 1 || runner.gotCalls[0].endpoint != "kv" || runner.gotCalls[0].query != "live=1" {
		t.Fatalf("engine saw %+v, want GET kv?live=1", runner.gotCalls)
	}
}

// TestEngineStreamsArePassedThrough: /polykv/tps is SSE, and wrapping it would
// mean waiting for an end that never comes.
func TestEngineStreamsArePassedThrough(t *testing.T) {
	events := "data: {\"tps\":41.5}\n\ndata: {\"tps\":42.0}\n\n"
	runner := &introspectableLlm{mockLlm: &mockLlm{}, engine: "opencoti", status: http.StatusOK, body: events, contentType: "text/event-stream"}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	w := engineGet(t, r, "?model=qwen3:8b&endpoint=polykv/tps")
	if w.Code != http.StatusOK || w.Body.String() != events {
		t.Fatalf("status %d body %q, want the events verbatim", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type %q, want text/event-stream", ct)
	}
}

// TestEngineCallRefusesWhatXollamaServesItself: inference goes through the
// scheduler, and the engine's URL-fetching routes never go anywhere.
func TestEngineCallRefusesWhatXollamaServesItself(t *testing.T) {
	runner := &introspectableLlm{mockLlm: &mockLlm{}, engine: "opencoti", status: http.StatusOK, body: "{}"}
	r := engineTestServer(t, map[string]*runnerRef{"a": engineRunner("qwen3:8b", runner)})

	for _, endpoint := range []string{
		"completion", "completions", "v1/chat/completions", "v1/messages", "embeddings", "infill",
		"cors-proxy", "tools", "models/load",
		"sessions/a%2fb/close", "polykv/pools/../kv", "polykv/pools/./fork", "sessions/..",
		"polykv/pools/0/fork/extra", "//elsewhere.example/polykv/pools",
	} {
		t.Run(endpoint, func(t *testing.T) {
			w := engineCall(t, r, http.MethodPost, "?model=qwen3:8b&endpoint="+endpoint, `{}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body)
			}
		})
	}
	if len(runner.gotCalls) != 0 {
		t.Fatalf("reached the engine with %v", runner.gotCalls)
	}
}

// TestEngineControlNeedsAModel: a control call with no model has no engine.
func TestEngineControlNeedsAModel(t *testing.T) {
	r := engineTestServer(t, map[string]*runnerRef{})
	if w := engineCall(t, r, http.MethodPost, "?endpoint=polykv/pools", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
