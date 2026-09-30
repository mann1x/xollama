package server

import (
	"sync"
	"time"
)

// A client that sends tools but no council_chat_state is a generic harness:
// it drives a council model as it drives any model, and knows nothing of the
// council's resume point. Its turn is still the council's -- a task sent to a
// council model is what the council is for -- and the server keeps the
// sealed resume point in its place, by the conversation's session, and sends
// it none. Each request of the turn picks it up again; the state binds itself
// to the turn (councilTurnHashes), so a point kept for another turn is a
// fresh start, as a client's would be.

const (
	councilHeldMax = 64
	councilHeldTTL = 2 * time.Hour
)

type heldState struct {
	blob string
	at   time.Time
}

// councilHeldStates holds the latest resume point of each generic client's
// conversation.
type councilHeldStates struct {
	mu sync.Mutex
	m  map[string]heldState
}

var councilHeld = &councilHeldStates{m: map[string]heldState{}}

// get is the conversation's resume point, or "" for none.
func (h *councilHeldStates) get(session string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.m[session]
	if !ok || time.Since(s.at) > councilHeldTTL {
		delete(h.m, session)
		return ""
	}
	return s.blob
}

// put keeps blob as the conversation's resume point, dropping the oldest
// when the store is full.
func (h *councilHeldStates) put(session, blob string) {
	if session == "" || blob == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.m[session]; !ok && len(h.m) >= councilHeldMax {
		oldest, at := "", time.Time{}
		for k, s := range h.m {
			if oldest == "" || s.at.Before(at) {
				oldest, at = k, s.at
			}
		}
		delete(h.m, oldest)
	}
	h.m[session] = heldState{blob: blob, at: time.Now()}
}
