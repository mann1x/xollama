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

- Two flags on `llm.LlamaServerConfig` (`llm/server.go`), both set by
  `llamaServerConfigForModel` in `server/routes.go`: `SingleSequenceOnly`
  (`singleSequenceOnly` — an embedding model or the ollama#4165 deny-list) and
  `SingleSequenceStockOnly` (`parallelUnsafeArchitecture` — the deny-list
  alone, never an embedding model).
- **The deny-list is stock llama.cpp's.** It records what stock gets wrong
  with more than one sequence in flight; opencoti serves those architectures
  in parallel, so there the rule is lifted. `LlamaServerConfig.singleSequence`
  (`llm/engine_estimate.go`) is the one place that combines the two flags with
  the engine; every `resolveSlotPlan` caller passes it, never the raw field.
- The scheduler (`server/sched.go`) clamps to one only when
  `llm.WouldUseOpencoti` says stock will serve — a prediction made before the
  process exists.
- **`slots.live`** (`types/xollama/config.go`, `xollama tweak model`
  `slots-live`) is the count a load starts with: `liveSlots` in
  `server/slots_live.go` (`slots-live` hook, one line in `load()` after the
  ollama#4165 cap). For a completion model `llm.WouldUseOpencoti` says
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
- **A prediction is not the launch.** `servedSequences` in `startLlamaServer`
  (`llm/llama_server.go`) drops back to one and relaunches when stock served
  after all: the artifact was missing, or the opt-in fallback retried. Fewer
  sequences than reserved only ever uses less memory.
- With `XOLLAMA_ENGINE=llamacpp` `wouldUseOpencoti` is false, so both
  decisions are upstream's byte for byte. Embedding models stay at one
  sequence on every engine.
- Guards: `TestTheSingleSequenceRuleBindsOnlyStockLlamaCpp` and
  `TestTheDenyListIsTheOnlyReasonOpencotiLifts` in
  `server/sched_single_sequence_test.go`;
  `TestAStockLaunchServesTheDenyListOneSequence` in
  `llm/engine_estimate_test.go`. Prose: `docs/xollama/slots.mdx`. Registry row
  `launch-config` in `docs/protocols/UPSTREAM-SYNC.md`.
