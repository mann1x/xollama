---
paths:
  - llm/llama_server.go
  - llm/engine/stockless.go
  - llm/engine/stockless_test.go
  - llm/stockless_darwin_test.go
  - llm/engine/opencoti.go
  - docs/features/engine-opencoti-llamafile.md
---

# A missing stock `llama-server` (`stockless` hook)

- `engine.StockShipped(goos)` (`llm/engine/stockless.go`) says whether the
  platform ships a stock `llama-server` at all. It is false on macOS: every GGUF
  load runs on opencoti there, and a load that does not get it is refused with
  `engine.StocklessRefusal`, never run on a binary that does not exist.
- **On a stock-shipping platform the miss is fatal only for a load bound for
  stock llama.cpp.** `startLlamaServer` keeps `FindLlamaServer`'s error as
  `findErr` and goes on with `exe = ""` when the load is bound for opencoti:
  `XOLLAMA_ENGINE=opencoti` (`envconfig.Var(engine.EnvSelector)`) or the
  model's own `engine=opencoti` pin (`launch.config.enginePin()`). The guard
  after `engine.Launch` returns `findErr` when opencoti did not take the load
  (`!usedOpencoti && exe == ""`): never exec an empty path.
- **Why:** a `go build -o xollama .` tree has no `lib/ollama` and no stock
  binary, and the release's council gate (`scripts/gates/council.sh` via
  `scripts/gates/serve-side.sh`, `XOLLAMA_ENGINE=opencoti`, the pinned engine
  staged) must run on it without a `cmake` CPU-engine build. Before 2026-10-23
  the launch aborted on the missing stock binary before opencoti was asked.
- **Off means off.** With `XOLLAMA_ENGINE=llamacpp`, or no selector and no pin,
  a missing stock binary is upstream's error, unchanged. `exe` is `""` only
  when `FindLlamaServer` failed, so `findErr` is always set where it is returned.
- `engine.Launch` (`llm/engine/opencoti.go`) names the miss instead of "falling
  back"; `retryOnStockEngine` (`XOLLAMA_ENGINE_FALLBACK`) does nothing where
  no stock binary exists. In `SetupLlamaServerCommandEnv` the engine's
  directory stands in for the stock binary's.
- Both marked blocks in `startLlamaServer` are the one `stockless` Registry row
  in `docs/protocols/UPSTREAM-SYNC.md`; a change to either updates that row in
  the same commit. Guards: `llm/engine/stockless_test.go`,
  `llm/stockless_darwin_test.go` (the hosted macOS leg). Prose:
  `docs/features/engine-opencoti-llamafile.md`.
