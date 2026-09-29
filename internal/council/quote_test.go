package council

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

func pathRead(id, path string) api.ToolCall {
	args := api.NewToolCallFunctionArguments()
	args.Set("path", path)
	return api.ToolCall{ID: id, Function: api.ToolCallFunction{Name: "read_files", Arguments: args}}
}

// dIt is e75c7c3e's line: 564 characters of minified code ending in the
// stray space and brace the synthesizer kept misremembering.
var dIt = "function dIt(c){" + strings.Repeat("c.fillRect(0,0,1,1);", 26) + "c.restore();} })};"

// quoteCase is a member that read a.js, then sent the change in the last turn.
func quoteCase(read string, change api.ToolCall) (Config, []api.Message) {
	cfg := toolCfg()
	cfg.Results = map[string]string{ForwardedID("s", "r"): read}
	return cfg, []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{pathRead("r", "a.js")}},
		{Role: "assistant", ToolCalls: []api.ToolCall{change}},
	}
}

func results(cfg Config, turns []api.Message) []string {
	var out []string
	for _, m := range cfg.transcript(Synthesizer, "s", turns) {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

// A change quoting the start of a line exactly and its end as code usually
// looks is answered in place, with what the read has where the quote stops.
func TestAMisquotedChangeIsAnsweredWithTheActualText(t *testing.T) {
	read := "1: let a=1;\n2: " + dIt + "\n3: let b=2;"
	misq := strings.TrimSuffix(dIt, "} })};") + "}}})"
	cfg, turns := quoteCase(read, editCall("e", misq, "x"))
	if fw := cfg.forwarded(Synthesizer, "s", turns); len(fw) != 0 {
		t.Fatalf("the misquote was forwarded: %+v", fw)
	}
	got := results(cfg, turns)
	if len(got) != 2 || !strings.HasPrefix(got[1], "Not sent:") || !strings.Contains(got[1], "the read has « })};» where your quote has «}})»") {
		t.Errorf("answer %q, want the read's own tail", got)
	}
}

// What the read has, gutter and all, goes out; so does a quote of text the
// read does not show much of, and one after a change that went through.
func TestAChangeTheReadBearsOutGoesOut(t *testing.T) {
	read := "1: let a=1;\n2: " + dIt + "\n3: let b=2;"
	for name, tc := range map[string]struct {
		quote string
		turns func([]api.Message) []api.Message
	}{
		"exact, across lines": {quote: "let a=1;\n" + dIt},
		"too little matches":  {quote: "function initClouds(){ return clouds.map(function(c){ return c; }) }"},
		"after a change it cannot replay": {quote: strings.TrimSuffix(dIt, "} })};") + "}}})", turns: func(ts []api.Message) []api.Message {
			// Its quote is not in the read: what the target holds now is unknown.
			return append(ts[:1:1], api.Message{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w", "not in the read", "x")}}, ts[1])
		}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, turns := quoteCase(read, editCall("e", tc.quote, "x"))
			if tc.turns != nil {
				turns = tc.turns(turns)
				cfg.Results[ForwardedID("s", "w")] = "edited a.js"
			}
			if fw := cfg.forwarded(Synthesizer, "s", turns); len(fw) != 1 || fw[0].ID != ForwardedID("s", "e") {
				t.Errorf("forwarded %+v, want the change", fw)
			}
		})
	}
}

// A change the tool refused left the target as the read showed it.
func TestARefusedChangeKeepsTheRead(t *testing.T) {
	read := "1: " + dIt
	misq := strings.TrimSuffix(dIt, "} })};") + "});}}"
	cfg, turns := quoteCase(read, editCall("e", misq, "x"))
	turns = append(turns[:1:1], api.Message{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w", "nothing here", "y")}}, turns[1])
	cfg.Results[ForwardedID("s", "w")] = "old_text must occur exactly once; it occurs 0 times"
	if fw := cfg.forwarded(Synthesizer, "s", turns); len(fw) != 0 {
		t.Errorf("forwarded %+v, want the misquote answered in place", fw)
	}
}

// Past maxMisquotes, the client's own answer stands.
func TestMisquotesAreAnsweredInPlaceOnlySoOften(t *testing.T) {
	cfg, turns := quoteCase("1: "+dIt, api.ToolCall{})
	turns = turns[:1]
	for i := range maxMisquotes + 1 {
		turns = append(turns, api.Message{Role: "assistant", ToolCalls: []api.ToolCall{editCall(fmt.Sprint("e", i), strings.TrimSuffix(dIt, "} })};")+strings.Repeat("}", i+2), "x")}})
	}
	if fw := cfg.forwarded(Synthesizer, "s", turns); len(fw) != 1 {
		t.Errorf("forwarded %+v, want the change past the bound", fw)
	}
	if n := len(cfg.misquotes(Synthesizer, "s", turns)); n != maxMisquotes {
		t.Errorf("%d answered in place, want %d", n, maxMisquotes)
	}
}

// The member's own changes since its read are replayed onto it: a misquote
// after a change that went through is still answered in place, and a quote
// of what that change wrote goes out.
func TestTheReadFollowsTheMembersOwnChanges(t *testing.T) {
	read := "1: let a=1;\n2: " + dIt + "\n3: let b=2;"
	after := func(change api.ToolCall, result string, more ...api.ToolCall) (Config, []api.Message) {
		cfg, turns := quoteCase(read, change)
		mid := []api.Message{{Role: "assistant", ToolCalls: []api.ToolCall{editCall("w", "let b=2;", "let b=3; let cX=0;")}}}
		for i, m := range more {
			id := fmt.Sprint("m", i)
			m.ID = id
			mid = append(mid, api.Message{Role: "assistant", ToolCalls: []api.ToolCall{m}})
			cfg.Results[ForwardedID("s", id)] = result
		}
		cfg.Results[ForwardedID("s", "w")] = result
		return cfg, append(append(turns[:1:1], mid...), turns[1])
	}
	misq := strings.TrimSuffix(dIt, "} })};") + "}}})"
	cfg, turns := after(editCall("e", misq, "x"), "edited")
	if fw := cfg.forwarded(Synthesizer, "s", turns); len(fw) != 0 {
		t.Errorf("a misquote after the member's own change was forwarded: %+v", fw)
	}
	cfg, turns = after(editCall("e", "let a=1;\n"+strings.TrimSuffix(dIt, ";")+";\nlet b=3; let cX=0;", "x"), "edited")
	if fw := cfg.forwarded(Synthesizer, "s", turns); len(fw) != 1 {
		t.Errorf("a quote of what the member's change wrote was held: %+v", fw)
	}
	// A whole write is the text from then on.
	whole := api.NewToolCallFunctionArguments()
	whole.Set("path", "a.js")
	whole.Set("content", "let z=1;\n"+dIt)
	cfg, turns = after(editCall("e", misq, "x"), "edited", api.ToolCall{Function: api.ToolCallFunction{Name: "write_file", Arguments: whole}})
	if fw := cfg.forwarded(Synthesizer, "s", turns); len(fw) != 0 {
		t.Errorf("a misquote after a whole write was forwarded: %+v", fw)
	}
}
