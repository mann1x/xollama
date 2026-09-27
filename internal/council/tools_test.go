package council

import (
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// toolStub is a stub whose members can call tools: the member named in call
// (by MemberKey) calls that tool until its transcript holds a result, then
// answers with the results it read.
type toolStub struct {
	stub
	call map[string]string
	// narrate has the named members first describe a call in prose, with an
	// invented result, instead of making it -- until they are told off.
	narrate map[string]bool
	// args are the arguments of the named members' calls.
	args map[string]map[string]any
}

func (s *toolStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	var results []string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			results = append(results, m.Content)
		}
	}
	key := MemberKey(req.Role, req.Index, req.Round)
	if s.narrate[key] && len(results) == 0 && req.Messages[len(req.Messages)-1].Content != narratedNudge {
		out := "I called read_files and it returned INVENTED-DATA."
		onToken(out)
		return Reply{Content: out}, nil
	}
	if name := s.call[key]; name != "" && len(results) == 0 {
		args := api.NewToolCallFunctionArguments()
		for k, v := range s.args[key] {
			args.Set(k, v)
		}
		return Reply{Content: "let me look", Calls: []api.ToolCall{{Function: api.ToolCallFunction{Name: name, Arguments: args}}}}, nil
	}
	out := string(req.Role) + " says " + strings.Join(results, "|")
	onToken(out)
	return Reply{Content: out}, nil
}

var testTools = api.Tools{
	{Type: "function", Function: api.ToolFunction{Name: "read_files", ReadOnly: true}},
	{Type: "function", Function: api.ToolFunction{Name: "write_file"}},
}

func toolCfg() Config {
	cfg := FromModel(nil, 0.7)
	cfg.Tools = testTools
	return cfg
}

func callIDs(calls []api.ToolCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.ID+"="+c.Function.Name)
	}
	return out
}

// Both researchers read: the turn ends at the research step with both calls in
// one message, each under its member's id; the results bring it back there.
func TestParallelMembersToolCallsGoOutTogetherAndComeBack(t *testing.T) {
	s := &toolStub{stub: stub{route: `{"route":"council"}`}, call: map[string]string{"r1": "read_files", "r2": "read_files"}}
	cfg := toolCfg()
	res, err := Run(t.Context(), cfg, s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(callIDs(res.Calls), " "); got != "r1:call_0_0=read_files r2:call_0_0=read_files" {
		t.Fatalf("forwarded %s", got)
	}
	if res.Answer != "" || s.count(Critic) != 0 || s.count(Synthesizer) != 0 {
		t.Fatalf("the turn went past the step that waits: answer %q, critics %d", res.Answer, s.count(Critic))
	}
	if res.Calls[1].Function.Index != 1 || len(res.Progress.Suspended) != 2 {
		t.Fatalf("index %d, suspended %v", res.Calls[1].Function.Index, res.Progress.Suspended)
	}

	s2 := &toolStub{stub: stub{route: `{"route":"council"}`}, call: s.call}
	cfg.Results = map[string]string{"r1:call_0_0": "R1-DATA", "r2:call_0_0": "R2-DATA"}
	res2, err := RunFrom(t.Context(), cfg, s2, conv, res.Progress, nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Answer == "" || len(res2.Calls) != 0 || len(res2.Progress.Suspended) != 0 {
		t.Fatalf("resumed: answer %q calls %v", res2.Answer, res2.Calls)
	}
	if s2.count(Planner) != 0 || s2.count(Researcher) != 2 {
		t.Fatalf("resumed: planner %d researchers %d", s2.count(Planner), s2.count(Researcher))
	}
	var crit Request
	for _, c := range s2.calls {
		if c.Role == Critic {
			crit = c
		}
	}
	findings := crit.Messages[len(crit.Messages)-2].Content
	// The reply, not the narration before its call; then what it read.
	if strings.Contains(findings, "let me look") || !strings.Contains(findings, "researcher says R1-DATA\n\nEvidence (the tools called and what they returned):\n- read_files {} returned:\nR1-DATA") || !strings.Contains(findings, "returned:\nR2-DATA") {
		t.Fatalf("the critics read %q", findings)
	}
}

// One researcher reads, the other answers: only the reader waits, and the
// resumed turn does not ask the other again.
func TestOnlyTheMemberThatCalledWaits(t *testing.T) {
	s := &toolStub{stub: stub{route: `{"route":"council"}`}, call: map[string]string{"r2": "read_files"}}
	cfg := toolCfg()
	res, _ := Run(t.Context(), cfg, s, conv, func(Event) {})
	if got := strings.Join(callIDs(res.Calls), " "); got != "r2:call_0_0=read_files" {
		t.Fatalf("forwarded %s", got)
	}
	if f := res.Progress.Rounds[0].Findings; f[0] == "" || f[1] != "" {
		t.Fatalf("findings %q", f)
	}
	s2 := &toolStub{stub: stub{route: `{"route":"council"}`}, call: s.call}
	cfg.Results = map[string]string{"r2:call_0_0": "DATA"}
	if _, err := RunFrom(t.Context(), cfg, s2, conv, res.Progress, nil, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if s2.count(Researcher) != 1 {
		t.Fatalf("resumed: %d researcher calls, want the one that waited", s2.count(Researcher))
	}
}

// A researcher may not write: the call is refused in place and never leaves.
// The synthesizer may, and its call goes to the client.
func TestOnlyTheSynthesizerWrites(t *testing.T) {
	s := &toolStub{stub: stub{route: `{"route":"council"}`}, call: map[string]string{"r1": "write_file", "c1": "no_such_tool", "s": "write_file"}}
	res, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(callIDs(res.Calls), " "); got != "s:call_0_0=write_file" {
		t.Fatalf("forwarded %s", got)
	}
	if f := res.Progress.Rounds[0].Findings[0]; !strings.Contains(f, "Refused: write_file can change things") {
		t.Fatalf("the refused researcher reported %q", f)
	}
	if c := res.Progress.Rounds[0].Critiques[0]; !strings.Contains(c, "Refused: there is no tool named no_such_tool") {
		t.Fatalf("the critic reported %q", c)
	}
}

// A direct turn is the planner answering: it may call every tool.
func TestADirectAnswerCallsTools(t *testing.T) {
	s := &toolStub{stub: stub{route: `{"route":"direct"}`}, call: map[string]string{"d": "write_file"}}
	res, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {})
	if err != nil || strings.Join(callIDs(res.Calls), " ") != "d:call_0_0=write_file" {
		t.Fatalf("forwarded %v (%v)", callIDs(res.Calls), err)
	}
}

// Researchers and critics are told which tools are theirs, in their own
// instruction; the prefix every member shares does not change.
func TestResearchersAreToldWhichToolsOnlyRead(t *testing.T) {
	s := &toolStub{stub: stub{route: `{"route":"council"}`}}
	if _, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.calls {
		last := c.Messages[len(c.Messages)-1].Content
		note := strings.Contains(last, "You may call these tools, which only read: read_files.")
		if want := c.Role == Researcher || c.Role == Critic; note != want {
			t.Errorf("%s: tool note %v in %q", c.Role, note, last)
		}
	}
}

// Without tools, or on a Model that cannot call them, nothing changes.
func TestAModelWithoutToolsCallsNothing(t *testing.T) {
	s := &stub{route: `{"route":"council"}`}
	res, err := Run(t.Context(), toolCfg(), s, conv, func(Event) {})
	if err != nil || res.Answer == "" || res.Calls != nil {
		t.Fatalf("answer %q calls %v (%v)", res.Answer, res.Calls, err)
	}
}

// A turn with tools tells every member, through the charter the planner's
// requests open with, that the tools are real and what each role does with
// them; a turn without tools reads the charter it always did.
func TestTheCharterNamesTheToolsOnlyWhenThereAreSome(t *testing.T) {
	for _, tools := range []api.Tools{testTools, nil} {
		s := &toolStub{stub: stub{route: `{"route":"council"}`}}
		cfg := FromModel(nil, 0.7)
		cfg.Tools = tools
		if _, err := Run(t.Context(), cfg, s, conv, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		for _, c := range s.calls {
			if c.Role != Planner {
				continue
			}
			req := c.Messages[len(c.Messages)-1].Content
			if got := strings.Contains(req, "never describe a call you did not make"); got != (tools != nil) {
				t.Errorf("tools %v: the planner's request carries the tool charter: %v", tools != nil, got)
			}
		}
	}
}

// The synthesizer's answer is the user's: it carries no evidence. A result
// longer than the cap is cut and says by how much.
func TestEvidenceIsCappedAndNeverInTheAnswer(t *testing.T) {
	s := &toolStub{stub: stub{route: `{"route":"council"}`}, call: map[string]string{"r1": "read_files", "s": "read_files"}}
	cfg := FromModel(nil, 0.7)
	cfg.Tools = testTools
	res, _ := Run(t.Context(), cfg, s, conv, func(Event) {})
	cfg.Results = map[string]string{"r1:call_0_0": strings.Repeat("x", maxEvidence+10)}
	res, _ = RunFrom(t.Context(), cfg, s, conv, res.Progress, nil, func(Event) {})
	cfg.Results = map[string]string{"s:call_0_0": "S-DATA"}
	res, err := RunFrom(t.Context(), cfg, s, conv, res.Progress, nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Answer, "Evidence") || !strings.Contains(res.Answer, "S-DATA") {
		t.Fatalf("answer %q", res.Answer)
	}
	var synth Request
	for _, c := range s.calls {
		if c.Role == Synthesizer {
			synth = c
		}
	}
	all := ""
	for _, m := range synth.Messages {
		all += m.Content
	}
	// (The stub's reply echoes the whole result; the evidence entry is capped.)
	if !strings.Contains(all, "read_files {} returned:\n"+strings.Repeat("x", maxEvidence)+"\n[... 10 more characters]") {
		t.Fatalf("the synthesizer did not read the researcher's result, capped: %.300q", all[len(all)-min(len(all), 700):])
	}
}

// With tools the built-in charter stops telling researchers to use only their
// own knowledge; it says so again without them.
func TestTheCharterLetsResearchersReadOnlyWithTools(t *testing.T) {
	cfg := FromModel(nil, 0.7)
	if !strings.Contains(cfg.charter(), charterNoTools) {
		t.Fatal("the built-in charter changed; charterNoTools no longer matches it")
	}
	cfg.Tools = testTools
	if c := cfg.charter(); strings.Contains(c, charterNoTools) || !strings.Contains(c, charterWithTools) {
		t.Fatalf("charter with tools: %q", c)
	}
}

// A researcher that describes a call instead of making it is told once, and
// its narration -- with the result it invented -- never reaches the findings.
// A critic naming a tool is not a narration: it answers from the evidence.
func TestANarratedCallIsNeverAFinding(t *testing.T) {
	s := &toolStub{stub: stub{route: `{"route":"council"}`}, call: map[string]string{"r1": "read_files"}, narrate: map[string]bool{"r1": true, "c1": true}}
	cfg := FromModel(nil, 0.7)
	cfg.Tools = testTools
	res, err := Run(t.Context(), cfg, s, conv, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(callIDs(res.Calls), " "); got != "r1:call_0_0=read_files" {
		t.Fatalf("the nudged researcher forwarded %s", got)
	}
	cfg.Results = map[string]string{"r1:call_0_0": "REAL-DATA"}
	_, err = RunFrom(t.Context(), cfg, s, conv, res.Progress, nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	var synth Request
	critics := 0
	for _, c := range s.calls {
		if c.Role == Synthesizer {
			synth = c
		}
		if c.Role == Critic {
			critics++
		}
	}
	all := ""
	for _, m := range synth.Messages {
		all += m.Content
	}
	if !strings.Contains(all, "FINDINGS OF RESEARCHER 1:\nresearcher says REAL-DATA") || strings.Contains(all, "FINDINGS OF RESEARCHER 1:\nI called read_files") {
		t.Fatalf("the synthesizer read: %q", all)
	}
	if critics != 2 {
		t.Fatalf("critics were asked %d times; a critic naming a tool is not nudged", critics)
	}
}
