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
  ollama#4165 cap) replaces `OLLAMA_NUM_PARALLEL` with it only for a
  completion model `llm.WouldUseOpencoti` says opencoti will serve. Stock
  llama.cpp reserves a KV copy per slot at launch, so there the operator's
  count stands. `slots.live` above `slots.max` is refused by `Validate`.
  Guard: `server/slots_live_test.go`; Registry row `slots-live`.
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
