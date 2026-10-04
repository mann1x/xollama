package server

import (
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
)

// The same task sent again lands on the same session (its id comes from the
// opening), and nothing the server kept for the run before reaches it
// (native.sh 0422, 0424 and 0426 all ran on one session).
func TestATaskSentAgainStartsClean(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	prev := councilHeld
	councilHeld = &councilHeldStates{m: map[string]heldState{}}
	t.Cleanup(func() { councilHeld = prev })
	e := &councilEngine{route: `{"route":"council"}`, tools: map[string]string{"researcher": "read_files"}}
	s := councilToolServer(t, e)
	req := api.ChatRequest{Model: "council", Tools: councilTestTools, Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}}}
	toolChat(t, s, req)
	councilHeld.mu.Lock()
	var session string
	for k := range councilHeld.m {
		session = k
	}
	councilHeld.mu.Unlock()
	if session == "" {
		t.Fatal("the first run held no resume point")
	}

	// What a past run would leave behind for this session.
	councilKept.Lock()
	councilKept.m[session] = keptTurn{n: 2, p: council.Progress{Route: "PAST-RUN"}}
	councilKept.Unlock()
	councilCompactions.put(session, &compactionRecord{n: 1})
	councilDesks.mu.Lock()
	_, desk := councilDesks.m[session]
	councilDesks.mu.Unlock()
	if !desk {
		t.Fatal("the first run opened no review desk")
	}

	e2 := &councilEngine{route: `{"route":"council"}`, tools: e.tools}
	toolChat(t, councilToolServer(t, e2), req)
	if e2.count("planner") != 1 {
		t.Errorf("the second run was not planned afresh: roles %v", e2.roles)
	}
	councilKept.Lock()
	k := councilKept.m[session]
	councilKept.Unlock()
	if k.p.Route == "PAST-RUN" {
		t.Error("the past run's deliberation was kept for the new task")
	}
	if r := councilCompactions.get(session); r != nil && r.n == 1 {
		t.Error("the past run's compaction record was kept for the new task")
	}
}

func TestOnlyAConversationWithNoAnswerYetIsANewTask(t *testing.T) {
	if !councilFreshTask([]api.Message{{Role: "system"}, {Role: "user", Content: "q"}}) {
		t.Error("a task's first request was not taken as new")
	}
	if councilFreshTask([]api.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}, {Role: "user", Content: "q2"}}) {
		t.Error("a later turn was taken as a new task")
	}
}
