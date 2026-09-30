package council

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// scriptStub answers each call with the next reply of its script, and keeps
// the requests.
type scriptStub struct {
	stub
	replies []Reply
	reqs    []Request
}

func (s *scriptStub) StreamTools(_ context.Context, req Request, _ func(string)) (Reply, error) {
	s.reqs = append(s.reqs, req)
	if len(s.replies) == 0 {
		return Reply{Content: "nothing left"}, nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func tcall(name string, args map[string]any) api.ToolCall {
	a := api.NewToolCallFunctionArguments()
	for k, v := range args {
		a.Set(k, v)
	}
	return api.ToolCall{Function: api.ToolCallFunction{Name: name, Arguments: a}}
}

func reportCfg() Config {
	cfg := toolCfg()
	cfg.Tools = WithReports(WithEvidence(testTools))
	cfg.Results = map[string]string{}
	return cfg
}

// resume runs a member to its reply, answering each forwarded call with
// result.
func resume(t *testing.T, s ToolModel, cfg Config, req Request, result string) string {
	t.Helper()
	key := MemberKey(req.Role, req.Index, req.Round)
	var turns []api.Message
	for trip := 0; ; trip++ {
		out, next, err := callTools(t.Context(), s, cfg, req, turns, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if next == nil {
			return out
		}
		if trip > 20 {
			t.Fatal("no reply after 20 trips")
		}
		for _, c := range next[len(next)-1].ToolCalls {
			next = append(next, api.Message{Role: "tool", ToolCallID: ForwardedID(key, c.ID), Content: result})
			if cfg.Results != nil {
				cfg.Results[ForwardedID(key, c.ID)] = result // as the server fills it from the client's results
			}
		}
		turns = next
	}
}

// A researcher's report is its record: proposals checked against its own
// reads, and its evidence by ref rather than inline.
func TestAResearchersReportIsTyped(t *testing.T) {
	file := "line one\nif (a) { b();\nline three"
	s := &scriptStub{replies: []Reply{
		{Calls: []api.ToolCall{tcall("read_files", map[string]any{"path": "game.js"})}},
		{Calls: []api.ToolCall{tcall(ReportTool, map[string]any{
			"summary": "A brace is missing.",
			"proposals": []any{
				map[string]any{"path": "game.js", "old_text": "if (a) { b();", "new_text": "if (a) { b(); }", "why": "unclosed block", "check": "it loads"},
				map[string]any{"path": "game.js", "old_text": "text it never read", "new_text": "x", "why": "a guess"},
			},
			"claims": []any{map[string]any{"claim": "the block opens on line 2", "evidence": "game.js:2"}},
		})}},
	}}
	out := resume(t, s, reportCfg(), Request{Role: Researcher, Messages: []api.Message{{Role: "user", Content: "go"}}}, file)
	for _, want := range []string{"A brace is missing.", "its reads show this text", "NOT in its reads", "game.js:2", "Evidence (read any of it with " + EvidenceTool} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "line three") {
		t.Errorf("the read went inline:\n%s", out)
	}
}

// A critic's verdict is a call; the council routes on its field.
func TestACriticsVerdictIsTyped(t *testing.T) {
	s := &scriptStub{replies: []Reply{{Calls: []api.ToolCall{tcall(VerdictTool, map[string]any{"verdict": "confirmed", "place": "game.js:2", "why": "the block never closes"})}}}}
	out := resume(t, s, reportCfg(), Request{Role: Critic, Messages: []api.Message{{Role: "user", Content: "go"}}}, "")
	if place, ok := confirmed(out); !ok || place != "game.js:2" {
		t.Fatalf("confirmed %q %v in %q", place, ok, out)
	}
}

// A report made beside a read waits for the read: it is not taken until it
// is the turn's one call.
func TestAReportBesideAReadWaitsForIt(t *testing.T) {
	s := &scriptStub{replies: []Reply{
		{Calls: []api.ToolCall{tcall("read_files", map[string]any{"path": "a"}), tcall(ReportTool, map[string]any{"summary": "early", "proposals": []any{}, "claims": []any{}})}},
		{Calls: []api.ToolCall{tcall(ReportTool, map[string]any{"summary": "after the read", "proposals": []any{}, "claims": []any{}})}},
	}}
	out := resume(t, s, reportCfg(), Request{Role: Researcher, Messages: []api.Message{{Role: "user", Content: "go"}}}, "data")
	if !strings.HasPrefix(out, "after the read") {
		t.Fatalf("the report taken: %q", out)
	}
}

// At its step bound a reader answers in its result's format, and the record
// is its report.
func TestTheForcedAnswerIsTheReportsFormat(t *testing.T) {
	var replies []Reply
	for range researcherSteps {
		replies = append(replies, Reply{Calls: []api.ToolCall{tcall("read_files", map[string]any{"path": "f"})}})
	}
	replies = append(replies, Reply{Content: `{"summary":"forced","proposals":[],"claims":[{"claim":"it reads","evidence":"f:1"}]}`})
	s := &scriptStub{replies: replies}
	out := resume(t, s, reportCfg(), Request{Role: Researcher, Messages: []api.Message{{Role: "user", Content: "go"}}}, "data")
	last := s.reqs[len(s.reqs)-1]
	if string(last.Format) != string(reportSchema) {
		t.Fatalf("the last call's format: %s", last.Format)
	}
	if !strings.HasPrefix(out, "forced") || !strings.Contains(out, "f:1") {
		t.Fatalf("the report: %q", out)
	}
}

// The synthesizer's verdicts are calls, rendered as the markers the loop
// turns on.
func TestTheSynthesizersVerdictsAreTyped(t *testing.T) {
	cfg := reportCfg()
	cfg.MaxTests = 2
	s := &scriptStub{replies: []Reply{{Content: "Fixed the brace.", Calls: []api.ToolCall{tcall(RetestTool, map[string]any{"tried": "closed the block; it still fails"})}}}}
	out := resume(t, s, cfg, Request{Role: Synthesizer, Messages: []api.Message{{Role: "user", Content: "go"}}}, "")
	if !cfg.retested(out, 0) || !strings.Contains(out, "closed the block") {
		t.Fatalf("the reply: %q", out)
	}
}

// refusingStub refuses every request carrying a format, as an engine does
// that cannot parse its grammar, and answers the rest from its script.
type refusingStub struct{ scriptStub }

func (s *refusingStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	if req.Format != nil {
		s.reqs = append(s.reqs, req)
		return Reply{}, errors.New(`{"error":{"code":400,"message":"Failed to initialize samplers: failed to parse grammar"}}`)
	}
	return s.scriptStub.StreamTools(ctx, req, onToken)
}

// A result's schema carries no text bound: the engine expands maxLength into
// a grammar repetition it cannot parse (live on eleven2go, run 0415).
func TestNoResultSchemaBoundsItsText(t *testing.T) {
	for _, r := range []Role{Researcher, Critic} {
		if s := string(resultSchema(r)); strings.Contains(s, "maxLength") || strings.Contains(s, "minLength") {
			t.Errorf("%s's schema bounds its text: %s", r, s)
		}
	}
}

// An engine that refuses the forced answer's format does not end the member:
// it is asked once more without it, and its report stands.
func TestARefusedFormatIsAskedAgainWithout(t *testing.T) {
	cfg := reportCfg()
	var steps []Reply
	for range researcherSteps {
		steps = append(steps, Reply{Calls: []api.ToolCall{tcall("read_files", map[string]any{"path": "game.js"})}})
	}
	s := &refusingStub{scriptStub{replies: append(steps, Reply{Content: "The brace on line 2 is unclosed."})}}
	out := resume(t, s, cfg, Request{Role: Researcher, Messages: []api.Message{{Role: "user", Content: "go"}}}, "file")
	if !strings.Contains(out, "The brace on line 2 is unclosed.") {
		t.Fatalf("the member's report: %q", out)
	}
	last := s.reqs[len(s.reqs)-1]
	if last.Format != nil {
		t.Error("the member was asked with the refused format again")
	}
}
