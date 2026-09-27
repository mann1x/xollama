package council

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ollama/ollama/api"
)

// EvidenceTool is the council's own tool: it reads back a tool result a
// member's findings list by ref instead of carrying it. It is answered in the
// server and never reaches the client.
const EvidenceTool = "council_evidence"

const (
	// inlineEvidence is the largest result a reply carries whole; a longer
	// one is listed by ref with a preview, to be fetched with EvidenceTool.
	inlineEvidence = 1500
	// previewLines and previewChars bound that preview.
	previewLines = 10
	previewChars = 800
	// maxFetch bounds one EvidenceTool answer, in lines and characters.
	maxFetchLines = 200
	maxFetchChars = 8000
	// maxLookups bounds the member's turns that call only EvidenceTool.
	maxLookups = 6
	// ownBudget bounds the tool results a member carries whole in its own
	// transcript, when the Config names no ResultBudget. Measured live on b137 without it: a researcher that read a
	// 20 KB log, then an 8 KB file, carried both into its next request,
	// needed 15,398 cells of a 15,229-cell window and never ran.
	ownBudget = 12000
)

// WithEvidence is the tool list every member carries on a turn with tools:
// the client's, and EvidenceTool after them. The list is the same for every
// member, so the shared prefix -- and a PolyKV root -- holds it once. A
// client that names a tool EvidenceTool keeps its own, and loses the lookup.
func WithEvidence(tools api.Tools) api.Tools {
	if len(tools) == 0 {
		return tools
	}
	for _, t := range tools {
		if t.Function.Name == EvidenceTool {
			return tools
		}
	}
	props := api.NewToolPropertiesMap()
	props.Set("ref", api.ToolProperty{Type: api.PropertyType{"string"}, Description: "The ref of a tool result, exactly as the findings list it."})
	props.Set("lines", api.ToolProperty{Type: api.PropertyType{"string"}, Description: "A line range, e.g. 120-180. Lines are numbered from 1."})
	props.Set("pattern", api.ToolProperty{Type: api.PropertyType{"string"}, Description: "A regular expression; returns each matching line with its number."})
	t := api.Tool{Type: "function", Function: api.ToolFunction{
		Name:        EvidenceTool,
		Description: "Read part of a tool result another council member received, by its ref: the lines in a range, or the lines matching a pattern. Changes nothing.",
		Parameters:  api.ToolFunctionParameters{Type: "object", Required: []string{"ref"}, Properties: props},
		ReadOnly:    true,
	}}
	return append(append(api.Tools{}, tools...), t)
}

// local reports whether a call is answered by the council itself.
func local(c api.ToolCall) bool { return c.Function.Name == EvidenceTool }

func stringArg(c api.ToolCall, key string) string {
	v, ok := c.Function.Arguments.Get(key)
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// lookup answers an EvidenceTool call from this turn's results.
func (cfg Config) lookup(c api.ToolCall) string {
	ref := stringArg(c, "ref")
	res, ok := cfg.Results[ref]
	if !ok {
		return fmt.Sprintf("No evidence has the ref %q; use a ref exactly as the findings list it.", ref)
	}
	lines := strings.Split(res, "\n")
	if p := stringArg(c, "pattern"); p != "" {
		re, err := regexp.Compile(p)
		if err != nil {
			re = regexp.MustCompile(regexp.QuoteMeta(p))
		}
		var b strings.Builder
		n := 0
		for i, l := range lines {
			if !re.MatchString(l) {
				continue
			}
			if n++; n > maxFetchLines || b.Len()+len(l) > maxFetchChars {
				fmt.Fprintf(&b, "[... more matches; narrow the pattern or ask for lines]\n")
				break
			}
			fmt.Fprintf(&b, "%d: %s\n", i+1, l)
		}
		if n == 0 {
			return fmt.Sprintf("No line of %s matches %q (%d lines).", ref, p, len(lines))
		}
		return b.String()
	}
	from, to := 1, len(lines)
	if r := stringArg(c, "lines"); r != "" {
		a, z, _ := strings.Cut(r, "-")
		if v, err := strconv.Atoi(strings.TrimSpace(a)); err == nil {
			from, to = v, v
		}
		if v, err := strconv.Atoi(strings.TrimSpace(z)); err == nil {
			to = v
		}
	}
	from, to = max(from, 1), min(to, len(lines))
	if from > to {
		return fmt.Sprintf("%s has %d lines; ask for a range within them.", ref, len(lines))
	}
	to = min(to, from+maxFetchLines-1)
	body := strings.Join(lines[from-1:to], "\n")
	if len(body) > maxFetchChars {
		body = truncate(body, maxFetchChars)
		to = from + strings.Count(body, "\n")
	}
	return fmt.Sprintf("Lines %d-%d of %d in %s:\n%s", from, to, len(lines), ref, body)
}

// indexed is how a reply lists a result too long to carry: its ref, size and
// first lines.
func indexed(ref string, res string) string {
	lines := strings.Split(res, "\n")
	head := strings.Join(lines[:min(len(lines), previewLines)], "\n")
	head = truncate(head, previewChars)
	return fmt.Sprintf("[ref %s: %d characters, %d lines -- read more with %s]\n%s\n[...]", ref, len(res), len(lines), EvidenceTool, head)
}

// canLookup reports whether the members carry EvidenceTool this turn.
func (cfg Config) canLookup() bool {
	_, ok := cfg.tool(EvidenceTool)
	return ok
}

// lookupNote tells a member -- whose own results may arrive by ref too --
// how to read a listed
// result: before judging a claim that rests on it, or editing what it shows.
func (cfg Config) lookupNote(r Role) string {
	if !cfg.canLookup() {
		return ""
	}
	return fmt.Sprintf(" A result too long to carry is listed by its ref with its first lines; call %s with the ref and a line range or pattern to read the exact lines before you judge or change what they say.", EvidenceTool)
}

// truncate cuts s to at most n bytes, on a character boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// folded is which of a member's own results it carries by ref, not whole,
// so they fit its budget (resultBudget): its earlier turns first, oldest
// first -- an earlier look-up is dropped, to be asked again -- and then, if
// its last turn alone is too much, that turn's largest results, whose preview
// and ref it searches with EvidenceTool. Measured live on b137: a researcher
// that read three files in one turn needed 15,264 cells where at most 15,235
// were ever free, and never ran.
func (cfg Config) folded(r Role, key string, turns []api.Message) map[string]bool {
	if !cfg.canLookup() || len(turns) == 0 {
		return nil
	}
	budget := cfg.resultBudget()
	type res struct {
		id   string
		size int // what folding it saves
	}
	var older, last []res
	size := 0
	for i, t := range turns {
		for _, c := range t.ToolCalls {
			if ok, _ := cfg.may(r, c); !ok {
				continue
			}
			var n, saved int
			if local(c) {
				n = len(cfg.lookup(c))
				saved = n - len(droppedLookup)
			} else {
				ref := ForwardedID(key, c.ID)
				s, ok := cfg.Results[ref]
				if !ok {
					continue
				}
				n = len(s)
				if n > inlineEvidence {
					saved = n - len(indexed(ref, s))
				}
			}
			size += n
			if saved <= 0 {
				continue
			}
			switch {
			case i < len(turns)-1:
				older = append(older, res{c.ID, saved})
			case !local(c):
				// What the member just looked up stays: it asked for it.
				last = append(last, res{c.ID, saved})
			}
		}
	}
	slices.SortStableFunc(last, func(a, b res) int { return b.size - a.size })
	out := map[string]bool{}
	for _, f := range append(older, last...) {
		if size <= budget {
			break
		}
		out[f.id] = true
		size -= f.size
	}
	return out
}

// droppedLookup stands in for an earlier look-up a member no longer carries.
const droppedLookup = "(an earlier look-up, dropped to keep your context small; call " + EvidenceTool + " again for these lines if you need them)"

// resultBudget is the characters of tool results a member carries whole:
// the Config's, or ownBudget.
func (cfg Config) resultBudget() int {
	if cfg.ResultBudget > 0 {
		return cfg.ResultBudget
	}
	return ownBudget
}
