package server

import (
	"testing"

	"github.com/ollama/ollama/api"
)

// The council behind a chat stays alive (10.5): the next message of the same
// conversation may continue its deliberation, which runs the synthesizer
// alone. The deliberation comes from this server's memory, or -- after a
// restart -- from the client's state; another conversation never gets it.
func TestTheNextTurnContinuesTheSameCouncil(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	councilKept.Lock()
	councilKept.m = map[string]keptTurn{}
	councilKept.Unlock()

	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	empty := ""
	q1 := api.Message{Role: "user", Content: "Why is the sky blue?"}
	req := api.ChatRequest{Model: "council", SessionID: "chat-1", CouncilChatState: &empty, Messages: []api.Message{q1}}
	chunks := chatChunks(t, s, req)
	_, done := stateChunks(chunks)
	_, answer := joined(chunks)
	if e.count("researcher") != 2 || done == "" {
		t.Fatalf("first turn: researchers %d, state %v", e.count("researcher"), done != "")
	}

	next := []api.Message{q1, {Role: "assistant", Content: answer}, {Role: "user", Content: "It is still not right; go on."}}
	for _, tc := range []struct {
		name   string
		forget bool // the server restarted: only the client's state has it
	}{{"from memory", false}, {"from the client's state", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.forget {
				councilKept.Lock()
				councilKept.m = map[string]keptTurn{}
				councilKept.Unlock()
			}
			e2 := &councilEngine{route: `{"route":"continue"}`}
			s2 := councilServer(t, e2, councilOn())
			st := done
			_, content := joined(chatChunks(t, s2, api.ChatRequest{Model: "council", SessionID: "chat-1", CouncilChatState: &st, Messages: next}))
			if e2.count("researcher") != 0 || e2.count("critic") != 0 || e2.count("synthesizer") != 1 || content == "" {
				t.Fatalf("researchers %d critics %d synthesizer %d, answer %q", e2.count("researcher"), e2.count("critic"), e2.count("synthesizer"), content)
			}
		})
	}

	// Another conversation, even in the same session, is not offered it.
	e3 := &councilEngine{route: `{"route":"continue"}`}
	s3 := councilServer(t, e3, councilOn())
	other := []api.Message{{Role: "user", Content: "Something else"}, {Role: "assistant", Content: "ok"}, {Role: "user", Content: "go on"}}
	joined(chatChunks(t, s3, api.ChatRequest{Model: "council", SessionID: "chat-1", CouncilChatState: &done, Messages: other}))
	if e3.count("researcher") != 2 {
		t.Fatalf("another conversation continued a deliberation it never had: researchers %d", e3.count("researcher"))
	}
}
