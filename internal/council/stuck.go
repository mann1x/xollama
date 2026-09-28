package council

// A council that changes things and checks them can go round without moving
// the check: the fifth simple run on eleven2go made 14 changes over 6 cycles
// and every check returned the same error, while the plain model, stuck the
// same way, rewrote the broken part whole and followed each new error to the
// fix (plans/agentic-council-chat.md, 11.11). So the output of each failed
// cycle's last check is kept, and the next cycle is told whether it moved: a
// new output is a lead to follow, the same output again a sign to change
// approach. The wording names no topic -- the owner's rule: the nudge must
// hold for whatever the council is working on.

import (
	"fmt"
	"strings"

	"github.com/ollama/ollama/api"
)

// maxCheckChars is how much of a check's output is kept to compare.
const maxCheckChars = 2000

// stuckAfter is how many failed checks in a row with the same output make
// the next cycle change approach.
const stuckAfter = 2

// stuckNote is the change of approach, in words that fit any work.
const stuckNote = "The last %d checks returned the same output: the changes so far have not moved it. Do not refine the same approach again. Change approach: find where the fault lies first -- narrow down what the check exercises until its output changes -- or replace the failing part whole instead of changing it piece by piece."

// movedNote and sameNote say how a check compares with the one before.
const (
	movedNote = "Its check's output changed from the one before: progress, and the new output is the lead to follow."
	sameNote  = "Its check returned the same output as the one before."
)

// testsBody is the failed checks as the next cycle reads them: each with
// whether its output moved, and the change of approach when it has stopped.
func (cfg Config) testsBody() string {
	var b strings.Builder
	b.WriteString(testsIntro)
	for i, t := range cfg.tests {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "TEST %d:\n%s", i+1, t)
		if i > 0 && i < len(cfg.checks) && cfg.checks[i] != "" && cfg.checks[i-1] != "" {
			if sameCheck(cfg.checks[i], cfg.checks[i-1]) {
				b.WriteString("\n" + sameNote)
			} else {
				b.WriteString("\n" + movedNote)
			}
		}
	}
	if n := cfg.stuck(); n >= stuckAfter {
		b.WriteString("\n\n" + fmt.Sprintf(stuckNote, n))
	}
	return b.String()
}

// stuck is how many of the last failed checks returned the same output.
func (cfg Config) stuck() int {
	checks := cfg.checks[:min(len(cfg.checks), len(cfg.tests))]
	n := 0
	for i := len(checks) - 1; i >= 0; i-- {
		if checks[i] == "" || (n > 0 && !sameCheck(checks[i], checks[len(checks)-1])) {
			break
		}
		n++
	}
	return n
}

func sameCheck(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// lastCheck is the output of the last call of the synthesizer's turns that
// only reads -- the check it ran -- or "".
func (cfg Config) lastCheck(key string, turns []api.Message) string {
	for i := len(turns) - 1; i >= 0; i-- {
		for j := len(turns[i].ToolCalls) - 1; j >= 0; j-- {
			c := turns[i].ToolCalls[j]
			if local(c) || !cfg.readOnly(c) {
				continue
			}
			if res, _, ok := cfg.result(key, c); ok {
				return truncate(res, maxCheckChars)
			}
		}
	}
	return ""
}

// checkNote says, in a testing synthesizer's instruction, that a whole
// replacement is one change.
const checkNote = " A proposal to replace a part whole is made whole, in one change."

// wholeNote lets researchers propose a replacement once changes have not
// moved the check.
const wholeNote = " When changes have not moved the check, you may propose replacing the failing part whole."

// recordCheck hands the cycle's last check output to the run.
func (cfg Config) recordCheck(key string, turns []api.Message) {
	if cfg.checked != nil {
		*cfg.checked = cfg.lastCheck(key, turns)
	}
}
