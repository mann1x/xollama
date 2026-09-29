package server

import (
	"context"
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
	// Nothing to count with: half the characters, the reply cap and a margin.
	p := cm.ownWindow(t.Context(), r)
	if p == nil || p.NumCtx != 3072 || p.NumCtxMin != 3072 {
		t.Fatalf("placement %+v, want 3072", p)
	}
	r.Messages = []api.Message{{Content: strings.Repeat("x", 90000)}}
	if p := cm.ownWindow(t.Context(), r); p.NumCtx != 8192 {
		t.Errorf("a long review took %d, want the member window", p.NumCtx)
	}
	if cm.ownWindow(t.Context(), council.Request{Role: council.Critic}) != nil {
		t.Error("a critic took a reviewer's window")
	}
	// place states it, with no tree to place the reviewer on.
	var req api.ChatRequest
	if p, _, done := cm.place(t.Context(), council.Request{Role: council.Reviewer, MaxTokens: 1024}, &req); p == nil || p.NumCtx == 0 {
		t.Error("place gave a reviewer no window")
	} else {
		done()
	}
	if (&councilMembers{}).ownWindow(t.Context(), r) != nil {
		t.Error("a reviewer stated a window where there is no pool")
	}
}

// A reviewer's window holds its request as the engine counts it. On eleven2go
// (hard, 5ce5f7e7) a third of the characters sized a critic's review at 12800,
// and the engine refused its 13196 tokens.
func TestAReviewersWindowHoldsItsRequestInTokens(t *testing.T) {
	msgs := []api.Message{{Content: strings.Repeat("c.fill();}});", 2000)}} // 26000 characters
	r := council.Request{Role: council.Reviewer, MaxTokens: 1024, Messages: msgs}
	// Dense text: more tokens than half its characters.
	const counted = 15000
	cm := &councilMembers{reviewWindow: 196608, count: func(context.Context, []api.Message) (int, error) { return counted, nil }}
	p := cm.ownWindow(t.Context(), r)
	if p == nil || p.NumCtx < counted+1024 {
		t.Fatalf("placement %+v: the %d-token review and its reply do not fit", p, counted)
	}
	if old := roundUp(26000/3+1024+512, 256); old >= 13196 {
		t.Fatalf("the case no longer shows the old estimate's shortfall (%d)", old)
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

// A review streams under its reviewer's heading.
func TestAReviewHasItsReviewersHeading(t *testing.T) {
	if got := memberName(council.Event{Role: council.Reviewer, Index: 1, Round: 2}); got != "Reviewer 2 (round 3)" {
		t.Errorf("heading %q", got)
	}
}
