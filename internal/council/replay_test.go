package council

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// thinkStub reasons before each read; a Condenser call answers with a note.
type thinkStub struct {
	stub
	think func(n int) string
	n     int
	reqs  []Request
}

func (s *thinkStub) StreamTools(_ context.Context, req Request, _ func(string)) (Reply, error) {
	s.reqs = append(s.reqs, req)
	if req.Role == Condenser {
		return Reply{Content: "I settled that the brace on line 2 is open."}, nil
	}
	s.n++
	if s.n > 3 {
		return Reply{Content: "done"}, nil
	}
	return Reply{Thinking: s.think(s.n), Calls: []api.ToolCall{tcall("read_files", map[string]any{"path": fmt.Sprint(s.n)})}}, nil
}

// thoughts are the reasoning each request carried on the member's steps.
func thoughts(req Request) []string {
	var out []string
	for _, m := range req.Messages {
		if m.Role == "assistant" && m.Thinking != "" {
			out = append(out, m.Thinking)
		}
	}
	return out
}

// A member's next step carries the reasoning of its last step, and only that.
func TestAMembersLastReasoningIsReplayed(t *testing.T) {
	s := &thinkStub{think: func(n int) string { return fmt.Sprintf("thought %d", n) }}
	cfg := toolCfg()
	cfg.Results = map[string]string{}
	resume(t, s, cfg, Request{Role: Researcher, Messages: []api.Message{{Role: "user", Content: "go"}}}, "data")
	if got := thoughts(s.reqs[1]); len(got) != 1 || got[0] != "thought 1" {
		t.Fatalf("step 2 carried %q", got)
	}
	if got := thoughts(s.reqs[2]); len(got) != 1 || got[0] != "thought 2" {
		t.Fatalf("step 3 carried %q", got)
	}
}

// Reasoning that ended on the budget message is replaced by a note once its
// results are in; reasoning that did not is replayed as it was.
func TestCappedReasoningIsCondensed(t *testing.T) {
	const msg = "\nI have used my thinking budget. I must stop analysing now and act.\n"
	s := &thinkStub{think: func(n int) string {
		if n == 1 {
			return strings.Repeat("wait, again. ", 50) + msg
		}
		return "a short thought"
	}}
	cfg := toolCfg()
	cfg.Results = map[string]string{}
	cfg.BudgetMessage = msg
	resume(t, s, cfg, Request{Role: Researcher, Messages: []api.Message{{Role: "user", Content: "go"}}}, "line 2: if (a) {")
	var condensers int
	for _, r := range s.reqs {
		if r.Role == Condenser {
			condensers++
			if !strings.Contains(r.Messages[1].Content, "line 2: if (a) {") {
				t.Error("the condenser did not read what the call returned")
			}
		}
	}
	if condensers != 1 {
		t.Fatalf("%d condensations, want 1", condensers)
	}
	var step2 Request
	for _, r := range s.reqs {
		if r.Role == Researcher && len(thoughts(r)) > 0 {
			step2 = r
			break
		}
	}
	if got := thoughts(step2); len(got) != 1 || !strings.HasPrefix(got[0], condensedLeadIn) || strings.Contains(got[0], "wait, again") {
		t.Fatalf("step 2 carried %q", got)
	}
}
