package server

// xollama: the council kept across turns -- plans/agentic-council-chat.md,
// 10.5. Additive; reached from server/council.go (the `council` hook).
//
// A council turn that ends with its answer leaves its deliberation (the plan
// and the last round, council.Kept) for the next message, which is feedback
// to that same council: the planner may then continue it instead of starting
// over. It is bound to the conversation up to and including the user message
// it answered, so it is offered only while the client continues that
// conversation: an edit, a rewind or another conversation finds no match.
// It lives in this server's memory per session, and in the sealed
// council_chat_state (fields 6-8), so a restart or another replica with the
// same key does not lose it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"sync"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
)

// keptTurn is one session's last deliberation and the conversation it
// answered: its first n messages, hashed.
type keptTurn struct {
	n      int
	prefix []byte
	p      council.Progress
}

var councilKept = struct {
	sync.Mutex
	m map[string]keptTurn
}{m: map[string]keptTurn{}}

// councilPrefixHash hashes the first n messages as councilTurnHashes does:
// role and content only.
func councilPrefixHash(msgs []api.Message, n int) []byte {
	type rc struct{ R, C string }
	out := make([]rc, 0, n)
	for _, m := range msgs[:n] {
		out = append(out, rc{m.Role, m.Content})
	}
	b, _ := json.Marshal(out)
	s := sha256.Sum256(b)
	return s[:]
}

// lastUserEnd is one past the last user message: the conversation a turn
// answers.
func lastUserEnd(msgs []api.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i + 1
		}
	}
	return 0
}

// keepDeliberation records what an answered council turn leaves for the
// next one, and returns it for the turn's sealed state.
func keepDeliberation(session string, msgs []api.Message, kept *council.Progress) *keptTurn {
	n := lastUserEnd(msgs)
	if kept == nil || n == 0 {
		if session != "" {
			councilKept.Lock()
			delete(councilKept.m, session) // a direct answer ends the deliberation
			councilKept.Unlock()
		}
		return nil
	}
	k := keptTurn{n: n, prefix: councilPrefixHash(msgs, n), p: *kept}
	if session != "" {
		councilKept.Lock()
		councilKept.m[session] = k
		councilKept.Unlock()
	}
	return &k
}

// previousDeliberation is the deliberation this request continues: the
// session's in memory, else the one the client's state carries, when either
// was made for a conversation this request extends by a newer user message.
func previousDeliberation(session string, msgs []api.Message, fromState *keptTurn) *council.Progress {
	match := func(k keptTurn) bool {
		return k.n > 0 && k.n < lastUserEnd(msgs) && bytes.Equal(k.prefix, councilPrefixHash(msgs, k.n))
	}
	if session != "" {
		councilKept.Lock()
		k, ok := councilKept.m[session]
		councilKept.Unlock()
		if ok && match(k) {
			p := k.p
			return &p
		}
	}
	if fromState != nil && match(*fromState) {
		p := fromState.p
		return &p
	}
	return nil
}
