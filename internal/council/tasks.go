package council

// The planner is the council's coordinator: it keeps the council's task list
// and schedules the researchers on it (plans/agentic-council-chat.md, 11.10;
// the owner, 2026-09-28: "he should act as a coordinator and program/task
// manager"). The list is part of every plan the planner writes, under the
// plan's JSON grammar, so every plan and re-plan is an update of it: a tool
// would be one the planner may skip, as the synthesizer skipped
// council_review on the fourth simple run. The rules are the runtime's, not
// the model's: no task leaves the list, a task is closed only with the
// outcome that closed it, a refuted task is never assigned again, and only a
// researcher the council has can be assigned.

import (
	"fmt"
	"strings"

	"github.com/ollama/ollama/api"
)

// A task's statuses.
const (
	TaskOpen     = "open"
	TaskAssigned = "assigned"
	TaskDone     = "done"
	TaskRefuted  = "refuted"
)

// maxTasks bounds the list a plan carries; maxTaskChars each task's text.
const (
	maxTasks     = 24
	maxTaskChars = 400
)

// Task is one item of the council's work.
type Task struct {
	ID     int    `json:"id"`
	Task   string `json:"task"`
	Status string `json:"status"`
	// Researcher is the researcher working on it (from 1), or 0.
	Researcher int `json:"researcher,omitempty"`
	// Outcome is what settled a done or refuted task.
	Outcome string `json:"outcome,omitempty"`
}

func closed(s string) bool { return s == TaskDone || s == TaskRefuted }

// mergeTasks is the list after the planner's update next of prev, for a
// council of researchers.
func mergeTasks(prev, next []Task, researchers int) []Task {
	out := append([]Task(nil), prev...)
	at := map[int]int{}
	top := 0
	for i, t := range out {
		at[t.ID] = i
		top = max(top, t.ID)
	}
	for _, t := range next {
		t.Task = truncate(strings.TrimSpace(t.Task), maxTaskChars)
		t.Outcome = truncate(strings.TrimSpace(t.Outcome), maxTaskChars)
		if t.Researcher < 1 || t.Researcher > researchers {
			t.Researcher = 0
		}
		switch t.Status {
		case TaskOpen, TaskAssigned, TaskDone, TaskRefuted:
		default:
			t.Status = TaskOpen
		}
		if closed(t.Status) && t.Outcome == "" {
			// A task is closed only by what closed it.
			t.Status = TaskOpen
		}
		if t.Status == TaskAssigned && t.Researcher == 0 {
			t.Status = TaskOpen
		}
		if closed(t.Status) {
			t.Researcher = 0
		}
		i, known := at[t.ID]
		if !known || t.ID <= 0 {
			if t.Task == "" || len(out) >= maxTasks {
				continue
			}
			top++
			t.ID = top
			at[t.ID] = len(out)
			out = append(out, t)
			continue
		}
		old := out[i]
		if old.Status == TaskRefuted {
			// Refuted stays refuted: no researcher is given it again.
			continue
		}
		if t.Task == "" {
			t.Task = old.Task
		}
		out[i] = t
	}
	return out
}

// tasksFromBriefs is the list a plan without one stands for: a task per
// brief, assigned to its researcher.
func tasksFromBriefs(p Plan) []Task {
	var out []Task
	for i, b := range p.Briefs {
		if strings.TrimSpace(b) != "" {
			out = append(out, Task{ID: len(out) + 1, Task: truncate(b, maxTaskChars), Status: TaskAssigned, Researcher: i + 1})
		}
	}
	return out
}

// renderTasks is the list as the members read it.
func renderTasks(ts []Task) string {
	if len(ts) == 0 {
		return "(no tasks yet)"
	}
	var b strings.Builder
	for _, t := range ts {
		fmt.Fprintf(&b, "\n- #%d [%s", t.ID, t.Status)
		if t.Researcher > 0 {
			fmt.Fprintf(&b, ", researcher %d", t.Researcher)
		}
		fmt.Fprintf(&b, "] %s", t.Task)
		if t.Outcome != "" {
			fmt.Fprintf(&b, " -- %s", t.Outcome)
		}
	}
	return b.String()[1:]
}

// tasksSource heads the council's task list.
const tasksSource = "THE COUNCIL'S TASK LIST, KEPT BY THE PLANNER"

// tasksMsg is the list as a council message, or nothing when it is empty.
func tasksMsg(ts []Task) []api.Message {
	if len(ts) == 0 {
		return nil
	}
	return []api.Message{sourced(tasksSource, renderTasks(ts))}
}

// ledgerRules is the planner's standing instruction for the list.
const ledgerRules = `You are the council's coordinator: you keep its task list and schedule the researchers on it. In "tasks", write the whole list: every task already on it stays, with its id; add a task for each new piece of work (id 0 for a new one). Status: "open" (not started), "assigned" with "researcher" (who works on it now), "done" or "refuted" with "outcome" (the evidence that settled it: a check's output, a result). A refuted task is never assigned again. Each brief is the tasks you assign to that researcher, by id and in words. Close tasks only on evidence, never on a member's claim alone.`

// taskSchema is the JSON schema of one task.
func taskSchema(researchers int) string {
	return fmt.Sprintf(`{"type":"object","properties":{"id":{"type":"integer","minimum":0},"task":{"type":"string"},"status":{"enum":["open","assigned","done","refuted"]},"researcher":{"type":"integer","minimum":0,"maximum":%d},"outcome":{"type":"string"}},"required":["id","task","status"]}`, researchers)
}

// planTasks is the list a plan updates the council's with: its own, or a
// task per brief when it wrote none.
func planTasks(p Plan) []Task {
	if len(p.Tasks) > 0 {
		return p.Tasks
	}
	return tasksFromBriefs(p)
}
