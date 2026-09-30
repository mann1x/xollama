package server

import (
	"testing"

	"github.com/ollama/ollama/api"
)

// The members read past turns without their thinking, which is the council's
// own deliberation when a generic client sends it back.
func TestMembersReadNoPastThinking(t *testing.T) {
	conv := []api.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a", Thinking: "### Researcher 1 …"}}
	got := councilMembersView(conv)
	if got[1].Thinking != "" || got[1].Content != "a" || conv[1].Thinking == "" {
		t.Errorf("view %+v, conversation %+v", got, conv)
	}
}

// The held resume points are bounded: the oldest goes when the store is full.
func TestHeldStatesAreBounded(t *testing.T) {
	h := &councilHeldStates{m: map[string]heldState{}}
	for i := range councilHeldMax + 1 {
		h.put(string(rune('a'+i%26))+string(rune('0'+i/26)), "blob")
	}
	if len(h.m) != councilHeldMax || h.get("a0") != "" {
		t.Errorf("%d held, the first kept %v", len(h.m), h.get("a0") != "")
	}
}
