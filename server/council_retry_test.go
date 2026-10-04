package server

import (
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

// A member call that fails, or stalls, is asked again; the turn goes on.
func TestAFailedOrStalledMemberIsAskedAgain(t *testing.T) {
	was := councilIdleTimeout
	councilIdleTimeout = 100 * time.Millisecond
	backoff := councilRetryBackoff
	councilRetryBackoff = time.Millisecond
	t.Cleanup(func() { councilIdleTimeout, councilRetryBackoff = was, backoff })
	for name, e := range map[string]*councilEngine{
		"fails":  {route: `{"route":"council"}`, fail: map[string]int{"synthesizer": 1}},
		"stalls": {route: `{"route":"council"}`, stall: map[string]bool{"synthesizer": true}},
	} {
		t.Run(name, func(t *testing.T) {
			s := councilServer(t, e, councilOn())
			var answer string
			for _, c := range chatChunks(t, s, councilReq("Why is the sky blue?")) {
				answer += c.Message.Content
			}
			if n := e.count("synthesizer"); n != 2 {
				t.Errorf("the synthesizer was asked %d times, want 2", n)
			}
			if answer == "" {
				t.Error("the turn gave no answer")
			}
		})
	}
}

// A failure that keeps failing ends the turn after councilRetries more tries.
func TestAMemberThatKeepsFailingIsAskedAgainOnlyTwice(t *testing.T) {
	backoff := councilRetryBackoff
	councilRetryBackoff = time.Millisecond
	t.Cleanup(func() { councilRetryBackoff = backoff })
	e := &councilEngine{route: `{"route":"council"}`, fail: map[string]int{"synthesizer": 99}}
	s := councilServer(t, e, councilOn())
	createRequest(t, s.ChatHandler, councilReq("Why is the sky blue?"))
	if n := e.count("synthesizer"); n != 1+councilRetries {
		t.Errorf("the synthesizer was asked %d times, want %d", n, 1+councilRetries)
	}
}

func councilReq(q string) api.ChatRequest {
	return api.ChatRequest{Model: "council", Messages: []api.Message{{Role: "user", Content: q}}}
}
