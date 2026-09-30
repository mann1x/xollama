package council

// A member's result is a typed record, not a marker in its prose
// (plans/agentic-council-chat.md 11.29). A researcher ends by calling
// ReportTool, a critic VerdictTool, a synthesizer that checks DoneTool or
// RetestTool: the council answers them itself, and their arguments are the
// result. The runner then routes on fields -- what LangChain's structured
// output and LangGraph's typed state give -- instead of on "VERDICT:" strings
// the model had to remember to write, with a nudge each time one was missing.
//
// A record is rendered back into the text the rest of the council reads, so
// the flow (findings, critiques, the synthesizer's verdict, the task list,
// the resume state) keeps one form; what changes is that the model fills a
// schema. At the step bound the same schema is sent as the call's format, so
// the grammar admits no tool call while the tool list, and with it the shared
// prefix, stays as it was.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ollama/ollama/api"
)

const (
	ReportTool  = "council_report"
	VerdictTool = "council_verdict"
	DoneTool    = "council_done"
	RetestTool  = "council_retest"
)

// Bounds on a report, as its schema states them: what the critics read of a
// researcher is these, not its transcript (a critic of 20260930-085958 read
// 44k tokens a call).
const (
	maxProposals  = 8
	maxClaims     = 8
	maxReportText = 2000
)

// Proposal is one change a researcher proposes: the text to replace, as its
// read showed it, and the text to put there.
type Proposal struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
	Why     string `json:"why"`
	Check   string `json:"check"`
}

// Claim is a finding and where it shows.
type Claim struct {
	Claim    string `json:"claim"`
	Evidence string `json:"evidence"`
}

// Report is a researcher's result.
type Report struct {
	Summary   string     `json:"summary"`
	Proposals []Proposal `json:"proposals"`
	Claims    []Claim    `json:"claims"`
	Open      string     `json:"open"`
}

// Verdict is a critic's result: ready (the findings stand, perhaps with
// corrections), revise (not good enough to answer from) or confirmed (an
// error checked at its place, so the change starts at once).
type Verdict struct {
	Verdict     string `json:"verdict"`
	Place       string `json:"place"`
	Why         string `json:"why"`
	Corrections string `json:"corrections"`
}

func strProp(desc string) api.ToolProperty {
	return api.ToolProperty{Type: api.PropertyType{"string"}, Description: desc}
}

var proposalSchema = `{"type":"object","properties":{"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"},"why":{"type":"string"},"check":{"type":"string"}},"required":["path","old_text","new_text","why"]}`

// reportSchema is ReportTool's arguments, and the format of a researcher's
// forced answer.
var reportSchema = json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{`+
	`"summary":{"type":"string","maxLength":%d},`+
	`"proposals":{"type":"array","maxItems":%d,"items":%s},`+
	`"claims":{"type":"array","maxItems":%d,"items":{"type":"object","properties":{"claim":{"type":"string"},"evidence":{"type":"string"}},"required":["claim","evidence"]}},`+
	`"open":{"type":"string"}},"required":["summary","proposals","claims"]}`, maxReportText, maxProposals, proposalSchema, maxClaims))

// verdictSchema is VerdictTool's arguments, and a critic's forced answer.
var verdictSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"verdict":{"enum":["ready","revise","confirmed"]},"place":{"type":"string"},"why":{"type":"string"},"corrections":{"type":"string"}},` +
	`"required":["verdict","why"]}`)

// WithReports adds the result tools to the members' tools. Every member
// carries all of them, so the tool list -- rendered in the shared prefix -- is
// one list; may lets each role call only its own.
func WithReports(tools api.Tools) api.Tools {
	if len(tools) == 0 || slices.ContainsFunc(tools, func(t api.Tool) bool { return t.Function.Name == ReportTool }) {
		return tools
	}
	arr := func(desc string) api.ToolProperty {
		return api.ToolProperty{Type: api.PropertyType{"array"}, Description: desc}
	}
	report := api.NewToolPropertiesMap()
	report.Set("summary", strProp("What you found, in a few sentences."))
	report.Set("proposals", arr(fmt.Sprintf("Every change you propose, at most %d: each an object with path, old_text (the exact text to replace, copied from what you read), new_text, why, and check (how to tell it worked).", maxProposals)))
	report.Set("claims", arr(fmt.Sprintf("What you established, at most %d: each an object with claim and evidence (the path:line or the call that shows it).", maxClaims)))
	report.Set("open", strProp("The one question you could not settle, if any."))
	verdict := api.NewToolPropertiesMap()
	verdict.Set("verdict", api.ToolProperty{Type: api.PropertyType{"string"}, Enum: []any{"ready", "revise", "confirmed"}, Description: "ready: the findings are good enough to act on, corrections included. revise: they are not, and the researchers must look again. confirmed: you checked an error at its exact place."})
	verdict.Set("place", strProp("For confirmed: the place, as path:line."))
	verdict.Set("why", strProp("Why, in a few sentences."))
	verdict.Set("corrections", strProp("What the findings got wrong, if anything."))
	done := api.NewToolPropertiesMap()
	done.Set("summary", strProp("What was changed and what the check showed."))
	retest := api.NewToolPropertiesMap()
	retest.Set("tried", strProp("What you changed and what each check returned."))
	t := func(name, desc string, req []string, p *api.ToolPropertiesMap) api.Tool {
		return api.Tool{Type: "function", Function: api.ToolFunction{
			Name: name, Description: desc, ReadOnly: true,
			Parameters: api.ToolFunctionParameters{Type: "object", Required: req, Properties: p},
		}}
	}
	return append(append(api.Tools{}, tools...),
		t(ReportTool, "A researcher's report to the council: call it once, when your reads are done, instead of writing the report as text. Changes nothing.", []string{"summary", "proposals", "claims"}, report),
		t(VerdictTool, "A critic's verdict on the findings: call it once, instead of writing the verdict as text. Changes nothing.", []string{"verdict", "why"}, verdict),
		t(DoneTool, "The synthesizer's verdict that the check passed: call it after the check, once your answer to the user is written. Changes nothing.", []string{"summary"}, done),
		t(RetestTool, "The synthesizer's verdict that a check failed and the council should try again: call it with what you tried. Changes nothing.", []string{"tried"}, retest),
	)
}

// resultTool reports whether name is one of the result tools.
func resultTool(name string) bool {
	return name == ReportTool || name == VerdictTool || name == DoneTool || name == RetestTool
}

// ownResult is the result tool role r ends with, or "".
func (cfg Config) ownResult(r Role, round int) []string {
	switch r {
	case Researcher:
		return []string{ReportTool}
	case Critic:
		return []string{VerdictTool}
	case Synthesizer:
		if cfg.testing(round) {
			return []string{DoneTool, RetestTool}
		}
	}
	return nil
}

// resultSchema is the format of role r's forced answer, or nil.
func resultSchema(r Role) json.RawMessage {
	switch r {
	case Researcher:
		return reportSchema
	case Critic:
		return verdictSchema
	}
	return nil
}

// decode reads a result's arguments, from a call or a formatted reply.
func decode[T any](raw string) (T, bool) {
	var v T
	err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v)
	return v, err == nil
}

func argsJSON(c api.ToolCall) string { return c.Function.Arguments.String() }

// renderReport is a researcher's report as the council reads it. Each
// proposal says whether its old text is in what the researcher read: a quote
// the reads bear out can be applied as it is (a citation check at the
// source, claude-hooks P5).
func (cfg Config) renderReport(key string, turns []api.Message, rp Report) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(rp.Summary))
	if len(rp.Proposals) > 0 {
		b.WriteString("\n\nProposals:")
		for i, p := range rp.Proposals[:min(len(rp.Proposals), maxProposals)] {
			seen := "its reads show this text"
			if !cfg.readShows(key, turns, p.OldText) {
				seen = "NOT in its reads: check the text before changing it"
			}
			fmt.Fprintf(&b, "\n%d. %s (%s)\n   replace:\n%s\n   with:\n%s\n   why: %s", i+1, p.Path, seen, indent(p.OldText), indent(p.NewText), strings.TrimSpace(p.Why))
			if s := strings.TrimSpace(p.Check); s != "" {
				fmt.Fprintf(&b, "\n   check: %s", s)
			}
		}
	}
	if len(rp.Claims) > 0 {
		b.WriteString("\n\nClaims:")
		for _, c := range rp.Claims[:min(len(rp.Claims), maxClaims)] {
			fmt.Fprintf(&b, "\n- %s (%s)", strings.TrimSpace(c.Claim), strings.TrimSpace(c.Evidence))
		}
	}
	if s := strings.TrimSpace(rp.Open); s != "" {
		b.WriteString("\n\nOpen: " + s)
	}
	return b.String()
}

// readShows reports whether text is in one of the member's tool results.
func (cfg Config) readShows(key string, turns []api.Message, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			if res, _, ok := cfg.result(key, c); ok && strings.Contains(res, text) {
				return true
			}
		}
	}
	return false
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "      " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// renderVerdict is a critic's verdict in the words the council routes on.
func renderVerdict(v Verdict) string {
	out := strings.TrimSpace(v.Why)
	if s := strings.TrimSpace(v.Corrections); s != "" {
		out += "\n\nCorrections: " + s
	}
	switch v.Verdict {
	case "revise":
		out += "\n\n" + Revise
	case "confirmed":
		out += "\n\n" + Confirmed + " " + strings.TrimSpace(v.Place)
	}
	return out
}

// evidenceRefs is the evidence a typed report carries: each call it made and
// where to read the result, not the result itself.
func (cfg Config) evidenceRefs(r Role, key string, turns []api.Message) string {
	var b strings.Builder
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			res, ref, ok := cfg.result(key, c)
			if ok2, _ := cfg.may(r, c); !ok || !ok2 || local(c) {
				continue
			}
			if b.Len() == 0 {
				fmt.Fprintf(&b, "\n\nEvidence (read any of it with %s):", EvidenceTool)
			}
			fmt.Fprintf(&b, "\n- %s %s: ref %s, %d lines", c.Function.Name, c.Function.Arguments.String(), ref, strings.Count(res, "\n")+1)
		}
	}
	return b.String()
}

// resultLater answers a result call made beside other calls.
const resultLater = "Not taken yet: call it on its own, once the other calls' results are in."

// resultRole is the role a result tool belongs to.
func resultRole(name string) Role {
	switch name {
	case ReportTool:
		return Researcher
	case VerdictTool:
		return Critic
	}
	return Synthesizer
}

// canReport reports whether the members carry the result tools this turn.
func (cfg Config) canReport() bool {
	_, ok := cfg.tool(ReportTool)
	return ok
}

// takeResult turns a member's result call into the reply the council routes
// on. A result counts only as the turn's one call: beside reads or changes it
// waits for their results (resultLater), so a report is never written before
// the reads it rests on. It reports whether rep is now a typed result.
func (cfg Config) takeResult(r Role, round int, key string, turns []api.Message, rep *Reply) bool {
	own := cfg.ownResult(r, round)
	var res *api.ToolCall
	for i, c := range rep.Calls {
		switch {
		case slices.Contains(own, c.Function.Name):
			if res == nil {
				res = &rep.Calls[i]
			}
		case !local(c):
			return false
		}
	}
	if res == nil {
		return false
	}
	c := *res
	text := strings.TrimSpace(rep.Content)
	switch c.Function.Name {
	case ReportTool:
		rp, ok := decode[Report](argsJSON(c))
		if !ok {
			return false
		}
		rep.Content = cfg.renderReport(key, turns, rp)
	case VerdictTool:
		v, ok := decode[Verdict](argsJSON(c))
		if !ok {
			return false
		}
		rep.Content = renderVerdict(v)
	case DoneTool:
		rep.Content = strings.TrimSpace(text + "\n\n" + Done)
	case RetestTool:
		rep.Content = strings.TrimSpace(text + "\n\n" + Retest + " " + stringArg(c, "tried"))
	}
	rep.Calls = nil
	return true
}

// formatted reads a forced answer: the record, rendered, with its evidence
// by ref; ok is false when the reply is not one.
func (cfg Config) formatted(r Role, key string, turns []api.Message, content string) (string, bool) {
	switch r {
	case Researcher:
		if rp, ok := decode[Report](content); ok && strings.TrimSpace(rp.Summary) != "" {
			return cfg.renderReport(key, turns, rp) + cfg.evidenceRefs(r, key, turns), true
		}
	case Critic:
		if v, ok := decode[Verdict](content); ok && v.Verdict != "" {
			return renderVerdict(v) + cfg.evidenceRefs(r, key, turns), true
		}
	}
	return "", false
}
