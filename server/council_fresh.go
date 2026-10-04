package server

import "github.com/ollama/ollama/api"

// A conversation's session id is derived from its opening (llm.DeriveSessionID),
// so a harness that sends the same task again starts on the same session as
// the run before it. What the server keeps per session -- the last
// deliberation, the review desk, the held resume point, the compaction record
// -- then belongs to another conversation: native.sh 0422, 0424 and 0426 all
// ran on xo-efd33195b0dfad24, and the kept deliberation's prefix check passes
// for a one-message task. A conversation with no assistant turn yet is a new
// task, so nothing kept for its session is its own. Only a derived session is
// reset: a session id the client sends is its statement that the requests
// belong together.

// councilFreshTask reports whether msgs start a task: no assistant turn yet.
func councilFreshTask(msgs []api.Message) bool {
	for _, m := range msgs {
		if m.Role == "assistant" {
			return false
		}
	}
	return true
}

// councilForget drops everything kept for session. The engine's root pool and
// the kept stage layers stay: each is matched by its own content, never by the
// session alone.
func councilForget(session string) {
	if session == "" {
		return
	}
	councilKept.Lock()
	delete(councilKept.m, session)
	councilKept.Unlock()
	councilDesks.close(session)
	councilHeld.drop(session)
	councilCompactions.drop(session)
}
