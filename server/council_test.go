package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/internal/council"
	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// councilEngine answers each member by the role its prompt ends on, the way
// the real model is asked: the last "ROLE:" line is the member's own.
type councilEngine struct {
	route string

	mu       sync.Mutex
	roles    []string
	sessions []string
	prompts  []string
}

var councilMarkers = []string{`{"route":"direct"}`, "ROLE: PLANNER. The council", "ROLE: RESEARCHER", "ROLE: CRITIC", "ROLE: SYNTHESIZER"}

func (e *councilEngine) complete(_ context.Context, r llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
	role, at := "chat", -1
	for i, m := range councilMarkers {
		if j := strings.LastIndex(r.Prompt, m); j > at {
			at, role = j, []string{"route", "planner", "researcher", "critic", "synthesizer"}[i]
		}
	}
	e.mu.Lock()
	e.roles = append(e.roles, role)
	e.sessions = append(e.sessions, r.SessionID)
	e.prompts = append(e.prompts, r.Prompt)
	e.mu.Unlock()

	reply := map[string]string{
		"route":       e.route,
		"planner":     `{"plan":"look it up","briefs":["physics","history"]}`,
		"researcher":  "Rayleigh scattering.\nShorter wavelengths scatter more.\n",
		"critic":      "Keep both findings.",
		"synthesizer": "The sky is blue because air scatters blue light most.",
		"chat":        "Hello there!",
	}[role]
	if role == "route" && e.route == `{"route":"direct"}` {
		reply = e.route
	}
	// Two pieces, so the stream is a stream.
	half := len(reply) / 2
	fn(llm.CompletionResponse{Content: reply[:half]})
	fn(llm.CompletionResponse{Content: reply[half:], Done: true, DoneReason: llm.DoneReasonStop, PromptEvalCount: 10, EvalCount: 5})
	return nil
}

func (e *councilEngine) count(role string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, r := range e.roles {
		if r == role {
			n++
		}
	}
	return n
}

// councilRunner serves members concurrently. mockRunner records the last
// request in a field, which parallel members would race on.
type councilRunner struct {
	*mockRunner
	e *councilEngine
}

func (r *councilRunner) Completion(ctx context.Context, req llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
	return r.e.complete(ctx, req, fn)
}

func councilServer(t *testing.T, e *councilEngine, council *xollama.Council) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mock := &councilRunner{mockRunner: &mockRunner{}, e: e}
	s := &Server{sched: &Scheduler{
		pendingReqCh:  make(chan *LlmRequest, 8),
		finishedReqCh: make(chan *LlmRequest, 8),
		expiredCh:     make(chan *runnerRef, 8),
		unloadedCh:    make(chan any, 8),
		loaded:        make(map[string]*runnerRef),
		newServerFn: func(ml.SystemInfo, []ml.DeviceInfo, string, *gguf.Model, []string, []string, api.Options, int, llm.LlamaServerConfig) (llm.LlamaServer, error) {
			return mock, nil
		},
		getGpuFn:        getGpuFn,
		getSystemInfoFn: getSystemInfoFn,
		waitForRecovery: 100 * time.Millisecond,
		loadFn: func(req *LlmRequest, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) bool {
			req.successCh <- &runnerRef{llama: mock}
			return false
		},
	}}
	go s.sched.Run(t.Context())

	_, digest := createBinFile(t, gguftest.KV{
		"general.architecture":          "llama",
		"llama.context_length":          uint32(4096),
		"llama.embedding_length":        uint32(4096),
		"llama.block_count":             uint32(1),
		"llama.attention.head_count":    uint32(32),
		"llama.attention.head_count_kv": uint32(32),
		"tokenizer.ggml.tokens":         []string{" "},
		"tokenizer.ggml.scores":         []float32{0},
		"tokenizer.ggml.token_type":     []int32{0},
	}, []*gguftest.Tensor{
		{Name: "blk.0.attn.weight", Type: gguf.TensorTypeF32, Offset: uint64(0), Shape: []uint64{1, 1, 1, 1}, WriterTo: bytes.NewReader(make([]byte, 4))},
		{Name: "output.weight", Type: gguf.TensorTypeF32, Offset: uint64(0), Shape: []uint64{1, 1, 1, 1}, WriterTo: bytes.NewReader(make([]byte, 4))},
	})
	no := false
	var cfg *xollama.Config
	if council != nil {
		cfg = &xollama.Config{Version: xollama.SchemaVersion, Council: council}
	}
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model: "council",
		Files: map[string]string{"test.gguf": digest},
		Template: `{{- if .Tools }}{{ .Tools }}{{ end }}{{- range .Messages }}<{{ .Role }}>{{ .Content }}
{{ end }}`,
		Xollama: cfg,
		Stream:  &no,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	return s
}

func councilOn() *xollama.Council { yes := true; return &xollama.Council{Enabled: &yes} }

func chatChunks(t *testing.T, s *Server, req api.ChatRequest) []api.ChatResponse {
	t.Helper()
	w := createRequest(t, s.ChatHandler, req)
	if w.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", w.Code, w.Body.String())
	}
	var out []api.ChatResponse
	dec := json.NewDecoder(w.Body)
	for dec.More() {
		var c api.ChatResponse
		if err := dec.Decode(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func joined(chunks []api.ChatResponse) (thinking, content string) {
	var th, co strings.Builder
	for _, c := range chunks {
		th.WriteString(c.Message.Thinking)
		co.WriteString(c.Message.Content)
	}
	return th.String(), co.String()
}

func TestACouncilModelAnswersWithItsMembers(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	chunks := chatChunks(t, s, api.ChatRequest{
		Model:    "council",
		Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
	})

	thinking, content := joined(chunks)
	if content != "The sky is blue because air scatters blue light most." {
		t.Errorf("content %q", content)
	}
	for _, want := range []string{"### Planner", "### Researcher 1", "### Researcher 2", "### Critic 1", "### Critic 2", "Rayleigh scattering."} {
		if !strings.Contains(thinking, want) {
			t.Errorf("thinking lacks %q:\n%s", want, thinking)
		}
	}
	last := chunks[len(chunks)-1]
	if !last.Done || last.DoneReason != "stop" || last.EvalCount != 5*7 {
		t.Errorf("final chunk %+v, want done/stop and the members' 35 eval tokens", last)
	}
	for role, n := range map[string]int{"route": 1, "planner": 1, "researcher": 2, "critic": 2, "synthesizer": 1} {
		if got := e.count(role); got != n {
			t.Errorf("%s calls = %d, want %d", role, got, n)
		}
	}
}

func TestEveryParallelMemberHasItsOwnSession(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	chatChunks(t, s, api.ChatRequest{Model: "council", Messages: []api.Message{{Role: "user", Content: "Why?"}}})

	e.mu.Lock()
	defer e.mu.Unlock()
	byRole := map[string][]string{}
	for i, r := range e.roles {
		byRole[r] = append(byRole[r], e.sessions[i])
	}
	root := byRole["route"][0]
	if root == "" || byRole["planner"][0] != root {
		t.Fatalf("the planner's calls must share the conversation's session: %v", byRole)
	}
	seen := map[string]bool{root: true}
	for _, role := range []string{"researcher", "critic", "synthesizer"} {
		for _, id := range byRole[role] {
			if seen[id] || !strings.HasPrefix(id, root+"~") {
				t.Errorf("%s session %q shared or not under the conversation's %q", role, id, root)
			}
			seen[id] = true
		}
	}
}

func TestATrivialMessageIsAnsweredDirectly(t *testing.T) {
	e := &councilEngine{route: `{"route":"direct"}`}
	s := councilServer(t, e, councilOn())
	thinking, content := joined(chatChunks(t, s, api.ChatRequest{
		Model: "council", Messages: []api.Message{{Role: "user", Content: "Hello!"}},
	}))
	if thinking != "" || content != "Hello there!" || len(e.roles) != 2 {
		t.Errorf("direct turn: %d calls, thinking %q, content %q", len(e.roles), thinking, content)
	}
}

func TestThinkingOffHidesTheDeliberation(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	off := false
	thinking, content := joined(chatChunks(t, s, api.ChatRequest{
		Model: "council", Think: &api.ThinkValue{Value: off},
		Messages: []api.Message{{Role: "user", Content: "Why?"}},
	}))
	if thinking != "" || !strings.HasPrefix(content, "The sky is blue") {
		t.Errorf("thinking %q content %q", thinking, content)
	}
}

func TestANonStreamedCouncilTurnIsOneResponse(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	no := false
	chunks := chatChunks(t, s, api.ChatRequest{
		Model: "council", Stream: &no, Messages: []api.Message{{Role: "user", Content: "Why?"}},
	})
	if len(chunks) != 1 || !chunks[0].Done || !strings.Contains(chunks[0].Message.Thinking, "### Critic 2") ||
		chunks[0].Message.Content != "The sky is blue because air scatters blue light most." {
		t.Errorf("non-streamed turn: %+v", chunks)
	}
}

// Tools and a format are the client steering the model's own output; the
// model answers them as an ordinary chat.
func TestToolsAndFormatBypassTheCouncil(t *testing.T) {
	for _, req := range []api.ChatRequest{
		{Format: json.RawMessage(`"json"`)},
		{Tools: getTestTools()},
	} {
		e := &councilEngine{route: `{"route":"council"}`}
		s := councilServer(t, e, councilOn())
		req.Model = "council"
		req.Messages = []api.Message{{Role: "user", Content: "Why?"}}
		chatChunks(t, s, req)
		if !slices.Equal(e.roles, []string{"chat"}) {
			t.Errorf("format %s tools %d: calls %v, want one plain turn", req.Format, len(req.Tools), e.roles)
		}
	}
}

func TestAModelWithoutACouncilIsUntouched(t *testing.T) {
	no := false
	for _, c := range []*xollama.Council{nil, {Enabled: &no}} {
		e := &councilEngine{}
		s := councilServer(t, e, c)
		_, content := joined(chatChunks(t, s, api.ChatRequest{
			Model: "council", Messages: []api.Message{{Role: "user", Content: "Hello"}},
		}))
		if content != "Hello there!" || len(e.roles) != 1 || strings.Contains(e.prompts[0], "council of assistants") {
			t.Errorf("council %+v: %d calls, content %q", c, len(e.roles), content)
		}
	}
}

func TestTheCharterFollowsTheModelsSystemPrompt(t *testing.T) {
	m := &Model{System: "You are terse."}
	conv := councilConversation("CHARTER", m, []api.Message{{Role: "user", Content: "hi"}})
	if len(conv) != 2 || conv[0].Content != "You are terse.\n\nCHARTER" {
		t.Errorf("model system: %+v", conv)
	}
	conv = councilConversation("CHARTER", m, []api.Message{{Role: "system", Content: "Client rules."}, {Role: "user", Content: "hi"}})
	if conv[0].Content != "Client rules.\n\nCHARTER" || conv[1].Content != "hi" {
		t.Errorf("client system: %+v", conv)
	}
}

// Parallel members interleave token by token; the thinking must still read as
// one member at a time, each under a single heading.
func TestParallelMembersReadOneAtATime(t *testing.T) {
	th := newThinkingTags()
	var out strings.Builder
	ev := func(i int, text string, done bool) {
		out.WriteString(th.add(council.Event{Role: council.Researcher, Index: i, Kind: council.Thinking, Text: text, Done: done}))
	}
	ev(0, "a1\n", false)
	ev(1, "b1\n", false)
	ev(0, "a2\n", false)
	ev(1, "b2\n", false)
	ev(1, "", true)
	ev(0, "a3", false)
	ev(0, "", true)
	want := "### Researcher 1\na1\na2\na3\n\n### Researcher 2\nb1\nb2\n"
	if out.String() != want {
		t.Errorf("thinking:\n%q\nwant\n%q", out.String(), want)
	}
}
