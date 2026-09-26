package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	mu         sync.Mutex
	roles      []string
	sessions   []string
	prompts    []string
	placements []*llm.Placement
	budgets    []int // the think budget each call carried
	predicts   []int // and its num_predict
	// compaction overrides the compaction members' replies, by role.
	compaction map[string]string
	// gate, when set, holds the compaction's writer until it is closed.
	gate chan struct{}
	// hold keeps each compaction call in flight this long, so calls that
	// overlap on one session are seen doing so; overlaps names those sessions.
	hold     time.Duration
	inflight map[string]int
	overlaps []string
}

// compactionMarkers tell the compaction's members apart by their instruction.
var compactionMarkers = map[string]string{
	"Write the replay that takes its place":     "compaction-writer",
	"Everything above is about to be deleted":   "compaction-writer",
	"you own the **first half**":                "compaction-critic-1",
	"you own the **second half**":               "compaction-critic-2",
	"Join them back into one continuous replay": "compaction-synthesizer",
	"You are writing the retrospective":         "compaction-retrospective",
}

const (
	fakeReplayFirst  = "The user is asking me about Rayleigh scattering, and I am explaining it."
	fakeReplaySecond = "The user now asks whether it depends on wavelength; I am answering that it goes with the fourth power."
	fakeMerged       = "The user is asking me about Rayleigh scattering, and I am explaining it carefully. The user now asks whether it depends on wavelength; I am answering that it goes with the inverse fourth power."
)

var compactionReplies = map[string]string{
	"compaction-writer":        fakeReplayFirst + "\n\n" + compactionHalfway + "\n\n" + fakeReplaySecond,
	"compaction-critic-1":      "The user is asking me about Rayleigh scattering, and I am explaining it carefully.",
	"compaction-critic-2":      "The user now asks whether it depends on wavelength; I am answering that it goes with the inverse fourth power.",
	"compaction-synthesizer":   "## Replay\n\n" + fakeMerged,
	"compaction-retrospective": "## What worked\n- Answering from the law first.",
}

var councilMarkers = []string{`{"route":"direct"}`, "ROLE: PLANNER. The council", "ROLE: RESEARCHER", "ROLE: CRITIC", "ROLE: SYNTHESIZER"}

func (e *councilEngine) complete(ctx context.Context, r llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
	role, at := "chat", -1
	for i, m := range councilMarkers {
		if j := strings.LastIndex(r.Prompt, m); j > at {
			at, role = j, []string{"route", "planner", "researcher", "critic", "synthesizer"}[i]
		}
	}
	for m, cr := range compactionMarkers {
		if j := strings.LastIndex(r.Prompt, m); j > at {
			at, role = j, cr
		}
	}
	// The text path reads the folded turns as a transcript, not the
	// conversation; it runs on the owner, so its session does not tell.
	if strings.HasPrefix(role, "compaction-") && role != "compaction-retrospective" && strings.Contains(r.Prompt, "\n\nConversation:\n") {
		role = "compaction-text-" + strings.TrimPrefix(role, "compaction-")
	}
	if e.hold > 0 && strings.HasPrefix(role, "compaction-") {
		e.mu.Lock()
		if e.inflight == nil {
			e.inflight = map[string]int{}
		}
		if e.inflight[r.SessionID]++; e.inflight[r.SessionID] > 1 {
			e.overlaps = append(e.overlaps, r.SessionID+" "+role)
		}
		e.mu.Unlock()
		time.Sleep(e.hold)
		defer func() {
			e.mu.Lock()
			e.inflight[r.SessionID]--
			e.mu.Unlock()
		}()
	}
	e.mu.Lock()
	e.roles = append(e.roles, role)
	e.sessions = append(e.sessions, r.SessionID)
	e.prompts = append(e.prompts, r.Prompt)
	e.placements = append(e.placements, r.Placement)
	e.budgets = append(e.budgets, r.ThinkBudget)
	if r.Options != nil {
		e.predicts = append(e.predicts, r.Options.NumPredict)
	} else {
		e.predicts = append(e.predicts, 0)
	}
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
	if role == "compaction-writer" && e.gate != nil {
		select {
		case <-e.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if strings.HasPrefix(role, "compaction-") {
		reply = compactionReplies[strings.Replace(role, "compaction-text-", "compaction-", 1)]
		if v, ok := e.compaction[role]; ok {
			reply = v
		}
	}
	// A member given a budget reasons first, as a thinking model does -- once:
	// a structured reply is a second pass whose prompt already holds the
	// reasoning (ChatHandler's structured-outputs restart).
	if r.ThinkBudget > 0 && !strings.Contains(r.Prompt, memberReasoning) {
		reply = "<think>" + memberReasoning + "</think>" + reply
	}
	// Two pieces, so the stream is a stream.
	half := len(reply) / 2
	fn(llm.CompletionResponse{Content: reply[:half]})
	// A runner stops when its request is canceled, as ChatHandler does to
	// restart a thinking model under its format.
	if err := ctx.Err(); err != nil {
		return err
	}
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
	return councilServerWith(t, e, council, nil)
}

func councilServerWith(t *testing.T, e *councilEngine, council *xollama.Council, messages []api.Message) *Server {
	t.Helper()
	return councilServerOn(t, &councilRunner{mockRunner: &mockRunner{contextLength: 32768}, e: e}, council, messages)
}

const councilTemplate = `{{- if .Tools }}{{ .Tools }}{{ end }}{{- range .Messages }}<{{ .Role }}>{{ .Content }}
{{ end }}`

// councilThinkingTemplate is councilTemplate for a model that can think.
const councilThinkingTemplate = `{{- if .Tools }}{{ .Tools }}{{ end }}{{- range .Messages }}<{{ .Role }}>{{ if .Thinking }}<think>{{ .Thinking }}</think>{{ end }}{{ .Content }}
{{ end }}`

// memberReasoning is what a thinking member reasons; it must never reach the client.
const memberReasoning = "MEMBER-PRIVATE-REASONING"

func councilServerOn(t *testing.T, mock llm.LlamaServer, council *xollama.Council, messages []api.Message) *Server {
	t.Helper()
	return councilServerTemplate(t, mock, council, messages, councilTemplate)
}

func councilServerTemplate(t *testing.T, mock llm.LlamaServer, council *xollama.Council, messages []api.Message, template string) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
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
		Model:    "council",
		Files:    map[string]string{"test.gguf": digest},
		Template: template,
		Xollama:  cfg,
		Messages: messages,
		Stream:   &no,
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
		for _, seg := range th.add(council.Event{Role: council.Researcher, Index: i, Kind: council.Thinking, Text: text, Done: done}) {
			// A segment is one member's: its tag names the member whose
			// lines it holds.
			if want := fmt.Sprintf("Researcher %d", seg.tag.Index+1); seg.tag.Role != "researcher" || strings.Contains(seg.text, "### ") && !strings.Contains(seg.text, "### "+want+"\n") {
				t.Errorf("segment %q tagged %+v", seg.text, seg.tag)
			}
			if strings.Contains(seg.text, "a1") && seg.tag.Index != 0 || strings.Contains(seg.text, "b1") && seg.tag.Index != 1 {
				t.Errorf("segment %q tagged %+v", seg.text, seg.tag)
			}
			out.WriteString(seg.text)
		}
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

// A Modelfile's MESSAGE turns reach each member once: ChatHandler prepends
// them to every request, so the council must not fold them in as well.
func TestTheModelsOwnMessagesReachEachMemberOnce(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServerWith(t, e, councilOn(), []api.Message{
		{Role: "user", Content: "PRIMER-QUESTION"}, {Role: "assistant", Content: "PRIMER-ANSWER"},
	})
	chatChunks(t, s, api.ChatRequest{
		Model: "council", Options: map[string]any{"num_ctx": 8192},
		Messages: []api.Message{{Role: "user", Content: "Why?"}},
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, p := range e.prompts {
		if n := strings.Count(p, "PRIMER-QUESTION"); n != 1 {
			t.Errorf("%s prompt carries the model's MESSAGE %d times", e.roles[i], n)
		}
	}
}

func TestAThinkingRoleReasonsWithinItsBudgetAndHidesIt(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	c := councilOn()
	c.Planner = &xollama.CouncilRole{Think: "on"}
	c.Researcher = &xollama.CouncilRole{Think: "medium"}
	c.Synthesizer = &xollama.CouncilRole{Think: "2048"}
	s := councilServerTemplate(t, &councilRunner{mockRunner: &mockRunner{contextLength: 32768}, e: e}, c, nil, councilThinkingTemplate)
	thinking, content := joined(chatChunks(t, s, api.ChatRequest{
		Model:    "council",
		Options:  map[string]any{"num_ctx": 16384},
		Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
	}))
	if !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("answer %q", content)
	}
	if strings.Contains(thinking+content, memberReasoning) {
		t.Errorf("a member's own reasoning reached the client:\nthinking %q\ncontent %q", thinking, content)
	}
	if !strings.Contains(thinking, "Rayleigh scattering") {
		t.Errorf("the researchers' replies are missing from the deliberation: %q", thinking)
	}

	// on is 2048 tokens, medium a quarter of the 16k window; the budget comes
	// on top of the role's reply cap; the routing call and the critics never
	// reason.
	want := map[string][2]int{
		"route":       {0, 16},
		"planner":     {2048, 512 + 2048},
		"researcher":  {4096, 384 + 4096},
		"critic":      {0, 256},
		"synthesizer": {2048, 1024 + 2048},
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, role := range e.roles {
		if got := [2]int{e.budgets[i], e.predicts[i]}; got != want[role] {
			t.Errorf("%s: budget, num_predict = %v, want %v", role, got, want[role])
		}
	}
}

// Every thinking chunk names the one member it holds; the answer names none
// (council_tags_v1).
func TestEveryThinkingChunkNamesItsMember(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	chunks := chatChunks(t, s, api.ChatRequest{
		Model:    "council",
		Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
	})
	seen := map[api.CouncilTag]bool{}
	for _, c := range chunks {
		switch {
		case c.Message.Content != "" && c.Council != nil:
			t.Errorf("an answer chunk is tagged %+v", *c.Council)
		case c.Message.Thinking != "" && c.Council == nil:
			t.Errorf("a thinking chunk is untagged: %q", c.Message.Thinking)
		case c.Message.Thinking != "":
			seen[*c.Council] = true
			heading := strings.ToUpper(c.Council.Role[:1]) + c.Council.Role[1:]
			if c.Council.Role == "researcher" || c.Council.Role == "critic" {
				heading = fmt.Sprintf("%s %d", heading, c.Council.Index+1)
			}
			if strings.Contains(c.Message.Thinking, "### ") && !strings.Contains(c.Message.Thinking, "### "+heading+"\n") {
				t.Errorf("chunk tagged %+v holds %q", *c.Council, c.Message.Thinking)
			}
		case c.Done && c.Council != nil:
			t.Errorf("the done chunk is tagged %+v", *c.Council)
		}
	}
	for _, want := range []api.CouncilTag{{Role: "planner"}, {Role: "researcher"}, {Role: "researcher", Index: 1}, {Role: "critic"}, {Role: "critic", Index: 1}} {
		if !seen[want] {
			t.Errorf("no thinking tagged %+v; saw %v", want, seen)
		}
	}
}

// /api/xollama names the features this build serves.
func TestTheIdentityNamesTheFeatures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, api.XollamaIdentityPath, nil)
	XollamaIdentityHandler(c)
	var id api.XollamaIdentity
	if err := json.Unmarshal(w.Body.Bytes(), &id); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"council", "council_compaction_v1", "council_tags_v1"} {
		if !slices.Contains(id.Features, f) {
			t.Errorf("features %v lack %q", id.Features, f)
		}
	}
}
