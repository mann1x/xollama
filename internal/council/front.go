package council

// The synthesizer's front turn (plans/agentic-council-chat.md, 11.5; the
// owner's design 2026-09-28). On a turn with tools, the synthesizer -- the
// member that answers the user and the only one that changes anything --
// takes the request first, in place of the planner's route decision: it
// answers it itself, or hands it to the council with ForwardTool. Once the
// council is built (build.go) it is told what the council is set up for and
// judges whether the request continues that work; new work the target does
// not fit is sent through RebuildTool first, which runs the builder and
// answers with the new setup. One call answers a quick request, as a plain
// chat turn would, on the conversation's own session.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ollama/ollama/api"
)

// Front is the synthesizer taking a request first. It runs where the planner
// does, on the conversation's session.
const Front Role = "front"

// The front's own tools, answered by the council and never sent to the
// client.
const (
	ForwardTool = "council_forward"
	RebuildTool = "council_rebuild"
)

// RouteFront is a turn the front is still answering: it waits on the client.
const RouteFront = "front"

// WithRouting adds the front's tools to a tool turn's list. Every member's
// prompt carries the same list, so they share one prefix; only the front may
// call them.
func WithRouting(tools api.Tools) api.Tools {
	if len(tools) == 0 || slices.ContainsFunc(tools, func(t api.Tool) bool { return t.Function.Name == ForwardTool }) {
		return tools
	}
	props := api.NewToolPropertiesMap()
	props.Set("reason", api.ToolProperty{Type: api.PropertyType{"string"}, Description: "One sentence: why this needs the council."})
	rprops := api.NewToolPropertiesMap()
	rprops.Set("reason", api.ToolProperty{Type: api.PropertyType{"string"}, Description: "One sentence: what the new work is and why the current setup does not fit it."})
	return append(append(api.Tools{}, tools...),
		api.Tool{Type: "function", Function: api.ToolFunction{
			Name:        ForwardTool,
			Description: "Hand the user's latest request to the council: it researches, plans and proposes, and you then make and check the changes. Only the synthesizer may call it.",
			Parameters:  api.ToolFunctionParameters{Type: "object", Properties: props},
		}},
		api.Tool{Type: "function", Function: api.ToolFunction{
			Name:        RebuildTool,
			Description: "Set the council up again for new work its current target does not fit. You are told the new setup; then call council_forward. Only the synthesizer may call it.",
			Parameters:  api.ToolFunctionParameters{Type: "object", Properties: rprops},
		}},
	)
}

func routing(name string) bool { return name == ForwardTool || name == RebuildTool }

// fronted reports whether the front takes this turn's request.
func (cfg Config) fronted(m Model) bool {
	_, ok := m.(ToolModel)
	_, has := cfg.tool(ForwardTool)
	return ok && has
}

const frontMsg = "COUNCIL: You are the council's synthesizer, and you take the user's latest message above first. Answer it yourself only when you can already see the answer or the change to make: a reply, a question, a change whose place and content you know. When you would have to investigate first -- find a cause you cannot see yet, read and compare to find out what is wrong -- call council_forward now, before investigating: the council investigates in parallel and proposes, and you then make and check the changes."

// frontSteps bounds the front's own tool steps (J): past them it is told to
// forward, and two steps later the request is forwarded for it. Measured in
// ab-5: the front investigated for 11 trips before forwarding, and the
// theory it formed led the whole council.
const frontSteps = 3

var frontBudgetNote = fmt.Sprintf("You have taken %d tool steps yourself. Unless the answer is in hand now, call council_forward.", frontSteps)

const frontNoBuild = " The council has not been set up yet: the first request you forward sets it up for the work."

const frontJudge = " Judge whether the latest message continues that work or is new work. If it is new work that this does not fit, call council_rebuild before council_forward."

// frontRequest is the conversation and the front's instruction, then the
// client's system prompt as a direct answer reads it.
func frontRequest(cfg Config, conv []api.Message) []api.Message {
	s := frontMsg
	switch b := cfg.previousBuild(); {
	case cfg.mode() == api.CouncilModeAnswer:
		// The harness asked for the synthesizer alone: no hand-off exists.
		s = answerMsg
	case b != nil:
		s += " " + frontCue + " " + b.targetNote() + frontJudge
	default:
		s += " " + frontCue + frontNoBuild
	}
	if cfg.System != "" {
		s += "\n\n" + systemIntro + cfg.System
	}
	return append(clone(conv), user(cfg.withInstructions(sourcesNote+"\n\n"+s, Everyone, Synthesizer)))
}

// answerMsg is the front's instruction when the harness asked for an answer
// alone (api.CouncilModeAnswer): a plain agent turn.
const answerMsg = "COUNCIL: You are the council's synthesizer, and you answer the user's latest message above yourself, with the tools as you need them. The council is not convened for this request."

// rebuilt answers a RebuildTool call with the setup it produced.
func (cfg Config) rebuilt() string {
	b := cfg.build
	if b == nil || b.Target == "" {
		return "The council could not be set up again; it keeps its setup. Call council_forward to hand it the request."
	}
	var parts []string
	for _, r := range builtRoles {
		if s := b.Instructions[r]; s != "" {
			parts = append(parts, fmt.Sprintf("%s: %s", r, s))
		}
	}
	return fmt.Sprintf("The council is set up again for: %s\nIts instructions: %s\nYou, when it answers: %s\nNow call council_forward to hand it the request.",
		b.Target, strings.Join(parts, " | "), b.Instructions[Synthesizer])
}

// front runs the front turn from its turns so far. It returns the answer, or
// its turns while it waits on the client, or the route it chose -- the
// council, after rebuilding it when it asked.
func front(ctx context.Context, tm ToolModel, cfg Config, d Draws, conv []api.Message, emit Emit, turns []api.Message, rebuild func(context.Context) (*Build, error)) (string, []api.Message, string, error) {
	key := MemberKey(Front, 0, 0)
	own := frontRequest(cfg, conv)
	onToken := func(s string) { emit(Event{Role: Front, Kind: Content, Text: s}) }
	defer emit(Event{Role: Front, Kind: Content, Done: true})
	refusals, rebuilds, cuts := 0, 0, 0
	for {
		switch steps := toolSteps(turns); {
		case cfg.mode() == api.CouncilModeAnswer:
			// Nothing to hand off to: the client bounds the steps.
		case steps >= frontSteps+2:
			return "", turns, RouteCouncil, nil
		case steps >= frontSteps && !noted(turns, frontBudgetNote):
			turns = append(slices.Clone(turns), user(frontBudgetNote))
		}
		rep, err := tm.StreamTools(ctx, Request{
			Role: Front, Messages: append(clone(own), cfg.transcript(Front, key, turns)...),
			Seed: d.Direct.Seed, Temperature: d.Direct.Temperature, MaxTokens: writeTok(Front, maxTok(cfg, Synthesizer, "", "")), Think: cfg.Think[Synthesizer],
		}, onToken)
		if err != nil {
			return "", nil, "", err
		}
		if wasCut(Front, rep) && cuts < maxCuts {
			cuts++
			turns = append(slices.Clone(turns), cutTurn())
			onToken("\n\n(cut at the reply limit: asked again for a smaller change)\n\n")
			continue
		}
		cuts = 0
		if len(rep.Calls) == 0 {
			return replyText(turns, rep.Content), nil, "", nil
		}
		calls := named(rep.Calls, len(turns))
		turns = append(slices.Clone(turns), api.Message{Role: "assistant", Content: rep.Content, ToolCalls: calls})
		if slices.ContainsFunc(calls, func(c api.ToolCall) bool { return c.Function.Name == ForwardTool }) {
			return "", turns, RouteCouncil, nil
		}
		if slices.ContainsFunc(calls, func(c api.ToolCall) bool { return c.Function.Name == RebuildTool }) && rebuilds == 0 {
			rebuilds++
			b, err := rebuild(ctx)
			if err != nil {
				return "", nil, "", err
			}
			cfg.build = b
			continue
		}
		if len(cfg.forwarded(Front, key, turns)) > 0 {
			return "", turns, "", nil
		}
		if refusals++; refusals > maxRefusals {
			return replyText(turns, ""), nil, "", nil
		}
	}
}

// frontReport is what the front tried before it forwarded, as a failed check
// for the council (F), or "" when it called nothing the client ran. The
// council reads it as an attempt that did not settle the request, not as the
// conversation's word.
func (cfg Config) frontReport(turns []api.Message) string {
	if toolSteps(turns) == 0 {
		return ""
	}
	// Only the calls and what they returned: the front's own conclusions
	// stay out, so its first theory does not lead the council (ab-5 rerun:
	// the whole first cycle chased the front's guess).
	// Only when it changed something: reads alone are no attempt, and
	// framed as a failed check they sent the researchers to read again
	// (frontRead carries them instead).
	first, _ := cfg.changes(turns)
	if first < 0 {
		return ""
	}
	s := "The synthesizer worked on the request itself before forwarding it, and it was not settled. What it changed and checked, and what came back:"
	return truncate(s+cfg.evidenceOf(Front, MemberKey(Front, 0, 0), turns, func(at int, _ api.ToolCall) bool { return at >= first }), maxPriorChars)
}

// frontRead is what the front read that nothing changed since: research the
// council starts from, instead of reading it again (the consultants' #4:
// the front's reads were redone by the researchers, a trip each). A read
// before one of the front's own changes is not in it: it may be stale.
func (cfg Config) frontRead(turns []api.Message) string {
	_, last := cfg.changes(turns)
	ev := cfg.evidenceOf(Front, MemberKey(Front, 0, 0), turns, func(at int, c api.ToolCall) bool { return at > last && cfg.readOnly(c) })
	if ev == "" {
		return ""
	}
	return truncate(frontReadIntro+ev, maxPriorChars)
}

// frontReadIntro opens the front's reads for the council.
const frontReadIntro = "The synthesizer read these before forwarding the request, and nothing has changed them since. Start from them: where a result is shown by its ref, read it back with council_evidence rather than calling the tool again."

// frontReadSource heads the front's reads.
const frontReadSource = "WHAT THE SYNTHESIZER ALREADY READ"

// changes is the index, in the front's transcript, of its first and its last
// message that changed something, or -1, -1.
func (cfg Config) changes(turns []api.Message) (first, last int) {
	first, last = -1, -1
	for at, m := range cfg.transcript(Front, MemberKey(Front, 0, 0), turns) {
		if m.Role == "assistant" && slices.ContainsFunc(m.ToolCalls, func(c api.ToolCall) bool { return !local(c) && !cfg.readOnly(c) }) {
			if first < 0 {
				first = at
			}
			last = at
		}
	}
	return first, last
}

// withRead puts the front's reads after the conversation, for every member.
func withRead(conv []api.Message, read string) []api.Message {
	if read == "" {
		return conv
	}
	return append(clone(conv), sourced(frontReadSource, read))
}
