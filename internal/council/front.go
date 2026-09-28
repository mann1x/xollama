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

const frontMsg = "COUNCIL: You are the council's synthesizer, and you answer the user's latest message above. Answer it yourself, with the tools, when that is quick: a reply, a question, a small change. Call council_forward when it needs investigation or several steps of work: the council researches, plans and proposes, and you then make and check the changes."

const frontNoBuild = " The council has not been set up yet: the first request you forward sets it up for the work."

const frontJudge = " Judge whether the latest message continues that work or is new work. If it is new work that this does not fit, call council_rebuild before council_forward."

// frontRequest is the conversation and the front's instruction, then the
// client's system prompt as a direct answer reads it.
func frontRequest(cfg Config, conv []api.Message) []api.Message {
	s := frontMsg
	if b := cfg.previousBuild(); b != nil {
		s += " " + b.targetNote() + frontJudge
	} else {
		s += frontNoBuild
	}
	if cfg.System != "" {
		s += "\n\n" + systemIntro + cfg.System
	}
	return append(clone(conv), user(s))
}

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
	refusals, rebuilds := 0, 0
	for {
		rep, err := tm.StreamTools(ctx, Request{
			Role: Front, Messages: append(clone(own), cfg.transcript(Front, key, turns)...),
			Seed: d.Direct.Seed, Temperature: d.Direct.Temperature, MaxTokens: maxTok(cfg, Synthesizer), Think: cfg.Think[Synthesizer],
		}, onToken)
		if err != nil {
			return "", nil, "", err
		}
		if len(rep.Calls) == 0 {
			return replyText(turns, rep.Content), nil, "", nil
		}
		calls := named(rep.Calls, len(turns))
		turns = append(slices.Clone(turns), api.Message{Role: "assistant", Content: rep.Content, ToolCalls: calls})
		if slices.ContainsFunc(calls, func(c api.ToolCall) bool { return c.Function.Name == ForwardTool }) {
			return "", nil, RouteCouncil, nil
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
