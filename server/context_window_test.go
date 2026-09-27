package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/middleware"
)

// windowServer is a model whose runner reports window (0: reports nothing,
// as stock llama.cpp) on admission, before its first chunk.
func windowServer(t *testing.T, window int) *Server {
	t.Helper()
	mock := &mockRunner{}
	mock.CompletionFn = func(ctx context.Context, r llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
		llm.ReportContextWindow(ctx, window)
		fn(llm.CompletionResponse{Content: "hi"})
		fn(llm.CompletionResponse{Done: true, DoneReason: llm.DoneReasonStop})
		return nil
	}
	gin.SetMode(gin.TestMode)
	s := newServerWithMockRunner(t, mock)
	createMinimalGGUFModel(t, s, "win", nil, "{{ .Prompt }}", nil)
	return s
}

func TestTheEnginesWindowReachesTheClient(t *testing.T) {
	s := windowServer(t, 16384)
	stream, noStream := true, false
	for name, w := range map[string]*httptest.ResponseRecorder{
		"chat stream":       createRequest(t, s.ChatHandler, api.ChatRequest{Model: "win", Messages: []api.Message{{Role: "user", Content: "x"}}, Stream: &stream}),
		"chat no stream":    createRequest(t, s.ChatHandler, api.ChatRequest{Model: "win", Messages: []api.Message{{Role: "user", Content: "x"}}, Stream: &noStream}),
		"generate stream":   createRequest(t, s.GenerateHandler, api.GenerateRequest{Model: "win", Prompt: "x", Stream: &stream}),
		"generate no strea": createRequest(t, s.GenerateHandler, api.GenerateRequest{Model: "win", Prompt: "x", Stream: &noStream}),
	} {
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", name, w.Code, w.Body)
		}
		if got := w.Header().Get(llm.ContextWindowHeader); got != "16384" {
			t.Errorf("%s: X-Context-Window = %q, want 16384", name, got)
		}
	}
}

// Off means off: an engine that states no window leaves the response as
// upstream's.
func TestNoWindowNoHeader(t *testing.T) {
	s := windowServer(t, 0)
	w := createRequest(t, s.ChatHandler, api.ChatRequest{Model: "win", Messages: []api.Message{{Role: "user", Content: "x"}}})
	if _, ok := w.Header()[llm.ContextWindowHeader]; ok || w.Code != http.StatusOK {
		t.Fatalf("status %d, header %v", w.Code, w.Header())
	}
}

func TestTheWindowReachesOpenAIAndAnthropicClients(t *testing.T) {
	s := windowServer(t, 8192)
	r := gin.New()
	r.POST("/v1/chat/completions", middleware.ChatMiddleware(), s.ChatHandler)
	r.POST("/v1/completions", middleware.CompletionsMiddleware(), s.GenerateHandler)
	r.POST("/v1/messages", middleware.AnthropicMessagesMiddleware(), s.ChatHandler)
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"win","messages":[{"role":"user","content":"x"}]}`},
		{"/v1/chat/completions", `{"model":"win","stream":true,"messages":[{"role":"user","content":"x"}]}`},
		{"/v1/completions", `{"model":"win","prompt":"x"}`},
		{"/v1/messages", `{"model":"win","max_tokens":16,"messages":[{"role":"user","content":"x"}]}`},
		{"/v1/messages", `{"model":"win","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"x"}]}`},
	} {
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		w := NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d %s", c.path, c.body, w.Code, w.Body)
		}
		if got := w.Header().Get(llm.ContextWindowHeader); got != "8192" {
			t.Errorf("%s %s: X-Context-Window = %q, want 8192", c.path, c.body, got)
		}
	}
}

func TestTheFirstReportWins(t *testing.T) {
	ctx, w := llm.WithContextWindow(t.Context())
	llm.ReportContextWindow(ctx, 0)
	llm.ReportContextWindow(ctx, -5)
	llm.ReportContextWindow(ctx, 4096)
	llm.ReportContextWindow(ctx, 8192)
	llm.ReportContextWindow(t.Context(), 1) // no collector: nothing to do
	if w.Get() != 4096 {
		t.Fatalf("window = %d, want the first report", w.Get())
	}
}

func TestAWindowLearnedAfterTheFirstByteIsNotSent(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	exposeContextWindow(c)
	c.Writer.WriteString("early")
	llm.ReportContextWindow(c.Request.Context(), 2048)
	c.Writer.WriteString("late")
	if got := rec.Header().Get(llm.ContextWindowHeader); got != "" {
		t.Fatalf("header after the body started: %q", got)
	}
}

// A council turn runs inside its owner's window, and reports that one --
// not whichever member's admission came first.
func TestACouncilTurnReportsItsOwnersWindow(t *testing.T) {
	for _, grant := range []int{12288, 0} {
		councilCompactions.reset()
		councilRoots.reset()
		e := &councilEngine{route: `{"route":"council"}`}
		kv := &fakeKV{grant: grant, most: grant, used: 900, session: "conv-window"}
		s := polykvCouncil(t, e, kv, councilOn())
		w := createRequest(t, s.ChatHandler, longCouncilReq("conv-window", "Why is the sky blue?"))
		councilIdle.Wait()
		want := ""
		if grant > 0 {
			want = "12288"
		}
		if w.Code != http.StatusOK || w.Header().Get(llm.ContextWindowHeader) != want {
			t.Errorf("grant %d: status %d, X-Context-Window %q, want %q", grant, w.Code, w.Header().Get(llm.ContextWindowHeader), want)
		}
	}
}

// Every way the handler can start the response carries the header.
func TestEveryFirstWriteCarriesTheWindow(t *testing.T) {
	for name, write := range map[string]func(gin.ResponseWriter){
		"Write":          func(w gin.ResponseWriter) { w.Write([]byte("x")) },
		"WriteString":    func(w gin.ResponseWriter) { w.WriteString("x") },
		"WriteHeaderNow": func(w gin.ResponseWriter) { w.WriteHeader(http.StatusNoContent); w.WriteHeaderNow() },
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		exposeContextWindow(c)
		llm.ReportContextWindow(c.Request.Context(), 4096)
		write(c.Writer)
		if got := rec.Header().Get(llm.ContextWindowHeader); got != "4096" {
			t.Errorf("%s: X-Context-Window = %q, want 4096", name, got)
		}
	}
}

// A client that states its own window negotiates it: refused, it gets a 429
// at once with the largest window the engine would admit. A client that does
// not state one is not negotiating.
func TestANegotiatingClientGetsTheLargestAdmissible(t *testing.T) {
	mock := &mockRunner{}
	mock.CompletionFn = func(ctx context.Context, r llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
		if llm.Negotiating(ctx) {
			llm.ReportWindowRefusal(ctx, 512, 2*time.Second)
			return api.StatusError{StatusCode: http.StatusTooManyRequests, ErrorMessage: "cannot book 4096: 512 free"}
		}
		fn(llm.CompletionResponse{Done: true, DoneReason: llm.DoneReasonStop})
		return nil
	}
	gin.SetMode(gin.TestMode)
	s := newServerWithMockRunner(t, mock)
	createMinimalGGUFModel(t, s, "neg", nil, "{{ .Prompt }}", nil)
	msgs := []api.Message{{Role: "user", Content: "x"}}

	for name, stream := range map[string]bool{"stream": true, "no stream": false} {
		w := createRequest(t, s.ChatHandler, api.ChatRequest{Model: "neg", Messages: msgs, Stream: &stream, SessionID: "s1", Placement: &api.Placement{NumCtx: 4096, NumCtxMin: 1024}})
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: status %d %s, want 429", name, w.Code, w.Body)
		}
		if w.Header().Get(llm.LargestAdmissibleHeader) != "512" || w.Header().Get("Retry-After") != "2" {
			t.Fatalf("%s: headers %v", name, w.Header())
		}
		if !strings.Contains(w.Body.String(), "512 free") {
			t.Fatalf("%s: body %s", name, w.Body)
		}
	}
	neg := -1
	for name, p := range map[string]*api.Placement{"no placement": nil, "negative pool": {PoolID: &neg}, "min only": {NumCtxMin: 1024}} {
		w := createRequest(t, s.ChatHandler, api.ChatRequest{Model: "neg", Messages: msgs, Placement: p})
		if w.Code != http.StatusOK || w.Header().Get(llm.LargestAdmissibleHeader) != "" {
			t.Fatalf("%s: status %d headers %v", name, w.Code, w.Header())
		}
	}
	// A turn attached to a pool of the client's own drives the engine itself:
	// pool 0 is a real pool.
	w := createRequest(t, s.ChatHandler, api.ChatRequest{Model: "neg", Messages: msgs, SessionID: "s2", Placement: &api.Placement{PoolID: new(int)}})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("pool 0: status %d, want the refusal passed through", w.Code)
	}
}

// Retry-After is whole seconds, rounded up and never 0: a client told 0
// would retry in a loop.
func TestRetryAfterIsRoundedUpAndNeverZero(t *testing.T) {
	for wait, want := range map[time.Duration]string{0: "1", 1500 * time.Millisecond: "2", 3 * time.Second: "3"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		exposeContextWindow(c)
		llm.ReportWindowRefusal(c.Request.Context(), 256, wait)
		c.Writer.WriteString("x")
		if got := rec.Header().Get("Retry-After"); got != want {
			t.Errorf("wait %v: Retry-After %q, want %q", wait, got, want)
		}
	}
}

// A refusal that names no largest window -- "session allocation full" inside
// an owner's window -- carries Retry-After, and no X-Context-Largest-Admissible
// claiming 0.
func TestARefusalWithoutALargestWindowSaysNone(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	exposeContextWindow(c)
	llm.ReportWindowRefusal(c.Request.Context(), 0, 2*time.Second)
	c.Writer.WriteString("x")
	if _, ok := rec.Header()[llm.LargestAdmissibleHeader]; ok || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("headers %v", rec.Header())
	}
}
