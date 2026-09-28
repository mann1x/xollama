package server

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
)

// A background reviewer on opencoti states a window sized to its request:
// without one it would book the whole session pool.
func TestAReviewerStatesAWindowOfItsOwn(t *testing.T) {
	cm := &councilMembers{reviewWindow: 8192}
	r := council.Request{Role: council.Reviewer, MaxTokens: 1024, Messages: []api.Message{{Content: strings.Repeat("x", 3000)}}}
	p := cm.ownWindow(r)
	if p == nil || p.NumCtx != 2560 || p.NumCtxMin != 2560 {
		t.Fatalf("placement %+v, want 2560", p)
	}
	r.Messages = []api.Message{{Content: strings.Repeat("x", 90000)}}
	if p := cm.ownWindow(r); p.NumCtx != 8192 {
		t.Errorf("a long review took %d, want the member window", p.NumCtx)
	}
	if cm.ownWindow(council.Request{Role: council.Critic}) != nil {
		t.Error("a critic took a reviewer's window")
	}
	// place states it, with no tree to place the reviewer on.
	var req api.ChatRequest
	if p, _, done := cm.place(t.Context(), council.Request{Role: council.Reviewer, MaxTokens: 1024}, &req); p == nil || p.NumCtx == 0 {
		t.Error("place gave a reviewer no window")
	} else {
		done()
	}
	if (&councilMembers{}).ownWindow(r) != nil {
		t.Error("a reviewer stated a window where there is no pool")
	}
}

// A tool turn on a council with critics offers the synthesizer council_review
// and keeps the conversation's review desk for the trips after it.
func TestAToolTurnKeepsAReviewDesk(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilToolServer(t, e)
	empty := ""
	req := api.ChatRequest{
		Model: "council", Tools: councilTestTools, CouncilChatState: &empty, SessionID: "conv-review",
		Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
	}
	toolChat(t, s, req)
	defer councilDesks.close("conv-review")
	councilDesks.mu.Lock()
	d := councilDesks.m["conv-review"]
	councilDesks.mu.Unlock()
	if d == nil {
		t.Fatal("no review desk for the conversation")
	}
	e.mu.Lock()
	offered := len(e.prompts) > 0 && strings.Contains(e.prompts[len(e.prompts)-1], `"name":"council_review"`)
	e.mu.Unlock()
	if !offered {
		t.Error("the members were not offered council_review")
	}
	toolChat(t, s, req)
	councilDesks.mu.Lock()
	again := councilDesks.m["conv-review"]
	councilDesks.mu.Unlock()
	if again != d {
		t.Error("the next trip made a new desk")
	}
	councilDesks.close("conv-review")
	councilDesks.mu.Lock()
	_, left := councilDesks.m["conv-review"]
	councilDesks.mu.Unlock()
	if left {
		t.Error("a closed desk stayed")
	}
}
