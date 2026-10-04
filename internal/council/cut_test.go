package council

import (
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// cutStub's cut roles reach their reply cap mid-call, always or only on the
// first call.
type cutStub struct {
	frontStub
	cut    map[Role]bool
	always bool
}

func (s *cutStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	last := req.Messages[len(req.Messages)-1].Content
	if s.cut[req.Role] && (s.always || last != cutTurn().Content) {
		s.mu.Lock()
		s.calls = append(s.calls, req)
		s.mu.Unlock()
		onToken("I'll rewrite the part")
		return Reply{Content: "I'll rewrite the part", Cut: true}, nil
	}
	return s.frontStub.StreamTools(ctx, req, onToken)
}

func calls(s *cutStub, r Role) []Request {
	var out []Request
	for _, c := range s.calls {
		if c.Role == r {
			out = append(out, c)
		}
	}
	return out
}

// A writer on a tool turn has room for an edit, and a reply the cap cut
// before its call is asked again once, told to change less at once.
func TestACutWriterIsAskedAgainForASmallerChange(t *testing.T) {
	for _, r := range []Role{Front, Synthesizer} {
		route := `{"route":"council"}`
		if r == Front {
			route = `{"route":"direct"}`
		}
		s := &cutStub{frontStub: frontStub{toolStub{stub: stub{route: route}}}, cut: map[Role]bool{r: true}}
		if _, err := Run(t.Context(), frontCfg(), s, conv, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		cs := calls(s, r)
		if len(cs) < 2 {
			t.Fatalf("%s: %d calls, want the cut one asked again", r, len(cs))
		}
		for _, c := range cs {
			if c.MaxTokens < writeMaxTokens {
				t.Errorf("%s: reply cap %d, below an edit's %d", r, c.MaxTokens, writeMaxTokens)
			}
		}
		if got := cs[1].Messages[len(cs[1].Messages)-1].Content; got != cutTurn().Content {
			t.Errorf("%s: the second call ends with %q, not the note", r, got)
		}
	}
}

// Cut again right after the note, the reply stands: the council does not
// loop on a member that cannot fit its change.
func TestACutWriterIsAskedAgainOnlyOnce(t *testing.T) {
	s := &cutStub{frontStub: frontStub{toolStub{stub: stub{route: `{"route":"direct"}`}}}, cut: map[Role]bool{Front: true}, always: true}
	res, err := Run(t.Context(), frontCfg(), s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(calls(s, Front)); n != 1+maxCuts {
		t.Errorf("front calls %d, want %d", n, 1+maxCuts)
	}
	if !strings.Contains(res.Answer, "I'll rewrite the part") {
		t.Errorf("answer %q", res.Answer)
	}
}

// Only a member that writes is asked again: a cut researcher's findings are
// what it wrote.
func TestOnlyAWriterIsAskedAgain(t *testing.T) {
	if wasCut(Researcher, Reply{Cut: true}) || wasCut(Critic, Reply{Cut: true}) {
		t.Error("a reader was asked again")
	}
	if !wasCut(Synthesizer, Reply{Cut: true}) || wasCut(Synthesizer, Reply{Cut: true, Calls: []api.ToolCall{editCall("a", "x", "y")}}) {
		t.Error("wasCut ignores the calls a cut reply finished")
	}
	if writeTok(Researcher, 2048) != 2048 || writeTok(Synthesizer, 2048) != writeMaxTokens || writeTok(Synthesizer, 0) != 0 {
		t.Error("writeTok")
	}
}
