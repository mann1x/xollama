---
paths:
  - llm/engine_estimate.go
  - llm/engine_estimate_test.go
  - llm/server.go
  - server/sched.go
  - server/sched_single_sequence_test.go
  - server/slots_live.go
  - server/slots_live_test.go
  - docs/xollama/slots.mdx
---

# Dynamic slots and the single-sequence rule

- One flag on `llm.LlamaServerConfig` (`llm/server.go`), set by
  `llamaServerConfigForModel` in `server/routes.go`: `SingleSequenceOnly`
  (`singleSequenceOnly` — an embedding model). Every `resolveSlotPlan` caller
  passes it.
- **No architecture deny-list.** Upstream v0.40.15 (#19545) dropped the
  ollama#4165 list because llama-server serves those families in parallel;
  xollama's opencoti exemption from it (`SingleSequenceStockOnly`,
  `parallelUnsafeArchitecture`, the scheduler's `WouldUseOpencoti` check and
  the relaunch at one sequence, `servedSequences`) went with it. Do not bring
  a per-engine clamp back: with `XOLLAMA_ENGINE=llamacpp` the count must be
  upstream's.
- **`slots.live`** (`types/xollama/config.go`, `xollama tweak model`
  `slots-live`) is the count a load starts with: `liveSlots` in
  `server/slots_live.go` (`slots-live` hook, one line in `load()` after the
  embedding model's cap to one). For a completion model `llm.WouldUseOpencoti` says
  opencoti will serve it is `slots.live`, else `XOLLAMA_PARALLEL`, else one:
  **`OLLAMA_NUM_PARALLEL` is never read on opencoti** (owner's rule
  2026-09-27 — a stock ollama beside xollama shares it). Stock llama.cpp
  reserves a KV copy per slot at launch, so there the operator's
  `OLLAMA_NUM_PARALLEL` stands. `slots.live` above `slots.max` is refused by `Validate`.
  Guard: `server/slots_live_test.go`; Registry row `slots-live`.
- **The pool is `-c = num_ctx × live`, never × the ceiling** (owner's ruling
  2026-09-27). A request with no window is guaranteed the whole pool, so by
  default requests take turns, as on Ollama. Concurrency comes from clients
  booking windows (`placement.num_ctx`) out of a pool the operator sized, or
  from `XOLLAMA_PARALLEL`. opencoti commits the pool at load (b171: `-c 131072`
  cost 4x the KV of `-c 32768`); grow-on-demand is opencoti c9 stage 8. Do not
  widen `-c` or inject a per-request `num_ctx` before that ships. Prose:
  `docs/xollama/slots.mdx` "How the context is shared".
- **A stated pool unties the two** (`pool-cells` hook, owner 2026-10-24):
  `kv.pool_cells` replaces `-c` on opencoti (`withPoolCells`,
  `llm/engine_pool_cells.go`, after `appendSlotArgs`; never below one
  `num_ctx`, `--kv-unified` added). `num_ctx` stays the per-conversation cap.
  It is the operator's explicit size, not xollama widening `-c` on its own.
  The scheduler prices the same pool (`llm.PoolCellsFor` in `predictVRAM`).
  Refused with engine `llamacpp` or `kv.unified` off; no schema floor (like
  `slots.live`). Measured on solidPC's 3090, 2026-10-24: omnimerge-v4 IQ2_M
  kvarn3, `num_ctx 262144`, live 2, pool 655360 → `-c 655360`, 23.5 GB,
  rolling window not engaged. Guards: `llm/engine_pool_cells_test.go`,
  `TestAStatedPoolIsWhatThePredictionPricesTheCacheAt`,
  `types/xollama/pool_cells_test.go`. Tweak: `--kv-pool-cells`.
- With `XOLLAMA_ENGINE=llamacpp` `wouldUseOpencoti` is false, so the count
  is upstream's byte for byte. Embedding models stay at one sequence on every
  engine.
- Guards: `TestEveryEngineServesTheOperatorsSequenceCount` and
  `TestOnlyAnEmbeddingModelIsHeldToOneSequence` in
  `server/sched_single_sequence_test.go`. Prose: `docs/xollama/slots.mdx`. Registry row
  `launch-config` in `docs/protocols/UPSTREAM-SYNC.md`.
