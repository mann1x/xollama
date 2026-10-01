package server

import (
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
)

// The members read past turns without their thinking, which is the council's
// own deliberation when a generic client sends it back.
func TestMembersReadNoPastThinking(t *testing.T) {
	conv := []api.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a", Thinking: "### Researcher 1 …"}}
	got := councilMembersView(conv)
	if got[1].Thinking != "" || got[1].Content != "a" || conv[1].Thinking == "" {
		t.Errorf("view %+v, conversation %+v", got, conv)
	}
}

// The held resume points are bounded: the oldest goes when the store is full.
func TestHeldStatesAreBounded(t *testing.T) {
	h := &councilHeldStates{m: map[string]heldState{}}
	for i := range councilHeldMax + 1 {
		h.put(string(rune('a'+i%26))+string(rune('0'+i/26)), "blob")
	}
	if len(h.m) != councilHeldMax || h.get("a0") != "" {
		t.Errorf("%d held, the first kept %v", len(h.m), h.get("a0") != "")
	}
}

// The done chunk reports the conversation as the compactor measured it, then
// the front's or planner's prompt, and the members' sum only with neither:
// a synthesizer's prompt holds the plan and the findings too (229k on 0418).
func TestTheReportedPromptIsTheConversations(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		sum, convPrompt, toks int
		want                  int
	}{
		{"measured", 500, 40, 30, 30},
		{"a front or planner call", 500, 40, 0, 40},
		{"neither", 500, 0, 0, 500},
	} {
		cm := &councilMembers{convPrompt: tc.convPrompt, convTokens: tc.toks}
		cm.m.PromptEvalCount = tc.sum
		if got := cm.metrics(0).PromptEvalCount; got != tc.want {
			t.Errorf("%s: prompt_eval_count %d, want %d", tc.name, got, tc.want)
		}
	}
	if carriesConversation(council.Synthesizer) || !carriesConversation(council.Front) || !carriesConversation(council.Planner) {
		t.Error("only the front's and the planner's prompts are the conversation's")
	}
}

// The reported size is the whole request the client sent, its tool round
// trips included, however short the history before them: a generic harness's
// one-message task left compact nothing to measure, and a research round
// reported the members' sum (native.sh 0426: 393k).
func TestTheSentConversationIsMeasuredWhole(t *testing.T) {
	c := &councilCompactor{
		numCtx: 131072, compactAt: 0.85,
		render:   func(_ context.Context, ms []api.Message) (string, error) { return strings.Repeat("x", 4*len(ms)), nil },
		tokenize: func(_ context.Context, s string) ([]int, error) { return make([]int, len(s)), nil },
	}
	sent := []api.Message{
		{Role: "system"},
		{Role: "user", Content: "fix it"},
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "f:a", Function: api.ToolCallFunction{Name: "run_commands"}}}},
		{Role: "tool", ToolCallID: "f:a", Content: "Error"},
	}
	if got := c.sentTokens(t.Context(), sent); got != 16 {
		t.Errorf("measured %d tokens, want 16: the tool round trip is part of what was sent", got)
	}
	var none *councilCompactor
	if got := none.sentTokens(t.Context(), sent); got != 0 {
		t.Errorf("no compactor measured %d", got)
	}
}
