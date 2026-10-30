---
paths:
  - llm/engine_repeat_last_n.go
  - llm/engine_repeat_last_n_test.go
  - llm/llama_server.go
---

# `repeat_last_n = -1` on stock llama.cpp (`repeat-last-n` hook)

- `llm/engine_repeat_last_n.go` is additive and holds `stockRepeatLastN`. The
  hook is the `repeat_last_n` value in both request bodies of
  `llm/llama_server.go` (`llamaServerCompletionRequest` in `Completion`, the
  map in `llamaServerChatRequest`), each one line ending
  `// xollama-hook: repeat-last-n`. Keep the marker a trailing comment, so
  gofmt leaves upstream's alignment alone. Registry row `repeat-last-n` in
  `docs/protocols/UPSTREAM-SYNC.md`.
- **Why.** In Ollama's API, -1 means "the whole context" (upstream's MLX
  runner resolves any negative value to `num_ctx`). Stock `llama-server`'s
  schema accepts only 0 and up, and it answered -1 with a 400 ("Value must be
  between 0 <= value <= 2147483647, but got -1").
- On stock llama.cpp a negative value is sent as `num_ctx`, the per-sequence
  context each slot was launched with (`-c num_ctx * parallel`). `stockRepeatLastN`
  calls upstream's own `llamaServerRepeatLastN` for this, so a `numCtx` of 0
  sends 0. opencoti resolves -1 itself, so it gets the value as sent.
- **Upstream's stock path since v0.40.13** (#19344). xollama had mapped it on
  stock since 2026-10-20 (owner: "map -1 to num_ctx for stock llama.cpp"); the
  hook now keeps only opencoti's value, and with `XOLLAMA_ENGINE=llamacpp`
  every value is upstream's to the byte.
- Guards in `llm/engine_repeat_last_n_test.go`: `TestStockRepeatLastN`,
  `TestChatBodyRepeatLastNOnStockAndOpencoti`.

```sh
go test ./llm/ -run 'RepeatLastN'
```
