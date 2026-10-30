package llm

// xollama-hook: repeat-last-n
//
// repeat_last_n = -1 means "the whole context" in Ollama's API. Stock
// llama-server's schema takes only 0 and up, so since v0.40.13 (#19344)
// upstream sends a negative value as num_ctx (llamaServerRepeatLastN); xollama
// had done the same on stock since 2026-10-20 (owner: "map -1 to num_ctx for
// stock llama.cpp"). opencoti accepts -1 and resolves it to the slot's context
// itself, so it gets the value as sent. On stock this is upstream's function,
// unchanged.
func stockRepeatLastN(n, numCtx int, usedOpencoti bool) int {
	if usedOpencoti {
		return n
	}
	return llamaServerRepeatLastN(n, numCtx)
}
