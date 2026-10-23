---
paths:
  - llm/engine_stream_error.go
  - llm/engine_stream_error_test.go
  - llm/llama_server.go
  - scripts/gates/bench.sh
---

# An engine error mid-stream is an error (`engine-stream-error` hook)

- `llm/engine_stream_error.go` is additive. The hook is two marked lines in
  `Completion` (`llm/llama_server.go`): each event is shown to `streamFailed`,
  and the function ends with `streamUnfinished` instead of `nil`. Registry row
  `engine-stream-error` in `docs/protocols/UPSTREAM-SYNC.md`.
- **Why.** Upstream's completion loop reads `content` and `stop` only, so the
  engine's error event (`data: {"error":{"code":500,"message":"…"}}`) parses
  as an empty event, the stream ends without a final one and `Completion`
  returns nil having called back nothing: the client gets 200 with a zero
  response, or an empty stream. Measured on the Mac mini with a Qwen3.5-family
  model at temperature 0 (opencoti bug-4048, `got exception: vector`).
- **Off means off.** Both functions return nil unless `s.usedOpencoti`; stock
  llama.cpp keeps upstream's reading.
- `streamErrorMessage` takes the `error` as an object with a `message`, a
  plain string, or its raw text; a missing or `null` `error` is no error.
- The error wraps the engine's own message (`an error was encountered while
  running the model: …`). Never retry, never answer empty.
- Guards in `llm/engine_stream_error_test.go`:
  `TestAnOpencotiErrorEventFailsTheCompletion`,
  `TestAnOpencotiStreamWithoutItsFinalEventFailsTheCompletion`,
  `TestAFinishedOpencotiCompletionIsNotAnError`,
  `TestStockLlamaCppKeepsUpstreamsStreamHandling`.
- `scripts/gates/bench.sh` holds the same ground from outside: four sampled
  runs and one greedy run (temperature 0) per model, and a FAIL line for a run
  that returned an error, answered no tokens, or answered fewer than 20
  different words.

```sh
go test ./llm/ -run 'Opencoti|StockLlamaCpp'
```
