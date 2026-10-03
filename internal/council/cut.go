package council

import "github.com/ollama/ollama/api"

// A writer's change travels in one tool call, and a call is whole or it is
// nothing: cut at the reply cap, only the prose before it survives. An edit
// carries the old text and the new, so rewriting one part of a file costs
// twice that part in tokens. The medium council on eleven2go (2026-09-29)
// stopped at exactly 3,072 tokens three times, each time inside the rewrite
// stuck detection had asked for, and never fixed the fault plain fixed in
// that one edit.

// writeMaxTokens is the least reply cap a member that writes gets on a tool
// turn: room for an edit of a part twice the size of the one that was cut.
const writeMaxTokens = 16384

// maxCuts is how many cut replies a member is asked again for in a row.
const maxCuts = 1

// cutNote asks a member whose reply was cut at its cap to change less at once.
const cutNote = "Your last reply reached its length limit before its tool call was complete, so the call was not made and nothing changed. Make the change in smaller steps: one call per part, each carrying only the lines it replaces."

// writeTok is the reply cap of a member that writes, on a tool turn.
func writeTok(r Role, n int) int {
	if writes(r) && n > 0 {
		return max(n, writeMaxTokens)
	}
	return n
}

// wasCut reports a reply the cap ended with no call: what it was writing is
// lost, and the member is asked again.
func wasCut(r Role, rep Reply) bool {
	return rep.Cut && len(rep.Calls) == 0 && writes(r)
}

// cutTurn is the note that follows a cut reply.
func cutTurn() api.Message { return user(cutNote) }
