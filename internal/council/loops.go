package council

// Loop guards, ported from Cerebriline (sdk/packages/core/src/runtime/safety/
// loop-detection.ts and extensions/tools/executors/editor.ts), where they are
// proven (the owner, 2026-09-28). The council cannot see the client's
// file, so it keeps the two parts that need none: a call that changes nothing
// is refused in place, and a call that changes something, sent again
// unchanged by the same member, is answered with a steering ladder and, at
// the limit, ends the member's steps.

import (
	"fmt"

	"github.com/ollama/ollama/api"
)

// noChange is the refusal of a call whose new text is its old text
// (Cerebriline's NO_CHANGE_ERROR_PREFIX message, without the file it cannot
// see). The call is answered in place: it never costs a trip.
const noChange = "No change: what you sent as the new text is character-for-character the old text, so this call asks for nothing and was not sent. Sending it again cannot help: look at what it targets as it is now, with a tool that reads, and send the change that is still missing."

// noOpPairs are the argument pairs an edit names its old and new text by.
var noOpPairs = [][2]string{
	{"old_text", "new_text"},
	{"old_string", "new_string"},
	{"oldText", "newText"},
	{"old_str", "new_str"},
	{"old", "new"},
	{"search", "replace"},
	{"find", "replace"},
}

// noOp reports whether c asks to replace a text with itself.
func noOp(c api.ToolCall) bool {
	for _, p := range noOpPairs {
		o, ok1 := c.Function.Arguments.Get(p[0])
		n, ok2 := c.Function.Arguments.Get(p[1])
		os, isO := o.(string)
		ns, isN := n.(string)
		if ok1 && ok2 && isO && isN && os != "" && os == ns {
			return true
		}
	}
	return false
}

// loopStrikes is how many times a member may send one changing call with the
// same arguments; at the limit its steps end (Cerebriline's
// FUTILE_STRIKE_LIMIT).
const loopStrikes = 4

// strikeNote is the steering a repeated changing call's result carries, by
// how many times it has now been sent (Cerebriline's steeringFor and
// strikeWarning).
func strikeNote(n int) string {
	var steer string
	switch n {
	case 2:
		steer = "Before sending anything else: look at what this call targets as it is right now, with a tool that reads rather than writes, and build the change from that."
	case 3:
		steer = "Change what produces it rather than asking again: other arguments, another part, or another tool."
	default:
		steer = "Leave this alone now. Take the next thing that is still wrong."
	}
	warn := fmt.Sprintf("You have %d strike(s) left before the council stops your steps; sending the same call again spends one for nothing.", loopStrikes-n)
	if n >= loopStrikes-1 {
		warn = "This is the last strike: send this call again and the council stops your steps. Change the arguments, use a different tool, or say what you are stuck on."
	}
	if n >= loopStrikes {
		warn = "Your steps are stopped: report where things stand."
	}
	return fmt.Sprintf("\n\n[council: this call has now been sent %d times with these exact arguments, and nothing about them changed between attempts. %s %s]", n, steer, warn)
}

// sends counts, for each changing call in turns, how many times in a row the
// member has sent it with the same arguments and had the same result back,
// up to and including it, by call id. A different result starts the count
// again: the call did something else (Cerebriline clears it on a success).
func (cfg Config) sends(key string, turns []api.Message) map[string]int {
	type last struct {
		result string
		n      int
	}
	out, seen := map[string]int{}, map[string]last{}
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			if local(c) || cfg.readOnly(c) {
				continue
			}
			res, _, ok := cfg.result(key, c)
			if !ok {
				continue
			}
			k := readKey(c)
			l := seen[k]
			if l.n > 0 && l.result == res {
				l.n++
			} else {
				l = last{res, 1}
			}
			seen[k] = l
			out[c.ID] = l.n
		}
	}
	return out
}

// struck reports whether the member's last turn sent a changing call at the
// limit.
func (cfg Config) struck(key string, turns []api.Message) bool {
	if len(turns) == 0 {
		return false
	}
	n := cfg.sends(key, turns)
	for _, c := range turns[len(turns)-1].ToolCalls {
		if n[c.ID] >= loopStrikes {
			return true
		}
	}
	return false
}

// strikeStop is what a member's report says when its steps were stopped.
const strikeStop = "The same change was sent again and again with the same result, so the council stopped these steps."

// refusedEdit is the synthesizer's recovery from a change the tool refused
// (Cerebriline's first steering step): read the target as it is now, then
// make the change from that text, never the same call again.
const refusedEdit = "When a change is refused (its text not found, not unique), look at what it targets as it is now with a tool that reads, then make the change again from that text; never send the same call unchanged."
