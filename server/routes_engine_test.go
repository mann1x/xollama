package server

import (
	"context"
	"encoding/json"
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
	engine   string
	status   int
	body     string
	err      error
	gotPaths []string
}

func (s *introspectableLlm) EngineName() string { return s.engine }

func (s *introspectableLlm) EngineGet(ctx context.Context, endpoint string) (int, []byte, error) {
	// The real implementation validates first; mirroring that here keeps the
	// whitelist test honest about where the refusal happens.
	endpoint, err := llm.ValidIntrospectEndpoint(endpoint)
	if err != nil {
		return 0, nil, err
	}
	s.gotPaths = append(s.gotPaths, endpoint)
	if s.err != nil {
		return 0, nil, s.err
	}
	return s.status, []byte(s.body), nil
}

func engineTestServer(t *testing.T, runners map[string]*runnerRef) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	s := &Server{sched: &Scheduler{loaded: runners}}
	r := gin.New()
	r.GET("/api/engine", s.EngineHandler)
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

// TestEngineReadRefusesToDriveTheEngine is the security guard, and the reason
// the path is a whitelisted name rather than something the caller composes.
// llama-server trusts its caller completely: its surface includes POST
// /completion and, on some builds, slot save and restore to arbitrary files.
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
