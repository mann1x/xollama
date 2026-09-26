package council

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// bigLog is a result too long to carry, with its one fact deep inside.
func bigLog() string {
	var b strings.Builder
	for i := 1; i <= 800; i++ {
		if i == 612 {
			b.WriteString("2026-10-14 DEADLINE for Project Heron\n")
			continue
		}
		fmt.Fprintf(&b, "2026-09-%02d build step %d ok\n", i%28+1, i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func evidenceCall(args map[string]any) api.ToolCall {
	a := api.NewToolCallFunctionArguments()
	for k, v := range args {
		a.Set(k, v)
	}
	return api.ToolCall{Function: api.ToolCallFunction{Name: EvidenceTool, Arguments: a}}
}

func TestEvidenceIsReadBackByLinesOrPattern(t *testing.T) {
	cfg := Config{Results: map[string]string{"r1:call_0_0": bigLog()}}
	for _, tc := range []struct {
		args      map[string]any
		want, not string
	}{
		{map[string]any{"ref": "r1:call_0_0", "pattern": "DEADLINE"}, "612: 2026-10-14 DEADLINE for Project Heron", "step 611"},
		{map[string]any{"ref": "r1:call_0_0", "pattern": "("}, "No line of r1:call_0_0 matches", ""},
		{map[string]any{"ref": "r1:call_0_0", "lines": "611-613"}, "Lines 611-613 of 800 in r1:call_0_0:\n2026-09-24 build step 611 ok\n2026-10-14 DEADLINE for Project Heron\n2026-09-26 build step 613 ok", "step 614"},
		{map[string]any{"ref": "r1:call_0_0", "lines": "799-9999"}, "Lines 799-800 of 800", ""},
		{map[string]any{"ref": "r1:call_0_0", "lines": "900-950"}, "has 800 lines", ""},
		{map[string]any{"ref": "r1:call_0_0"}, "Lines 1-200 of 800", "step 201 "},
		{map[string]any{"ref": "r9:call_0_0"}, `No evidence has the ref "r9:call_0_0"`, ""},
	} {
		got := cfg.lookup(evidenceCall(tc.args))
		if !strings.Contains(got, tc.want) || tc.not != "" && strings.Contains(got, tc.not) {
			t.Errorf("%v: got %q", tc.args, got)
		}
	}
}

// A long result travels as its ref and first lines; a short one whole; and
// without the lookup a long one is cut, as before.
func TestALongResultTravelsByRef(t *testing.T) {
	turns := []api.Message{{Role: "assistant", ToolCalls: []api.ToolCall{
		{ID: "call_0_0", Function: api.ToolCallFunction{Name: "read_files"}},
		{ID: "call_0_1", Function: api.ToolCallFunction{Name: "read_files"}},
	}}}
	cfg := Config{Tools: WithEvidence(testTools), Results: map[string]string{"r1:call_0_0": bigLog(), "r1:call_0_1": "SHORT-RESULT"}}
	ev := cfg.evidence(Researcher, "r1", turns)
	if !strings.Contains(ev, "[ref r1:call_0_0: ") || !strings.Contains(ev, "800 lines -- read more with council_evidence]") || strings.Contains(ev, "DEADLINE") || !strings.Contains(ev, "returned:\nSHORT-RESULT") {
		t.Fatalf("evidence: %q", ev)
	}
	if len(ev) > 2*previewChars {
		t.Fatalf("the index is %d characters", len(ev))
	}
	cfg.Tools = testTools
	if ev := cfg.evidence(Researcher, "r1", turns); strings.Contains(ev, "[ref ") || !strings.Contains(ev, "more characters]") {
		t.Fatalf("without the lookup: %q", ev)
	}
}

// A critic reads the line it needs from a researcher's long result: answered
// in the server, never sent to the client, and the turn goes on to the answer.
func TestACriticReadsEvidenceInTheServer(t *testing.T) {
	s := &toolStub{
		stub: stub{route: `{"route":"council"}`},
		call: map[string]string{"r1": "read_files", "c1": EvidenceTool},
		args: map[string]map[string]any{"c1": {"ref": "r1:call_0_0", "pattern": "DEADLINE"}},
	}
	cfg := FromModel(nil, 0.7)
	cfg.Tools = WithEvidence(testTools)
	res, err := Run(t.Context(), cfg, s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Results = map[string]string{"r1:call_0_0": bigLog()}
	res, err = RunFrom(t.Context(), cfg, s, conv, res.Progress, nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Calls) != 0 || res.Answer == "" {
		t.Fatalf("the lookup left the server: calls %v, answer %q", callIDs(res.Calls), res.Answer)
	}
	var critique, synth string
	for _, c := range s.calls {
		last := c.Messages[len(c.Messages)-1].Content
		switch c.Role {
		case Critic:
			if c.Index == 0 && strings.HasPrefix(last, "Lines") || strings.Contains(last, "DEADLINE") {
				critique = last
			}
		case Synthesizer:
			for _, m := range c.Messages {
				synth += m.Content
			}
		}
	}
	if critique != "612: 2026-10-14 DEADLINE for Project Heron\n" {
		t.Fatalf("the critic read %q", critique)
	}
	// The stub's reply echoes what it read; the evidence is what the council
	// adds, and it carries the index alone.
	_, ev, _ := strings.Cut(synth, "FINDINGS OF RESEARCHER 1:")
	_, ev, _ = strings.Cut(ev, "Evidence (the tools called")
	ev, _, _ = strings.Cut(ev, "FINDINGS OF RESEARCHER 2:")
	if !strings.Contains(ev, "[ref r1:call_0_0: ") || strings.Contains(ev, "build step 700") {
		t.Fatalf("the synthesizer's evidence: %q", ev)
	}
}

// A member that only looks things up is stopped after maxLookups turns.
func TestLookupsAreBounded(t *testing.T) {
	s := &loopStub{}
	cfg := Config{Tools: WithEvidence(testTools)}
	req := Request{Role: Critic, Messages: []api.Message{{Role: "user", Content: "go"}}}
	out, turns, err := callTools(t.Context(), s, cfg, req, nil, func(string) {})
	if err != nil || turns != nil {
		t.Fatalf("out %q turns %v err %v", out, turns, err)
	}
	if s.n != maxLookups+1 {
		t.Fatalf("asked %d times", s.n)
	}
}

type loopStub struct {
	stub
	n int
}

func (s *loopStub) StreamTools(_ context.Context, _ Request, _ func(string)) (Reply, error) {
	s.n++
	return Reply{Calls: []api.ToolCall{evidenceCall(map[string]any{"ref": "x"})}}, nil
}

func TestWithEvidence(t *testing.T) {
	if WithEvidence(nil) != nil {
		t.Fatal("a turn without tools gained one")
	}
	got := WithEvidence(testTools)
	if len(got) != 3 || got[2].Function.Name != EvidenceTool || !got[2].Function.ReadOnly || len(testTools) != 2 {
		t.Fatalf("%v", got)
	}
	if again := WithEvidence(got); len(again) != 3 {
		t.Fatalf("added twice: %d", len(again))
	}
}

// A member that read a long result, then asks for another, carries the first
// by ref once its own results outgrow ownBudget; the newest stay whole.
func TestAMemberFoldsItsOwnOldResults(t *testing.T) {
	turns := []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call_0_0", Function: api.ToolCallFunction{Name: "read_files"}}}},
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call_1_0", Function: api.ToolCallFunction{Name: "read_files"}}}},
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call_2_0", Function: api.ToolCallFunction{Name: "read_files"}}}},
	}
	long := bigLog()
	cfg := Config{Tools: WithEvidence(testTools), Results: map[string]string{"r2:call_0_0": long, "r2:call_1_0": "small", "r2:call_2_0": long}}
	var got []string
	for _, m := range cfg.transcript(Researcher, "r2", turns) {
		if m.Role == "tool" {
			got = append(got, m.Content)
		}
	}
	if len(got) != 3 || !strings.HasPrefix(got[0], "[ref r2:call_0_0: ") || got[1] != "small" || got[2] != long {
		t.Fatalf("transcript results: %.60q", got)
	}
	// Within the budget, or without the lookup, nothing is folded.
	cfg.Results["r2:call_0_0"] = "short"
	if f := cfg.folded(Researcher, "r2", turns); len(f) != 0 {
		t.Fatalf("folded within the budget: %v", f)
	}
	cfg.Results["r2:call_0_0"], cfg.Tools = long, testTools
	if f := cfg.folded(Researcher, "r2", turns); len(f) != 0 {
		t.Fatalf("folded without the lookup: %v", f)
	}
}
