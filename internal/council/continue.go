package council

// One council across turns (plans/agentic-council-chat.md, 10.5; the owner's
// ruling 2026-09-27). The council behind a chat stays alive: a turn that ends
// with the council's answer keeps its deliberation, and the next user message
// is feedback to that council. The planner decides what it asks for:
//
//   - direct: the work is done, or the message is trivial -- answer it;
//   - continue: the work goes on from where it stands -- the synthesizer takes
//     the message with the plan, findings and critiques it answered from;
//   - council: it needs the council again -- a fresh deliberation.
//
// In ab-3 each "it is still not working, continue" started a new council from
// the route, twice; continue gives that message to the member that was doing
// the work. Nothing here is specific to code: the request's system and user
// messages say what the work is.

import (
	"errors"
	"fmt"
	"strings"
)

// errNothingToContinue is a continue route with no deliberation to continue;
// Decide never returns one, so it is a caller's bug.
var errNothingToContinue = errors.New("council: continue with no previous deliberation")

// RouteContinue is the planner's third route, offered only when the turn
// before left a deliberation (Config.Previous).
const RouteContinue = "continue"

const routeMsgContinue = `ROLE: PLANNER. The council answered the user's previous message; the user's latest message replies to that answer. Decide what it asks for. Reply with JSON only: {"route":"direct"} when the work is done or the message is trivial (thanks, a greeting, a one-line fact), {"route":"continue"} when the work the council was doing goes on from where it stands (feedback, a correction, "continue", a result to act on), or {"route":"council"} when it is a new or different task that needs the council again.`

// continueNote tells the synthesizer the turn goes on from its last answer.
// The record above it answers the earlier message, so the note quotes the
// reply and says so: a weak synthesizer given only the record repeats the
// earlier answer to a reply that changed a value (the council gate's leak of
// 20 litres answered with the 10 litres' 26 minutes 40 seconds, 2026-10-09).
const continueNote = " The conversation above goes on after the council's last answer, and the user's latest message replies to it%s. The plan, findings and critiques above were written for the earlier message. Act on the reply: where it changes the task (a new value, a correction, another case), redo the work it touches with the change, never repeating the earlier answer; where it asks to go on, continue from where the work stands; if it only confirms the work is complete, say so plainly."

// maxQuotedReply bounds the reply the continue note quotes.
const maxQuotedReply = 600

// Kept is the deliberation a finished turn leaves for the next one to
// continue: its plan and its last round. Nil for a direct answer.
func Kept(p Progress) *Progress {
	if p.Plan == nil || len(p.Rounds) == 0 {
		return nil
	}
	last := p.Rounds[len(p.Rounds)-1]
	k := Progress{Route: RouteCouncil, Plan: p.Plan, Rounds: []RoundProgress{last}, Build: p.Build, Prior: lastPrior(append(append([]string(nil), p.Prior...), p.Tests...)), Tasks: p.Tasks}
	k = k.clone()
	return &k
}

func continuedNote(cfg Config) string {
	if !cfg.continuing {
		return ""
	}
	quoted := ""
	if r := strings.TrimSpace(cfg.request); r != "" {
		if len(r) > maxQuotedReply {
			r = strings.ToValidUTF8(r[:maxQuotedReply], "") + " …"
		}
		quoted = ": «" + r + "»"
	}
	return fmt.Sprintf(continueNote, quoted)
}

// RouteCouncil is the full deliberation's route.
const RouteCouncil = "council"

// continuing is RunFrom's continue route: the plan and last round of the
// deliberation it continues, taken into this turn's progress so a suspended
// synthesizer resumes from its own record.
func continuing(prev *Progress) (Plan, RoundProgress, bool) {
	if prev == nil || prev.Plan == nil || len(prev.Rounds) == 0 {
		return Plan{}, RoundProgress{}, false
	}
	return *prev.Plan, prev.Rounds[len(prev.Rounds)-1], true
}
