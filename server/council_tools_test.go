package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// councilToolTemplate renders tools and a member's calls as the model emits
// them, so upstream's parser reads a call back.
const councilToolTemplate = `{{- if .Tools }}{{ .Tools }}{{ end }}{{- range .Messages }}<{{ .Role }}>{{ .Content }}
{{- range .ToolCalls }}{"name": "{{ .Function.Name }}", "arguments": {{ .Function.Arguments }}}{{ end }}
{{ end }}`

func councilToolServer(t *testing.T, e *councilEngine) *Server {
	t.Helper()
	return councilServerTemplate(t, &councilRunner{mockRunner: &mockRunner{contextLength: 32768}, e: e}, councilOn(), nil, councilToolTemplate)
}

var councilTestTools = func() api.Tools {
	var tools api.Tools
	for _, body := range []string{
		`{"type":"function","function":{"name":"read_files","description":"Read files.","x_read_only":true,"parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		`{"type":"function","function":{"name":"write_file","description":"Write a file.","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
	} {
		var tool api.Tool
		if err := json.Unmarshal([]byte(body), &tool); err != nil {
			panic(err)
		}
		tools = append(tools, tool)
	}
	return tools
}()

// A council turn with tools, for a client that carries the state: both
// researchers read, the turn ends with both calls in one message and the
// state, and the results bring it back where it stopped -- the route and the
// plan not asked again, the calls and results out of the conversation the
// critics and the synthesizer read.
func TestACouncilTurnCallsToolsAndResumes(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	e := &councilEngine{route: `{"route":"council"}`, tools: map[string]string{"researcher": "read_files"}}
	s := councilToolServer(t, e)
	empty := ""
	q := api.Message{Role: "user", Content: "Why is the sky blue?"}
	req := api.ChatRequest{Model: "council", Tools: councilTestTools, CouncilChatState: &empty, Messages: []api.Message{q}}
	chunks := toolChat(t, s, req)
	var calls []api.ToolCall
	var content string
	for _, c := range chunks {
		calls = append(calls, c.Message.ToolCalls...)
		content += c.Message.Content
	}
	done := chunks[len(chunks)-1]
	if len(calls) != 2 || !strings.HasPrefix(calls[0].ID, "r1:call_") || !strings.HasPrefix(calls[1].ID, "r2:call_") || calls[0].Function.Name != "read_files" {
		t.Fatalf("calls %+v", calls)
	}
	if content != "" || !done.Done || done.DoneReason != "stop" || done.CouncilChatState == "" {
		t.Fatalf("content %q, done %v %q, state %v", content, done.Done, done.DoneReason, done.CouncilChatState != "")
	}
	if e.count("critic") != 0 || e.count("synthesizer") != 0 {
		t.Fatalf("the turn went on: critics %d synthesizer %d", e.count("critic"), e.count("synthesizer"))
	}
	for i, p := range e.prompts {
		if !strings.Contains(p, `"name":"read_files"`) || strings.Contains(p, "x_read_only") {
			t.Fatalf("%s's prompt does not start from the tools as rendered: %q", e.roles[i], p[:min(len(p), 200)])
		}
	}

	e2 := &councilEngine{route: `{"route":"council"}`, tools: e.tools}
	s2 := councilToolServer(t, e2)
	req.CouncilChatState = &done.CouncilChatState
	req.Messages = []api.Message{
		q,
		{Role: "assistant", ToolCalls: calls},
		{Role: "tool", ToolCallID: calls[0].ID, Content: "R1-RESULT"},
		{Role: "tool", Content: "R2-RESULT"}, // no id: the next open call's
	}
	_, content = joined(toolChat(t, s2, req))
	if !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("resumed answer %q", content)
	}
	if e2.count("route") != 0 || e2.count("planner") != 0 || e2.count("researcher") != 2 {
		t.Fatalf("resumed: route %d planner %d researchers %d", e2.count("route"), e2.count("planner"), e2.count("researcher"))
	}
	var sawR1, sawR2 bool
	for i, p := range e2.prompts {
		switch e2.roles[i] {
		case "researcher":
			sawR1 = sawR1 || strings.Contains(p, "<tool>R1-RESULT")
			sawR2 = sawR2 || strings.Contains(p, "<tool>R2-RESULT")
		case "critic", "synthesizer":
			// The researchers' results as their evidence, never their traffic.
			if !strings.Contains(p, "returned:\nR1-RESULT") || strings.Contains(p, "<tool>") || strings.Contains(p, "r1:call") {
				t.Errorf("%s reads %q", e2.roles[i], p)
			}
		}
	}
	if !sawR1 || !sawR2 {
		t.Fatalf("results reached the researchers: r1 %v r2 %v", sawR1, sawR2)
	}
}

// A researcher's write is refused in place; the synthesizer's goes out.
func TestACouncilSynthesizerWritesThroughTheClient(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	e := &councilEngine{route: `{"route":"council"}`, tools: map[string]string{"researcher": "write_file", "synthesizer": "write_file"}}
	s := councilToolServer(t, e)
	empty := ""
	var calls []api.ToolCall
	for _, c := range toolChat(t, s, api.ChatRequest{Model: "council", Tools: councilTestTools, CouncilChatState: &empty, Messages: []api.Message{{Role: "user", Content: "Fix the notes."}}}) {
		calls = append(calls, c.Message.ToolCalls...)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0].ID, "s:call_") || calls[0].Function.Name != "write_file" {
		t.Fatalf("calls %+v", calls)
	}
	refused := 0
	for i, p := range e.prompts {
		if e.roles[i] == "researcher" && strings.Contains(p, "<tool>Refused: write_file can change things") {
			refused++
		}
	}
	if refused != 2 {
		t.Fatalf("%d researchers read their refusal", refused)
	}
}

// Tools from a client that carries no state stay a plain chat, as before 9.5.
func TestToolsWithoutStateStayAPlainChat(t *testing.T) {
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilToolServer(t, e)
	toolChat(t, s, api.ChatRequest{Model: "council", Tools: councilTestTools, Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}}})
	if e.count("route") != 0 || e.count("chat") != 1 {
		t.Fatalf("roles %v", e.roles)
	}
}

func TestAResumedTurnsToolTrafficLeavesTheConversation(t *testing.T) {
	conv := []api.Message{
		{Role: "system"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "r1:a"}, {ID: "r2:b"}}},
		{Role: "tool", Content: "B", ToolCallID: "r2:b"},
		{Role: "tool", Content: "A"},
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "s:c"}}},
		{Role: "tool", Content: "C"},
	}
	got, results := councilToolTurn(conv)
	if len(got) != 4 || got[3].Content != "q2" {
		t.Fatalf("conversation %+v", got)
	}
	if results["r1:a"] != "A" || results["r2:b"] != "B" || results["s:c"] != "C" || len(results) != 3 {
		t.Fatalf("results %v", results)
	}
	if got, results := councilToolTurn(conv[:4]); len(got) != 4 || results != nil {
		t.Fatalf("a turn with no traffic: %d, %v", len(got), results)
	}
}

// toolChat sends req as a client does: ReadOnly is never marshalled, so each
// read-only tool's mark goes on the wire as x_read_only.
func toolChat(t *testing.T, s *Server, req api.ChatRequest) []api.ChatResponse {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	for i, tool := range req.Tools {
		if tool.Function.ReadOnly {
			body["tools"].([]any)[i].(map[string]any)["function"].(map[string]any)["x_read_only"] = true
		}
	}
	w := createRequest(t, s.ChatHandler, body)
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

// On PolyKV the conversation's root is rendered with the turn's tools, as
// every member's prompt is: the root stays their shared prefix and every layer
// still forks it.
func TestAToolTurnsRootHoldsTheTools(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{unowned: true}
	s := polykvCouncil(t, e, kv, councilOn())
	empty := ""
	req := polykvReq
	req.Tools, req.CouncilChatState = councilTestTools, &empty
	_, content := joined(toolChat(t, s, req))
	if !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("answer %q", content)
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if len(kv.pools) != 4 || !strings.Contains(kv.pools[0].text, `"name":"read_files"`) {
		t.Fatalf("pools %+v: want a root holding the tools and three layers on it", kv.pools)
	}
	for i, p := range e.prompts {
		if !strings.HasPrefix(p, kv.pools[0].text) {
			t.Errorf("%s does not start with the root", e.roles[i])
		}
	}
}

// A resumed member attaches to its step's shared layer, as on its first call:
// never to a pool of its own holding its instruction and its tool traffic.
func TestAResumedMemberReattachesToItsStage(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`, tools: map[string]string{"researcher": "read_files"}}
	kv := &fakeKV{unowned: true}
	s := polykvCouncil(t, e, kv, councilOn())
	empty := ""
	req := polykvReq
	req.Tools, req.CouncilChatState = councilTestTools, &empty
	chunks := toolChat(t, s, req)
	var calls []api.ToolCall
	for _, c := range chunks {
		calls = append(calls, c.Message.ToolCalls...)
	}
	state := chunks[len(chunks)-1].CouncilChatState
	req.CouncilChatState = &state
	req.Messages = append(slices.Clone(req.Messages), api.Message{Role: "assistant", ToolCalls: calls},
		api.Message{Role: "tool", ToolCallID: calls[0].ID, Content: "R1-RESULT"},
		api.Message{Role: "tool", ToolCallID: calls[1].ID, Content: "R2-RESULT"})
	if _, content := joined(toolChat(t, s, req)); !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("answer %q", content)
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	for _, p := range kv.pools {
		if strings.Contains(p.text, "<tool>") || strings.Contains(p.text, "ROLE: RESEARCHER") {
			t.Errorf("pool %d holds a member's own part: %q", p.id, p.text[max(0, len(p.text)-160):])
		}
	}
	for i, r := range e.roles {
		if r == "researcher" && (e.placements[i] == nil || e.placements[i].PoolID == nil) {
			t.Errorf("researcher call %d ran unpooled: %+v", i, e.placements[i])
		}
	}
}
