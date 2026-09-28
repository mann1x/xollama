package council

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

func editCall(id, old, new string) api.ToolCall {
	args := api.NewToolCallFunctionArguments()
	args.Set("path", "a.js")
	args.Set("old_text", old)
	args.Set("new_text", new)
	return api.ToolCall{ID: id, Function: api.ToolCallFunction{Name: "write_file", Arguments: args}}
}

// A change whose new text is its old text is refused in place: it asks for
// nothing, and never costs a trip (Cerebriline's "No change:").
func TestANoOpChangeIsRefusedInPlace(t *testing.T) {
	cfg := toolCfg()
	noop, real := editCall("n", "x", "x"), editCall("r", "x", "y")
	turns := []api.Message{{Role: "assistant", ToolCalls: []api.ToolCall{noop, real}}}
	fw := cfg.forwarded(Synthesizer, "s", turns)
	if len(fw) != 1 || fw[0].ID != ForwardedID("s", "r") {
		t.Fatalf("forwarded %+v, want only the real change", fw)
	}
	var got []string
	for _, m := range cfg.transcript(Synthesizer, "s", turns) {
		if m.Role == "tool" {
			got = append(got, m.Content)
		}
	}
	if len(got) != 2 || got[0] != noChange {
		t.Errorf("results %q, want the no-op answered with the refusal", got)
	}
	if noOp(editCall("e", "", "")) {
		t.Error("two empty texts are not a no-op to refuse")
	}
}

// strikeStub's synthesizer sends the same change on every step.
type strikeStub struct{ toolStub }

func (s *strikeStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	if req.Role != Synthesizer {
		return s.toolStub.StreamTools(ctx, req, onToken)
	}
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	return Reply{Calls: []api.ToolCall{editCall(fmt.Sprint("c", len(req.Messages)), "x", "y")}}, nil
}

// The same change sent again and again with the same result is steered, and
// at the limit the synthesizer's steps end with a report.
func TestTheSameChangeSentAgainEndsTheSteps(t *testing.T) {
	build := `{"target":"t","planner":"","researcher":"","critic":"","synthesizer":"","max_tests":1,"max_steps":12}`
	s := &strikeStub{toolStub{stub: stub{route: `{"route":"council"}`, build: build}}}
	res := driveResults(t, toolCfg(), s, func(int) string { return "old_text not found" })
	// Two cycles (a testing one, then the last), each stopped at the limit
	// rather than at its step budget of 12.
	per := map[int]int{}
	for _, c := range s.calls {
		if c.Role == Synthesizer {
			per[c.Round]++
		}
	}
	if len(per) != 2 || per[0] != loopStrikes || per[1] != loopStrikes {
		t.Errorf("synthesizer calls per cycle %v, want %d in each of 2", per, loopStrikes)
	}
	if res.Kept == nil || len(res.Kept.Prior) != 1 || !strings.Contains(res.Kept.Prior[0], strikeStop) {
		t.Errorf("the testing cycle's report does not say its steps were stopped: %+v", res.Kept)
	}
	last := all(lastOf(s.calls, Synthesizer))
	if !strings.Contains(last, strikeNote(loopStrikes-1)) || !strings.Contains(last, strikeNote(2)) {
		t.Errorf("the ladder was not shown:\n%s", last)
	}
}

// A refused change is made again from the text as it is now, never resent
// unchanged; and the planner keeps every researcher working.
func TestTheCouncilIsToldToRedoARefusedChangeAndKeepResearchersBusy(t *testing.T) {
	s := &retestStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}}
	if _, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all(lastOf(s.calls, Synthesizer)), refusedEdit) {
		t.Error("the synthesizer is not told how to redo a refused change")
	}
	if strings.Contains(builderPrompt, "never resend an edit that failed") || !strings.Contains(builderPrompt, "make the edit again from that text") {
		t.Error("the builder's example still tells the synthesizer to drop a refused edit")
	}
	if !strings.Contains(all(lastOf(s.calls, Planner)), "never leave one without a task while tasks are open") {
		t.Error("the planner is not told to keep every researcher working")
	}
}
