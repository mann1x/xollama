package llm

// xollama-hook: repeat-last-n
//
// repeat_last_n = -1 means "the whole context" in Ollama's API (upstream's own
// MLX runner resolves any negative value to num_ctx). opencoti accepts -1 and
// resolves it to the slot's context itself. Stock llama-server's schema takes
// only 0 and up and answers -1 with a 400 ("Value must be between 0 <= value
// <= 2147483647, but got -1"). So on stock llama.cpp a negative value goes out
// as num_ctx, the per-sequence context the launch sized each slot to
// (`-c num_ctx * parallel`). Owner, 2026-10-20: "map -1 to num_ctx for stock
// llama.cpp". This is a deliberate departure from upstream's stock path, which
// passes -1 through and fails.
func stockRepeatLastN(n, numCtx int, usedOpencoti bool) int {
	if n < 0 && !usedOpencoti && numCtx > 0 {
		return numCtx
	}
	return n
}
