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
const stuckNote = "The last %d checks returned the same output: the changes so far have not moved it. The explanation behind those changes is refuted: mark its tasks refuted, and assign no more changes of the same kind. Do not refine the same approach again. Change approach: find where the fault lies first -- narrow down what the check exercises until its output changes -- or replace the failing part whole instead of changing it piece by piece."

// movedNote and sameNote say how a check compares with the one before.
const (
	movedNote = "Its check's output changed from the one before: progress, and the new output is the lead to follow."
	sameNote  = "Its check returned the same output as the one before."
	// The same, against the last check of the agent that worked on it
	// before the council (directive.go).
	movedAgentNote = "Its check's output changed from the one the agent before the council got: progress, and the new output is the lead to follow."
	sameAgentNote  = "Its check returned the same output as the agent before the council got."
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
		if pc := cfg.priorCheck(); i == 0 && pc != "" && len(cfg.checks) > 0 && cfg.checks[0] != "" {
			// The harness's agent checked before the council began.
			if sameCheck(cfg.checks[0], pc) {
				b.WriteString("\n" + sameAgentNote)
			} else {
				b.WriteString("\n" + movedAgentNote)
			}
		}
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

// keptNote sends a re-plan back, once, when the stuck note told the planner to
// change approach and its update of the list does not: no task refuted, or no
// task for the new approach. On eleven2go (hard, 5ce5f7e7) the planner wrote
// in its plan that it would replace the failing part whole, and then kept every
// task open, assigned the refuted one again and gave the replacement to nobody,
// so the synthesizer applied local changes once more. The list is what the
// members work from, so it is where the change of approach has to be.
const keptNote = "ROLE: PLANNER. The last %d checks returned the same output, and this plan %s: it keeps the approach those checks refuted. Mark each task whose changes they tested as refuted, with their output as its outcome, and add the new approach as a new task (id 0) assigned to a researcher. When the new approach is to replace the failing part whole, that task is to write the whole replacement out in full: the researcher writes it, and the synthesizer applies it in one write. Reply with JSON only, in the same grammar."

// approachKept says how the list's update next of prev keeps the approach,
// or "" when it changes it: it refutes a task and adds one.
func approachKept(prev, next []Task) string {
	was := make(map[int]string, len(prev))
	for _, t := range prev {
		was[t.ID] = t.Status
	}
	refuted, added := false, false
	for _, t := range next {
		old, ok := was[t.ID]
		switch {
		case !ok:
			added = true
		case t.Status == TaskRefuted && old != TaskRefuted:
			refuted = true
		}
	}
	switch {
	case !refuted && !added:
		return "marks no task refuted and adds none"
	case !refuted:
		return "marks no task refuted"
	case !added:
		return "adds no task for a new approach"
	}
	return ""
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
	if pc := cfg.priorCheck(); n > 0 && n == len(checks) && pc != "" && sameCheck(pc, checks[0]) {
		// The agent's last check returned it too.
		n++
	}
	return n
}

func sameCheck(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// lastCheck is the output of the check the synthesizer ran on its changes,
// or "" when there is none.
//
// The check is what the member itself uses to check: among the calls that
// only read, made after its first change, the one (tool and arguments) it
// called most, the earliest on a tie; one whose output is the previous
// cycle's check is that same check. Its latest result counts. Twice a
// narrower rule was wrong on eleven2go:
//   - "the last read" took a search made after the check (96edc4ae);
//   - "the first read after the last change" took a read of the file the
//     synthesizer had just edited, after it had run the check and then
//     edited again (4770e33b run 2).
//
// Both read as a check whose output moved, so six identical errors were
// reported as progress and the council was never told to change approach.
// With no change in the cycle, the last call that only reads stands.
func (cfg Config) lastCheck(key string, turns []api.Message) string {
	type cand struct {
		n, first int
		last     api.ToolCall
	}
	var order []string
	cands := map[string]*cand{}
	var last *api.ToolCall
	wrote, at := false, 0
	for i := range turns {
		for j := range turns[i].ToolCalls {
			c := turns[i].ToolCalls[j]
			at++
			switch {
			case local(c):
			case !cfg.readOnly(c):
				wrote = true
			case cfg.CheckTool != "" && c.Function.Name != cfg.CheckTool:
				// The harness named its check: other reads are not it.
			default:
				last = &turns[i].ToolCalls[j]
				if !wrote {
					continue
				}
				k := readKey(c)
				if cands[k] == nil {
					cands[k] = &cand{first: at}
					order = append(order, k)
				}
				cands[k].n++
				cands[k].last = c
			}
		}
	}
	if len(order) == 0 {
		if last == nil {
			return ""
		}
		res, _, _ := cfg.result(key, *last)
		return truncate(res, maxCheckChars)
	}
	out := func(k string) (string, bool) {
		res, _, ok := cfg.result(key, cands[k].last)
		return truncate(res, maxCheckChars), ok
	}
	if n := len(cfg.checks); n > 0 && cfg.checks[n-1] != "" {
		for _, k := range order {
			if res, ok := out(k); ok && sameCheck(res, cfg.checks[n-1]) {
				return res
			}
		}
	}
	best := order[0]
	for _, k := range order[1:] {
		if cands[k].n > cands[best].n {
			best = k
		}
	}
	res, ok := out(best)
	if !ok {
		// The check's own result is what counts; without it, nothing is
		// compared rather than a read standing in for it.
		return ""
	}
	return res
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
