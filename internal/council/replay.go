package council

// A member's reasoning between its own steps (plans/agentic-council-chat.md
// 11.29), ported from Cerebriline:
//
//   - Replay "last" (sdk/packages/llms/src/providers/reasoning-history.ts,
//     autoReasoningHistoryMode): the member's latest step carries the
//     reasoning it followed, so its next step starts from it instead of
//     re-deriving it; older steps carry none. Before this a member saw only
//     its content and calls.
//   - Capped-thinking condensation (sdk/packages/core/src/extensions/context/
//     capped-thinking.ts): reasoning that ended on the server's budget message
//     is not something a model continues from, it is something it re-derives
//     -- Cerebriline watched one reach "final plan... wait" a dozen times. So
//     once the step's results are in, that reasoning is replaced by a short
//     note in the member's own voice: what it settled, what it ruled out,
//     what its calls returned.

import (
	"context"
	"regexp"
	"strings"

	"github.com/ollama/ollama/api"
)

// Condenser writes the note that replaces a member's capped reasoning.
const Condenser Role = "condenser"

const (
	// condensedNoteTokens is the note's cap (CONDENSED_THINKING_NOTE_TOKENS).
	condensedNoteTokens = 2000
	// condensedMaxChars guards a runaway note (CONDENSED_THINKING_MAX_CHARS).
	condensedMaxChars = 12000
	// budgetTailChars is how far from the end the budget message may be
	// (BUDGET_MESSAGE_TAIL_MARGIN_CHARS): reasoning that only discusses its
	// budget mid-thought has not run out of it.
	budgetTailChars = 400
)

// condensedLeadIn opens a condensed note (CONDENSED_THINKING_LEAD_IN), and
// marks reasoning already condensed.
const condensedLeadIn = "I have already reasoned this far on this problem, up to the point where I ran out of thinking budget. Picking up from here rather than starting again:"

// condenserSystem and condenserPrompt are Cerebriline's
// CAPPED_THINKING_SYSTEM_PROMPT and DEFAULT_CAPPED_THINKING_PROMPT.
const condenserSystem = "You are compressing a train of thought, in the voice of the person who was thinking it. What you write goes back into that same reasoning as its own continuation -- not as a report about it -- so write it the way thinking is written: first person, present tense, plain sentences, no headings and no lists. Keep every specific: paths, symbols, line numbers, error text, and above all what was already ruled out and why."

const condenserPrompt = `You were reasoning about a problem and ran out of thinking budget mid-thought. Below is how far you got, and what the tool call you managed to make returned.

Rewrite that reasoning, compressed, as the thinking it is. It is going back into your own reasoning channel as the part you have already done, so that your next pass starts from here instead of starting over and arriving at this same point again.

Write it as thought, not as a report about thought:
- First person, present tense, the way you were already thinking. No headings, no bullet lists, no preamble, no sign-off. Do not label it or announce what it is; just think.
- Carry what you called and what it returned. Every tool call in this turn, by name, with the argument that mattered and what came back -- the file you read and what was at the line you were looking for, the edit you attempted and whether it applied, the command you ran and what it printed. A retrospective that omits this is the one thing the next pass cannot recover, because the results are not in front of it any more.
- State what you have settled as settled. Do not re-derive it and do not hedge it.
- Say what you ruled out and why, in a clause each. This is the part that stops the next pass repeating this one, and it is the part that gets dropped first if you are careless.
- Long enough that none of the above is missing, short enough that none of it is padding. Length follows the content: a turn that made four calls and settled three questions is not a paragraph. If you find yourself restating a line you have already written, you are finished: stop.
- End on the one question still open and the single next action, stated concretely.

Two things to keep out of it:
- Do not copy file contents. Name the file and the line and say what you concluded about it. Quoted code goes stale the moment the file is edited, and reasoning that argues with the next tool result costs more than it saved.
- Do not narrate the interruption. It is not part of the problem.`

var spaces = regexp.MustCompile(`\s+`)

func collapse(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// hitBudget reports whether reasoning ended on the budget message: its
// longest line, whitespace collapsed on both sides, in the tail
// (reasoningHitBudget). Without a known message nothing counts.
func hitBudget(thinking, message string) bool {
	marker := ""
	for _, line := range strings.Split(message, "\n") {
		if c := collapse(line); len(c) > len(marker) {
			marker = c
		}
	}
	if marker == "" || thinking == "" {
		return false
	}
	tail := thinking[max(0, len(thinking)-len(marker)-budgetTailChars):]
	return strings.Contains(collapse(tail), marker)
}

// withReasoning appends a member's step, carrying the reasoning it followed,
// and drops the reasoning of its earlier steps ("last").
func withReasoning(turns []api.Message, step api.Message) []api.Message {
	out := make([]api.Message, 0, len(turns)+1)
	for _, t := range turns {
		if t.Role == "assistant" && t.Thinking != "" && step.Thinking != "" {
			t.Thinking = ""
		}
		out = append(out, t)
	}
	return append(out, step)
}

// condense replaces the capped reasoning of the member's last step, once its
// results are in, with a note (Condenser). It returns the turns unchanged
// when there is nothing to condense or the note is unusable.
func (cfg Config) condense(ctx context.Context, tm ToolModel, key string, turns []api.Message) []api.Message {
	at := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "assistant" && len(turns[i].ToolCalls) > 0 {
			at = i
			break
		}
	}
	if at < 0 {
		return turns
	}
	step := turns[at]
	if strings.HasPrefix(step.Thinking, condensedLeadIn) || !hitBudget(step.Thinking, cfg.BudgetMessage) {
		return turns
	}
	var got strings.Builder
	for _, c := range step.ToolCalls {
		res, _, ok := cfg.result(key, c)
		if !ok && !local(c) {
			return turns // not every result is in yet
		}
		got.WriteString("\n- " + c.Function.Name + " " + c.Function.Arguments.String() + " returned:\n" + truncate(res, 2000))
	}
	msgs := []api.Message{
		{Role: "system", Content: condenserSystem},
		{Role: "user", Content: condenserPrompt + "\n\nHow far you got:\n\n" + truncate(step.Thinking, condensedMaxChars) + "\n\nWhat your calls returned:" + got.String()},
	}
	rep, err := tm.StreamTools(ctx, Request{Role: Condenser, Messages: msgs, MaxTokens: condensedNoteTokens, Temperature: cfg.Temperature}, func(string) {})
	note := strings.TrimSpace(rep.Content)
	if err != nil || note == "" || len(note) > condensedMaxChars {
		return turns
	}
	out := clone(turns)
	out[at].Thinking = condensedLeadIn + "\n\n" + note
	return out
}
