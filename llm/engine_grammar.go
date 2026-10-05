package llm

// xollama: the window of a schema conversion on opencoti -- part of the
// `council` hook, reached from schemaGrammar in llama_server.go and from
// councilMembers.call in server/council.go.

import "encoding/json"

// GrammarWindow is the window, in cells, a schema conversion books on
// opencoti. The conversion is an empty completion: it evaluates and generates
// nothing, but opencoti admits every completion to a window, and one that
// names none asks for a whole default window (262144 on a 384k pool). With a
// council's owners holding the pool that was refused ("largest admissible 185
// < this request's minimum window 258", a 429, and the turn failed), on the
// first thinking member with a format of every server process: measured on
// solidPC, 2026-10-05, 4 runs of 7, on two engines. The engine's own minimum
// for such a request is 258.
const GrammarWindow = 512

// grammarWindow is what a schema conversion states as its window: nothing on
// stock llama.cpp, whose body stays upstream's.
func grammarWindow(usedOpencoti bool) int {
	if usedOpencoti {
		return GrammarWindow
	}
	return 0
}

// SchemaGrammarKnown is whether schema was already converted in this process,
// so a thinking request with it sends the engine nothing first.
func SchemaGrammarKnown(schema json.RawMessage) bool {
	schemaGrammars.Lock()
	defer schemaGrammars.Unlock()
	_, ok := schemaGrammars.entries[string(schema)]
	return ok
}
