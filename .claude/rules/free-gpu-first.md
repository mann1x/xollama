---
paths:
  - server/evict_last.go
  - server/evict_last_test.go
  - server/xollama_batch_unknown.go
  - server/xollama_batch_unknown_test.go
  - server/xollama_attached_vram.go
  - server/xollama_attached_vram_test.go
  - llm/engine_draft_head.go
  - llm/engine_draft_head_test.go
  - llm/engine_vram_estimate.go
  - llm/engine_vram_estimate_test.go
  - llm/llama_server.go
  - server/sched.go
  - docs/features/device-selection.md
---

# Eviction is the last resort on opencoti (`evict-last` hook)

- `server/evict_last.go` and `llm/engine_vram_estimate.go` are additive. The
  hook is marked lines in `server/sched.go`: the four `predictVRAM(...)` calls
  where upstream has `llm.PredictServerVRAM(...)` (in `load`, the auto-context
  retry and the two mmap decisions), `needsEviction(...)` as the condition of
  the eviction pre-flight in `load`, in `processPending`
  `busy := s.busyGPUs(gpus)` after `updateFreeSpace` and
  `findRunnerToUnloadOn` in place of `findRunnerToUnload`, and
  `s.waitForHostMemory(runner)` at the top of `waitForVRAMRecovery`. Registry row
  `evict-last` in `docs/protocols/UPSTREAM-SYNC.md`.
- **Why.** Upstream predicts a load as its file plus an f16 KV cache on every
  block at the whole context and evicts when that passes 80% of free memory.
  Measured on eleven2go (RTX 3090 + RX 9070 XT, 2026-10-11): 42.7 GiB predicted
  for a load that takes 21.5, so two clients evicted each other's model on
  every request while the 9070 XT sat empty.
- **Off means off.** `predictVRAM` is upstream's `llm.PredictServerVRAM` plus
  `slotCeilingVRAM` unless `llm.EveryGroupOnOpencoti` says opencoti serves the
  load on every backend the placement may pick; `needsEviction` is upstream's
  `predicted > free*80/100` unless `llm.WouldUseOpencoti`;
  `XOLLAMA_ENGINE=llamacpp` changes nothing.
- **The prediction is the engine's arithmetic on the GGUF**
  (`llm.PredictOpencotiVRAM`, an `OpencotiVRAM` with `Total()`): weights, the
  KV cache in the load's cache type on only the blocks that keep one, the
  sliding-window cache, the drafter, a hybrid model's recurrent state (`State`,
  one row per sequence, times `1 + draftDepth` with a built-in drafter), and a
  1 GiB reserve. The reserve is a
  constant fitted to two loads, not derived.
- **What is attached to the model is priced too** (`attached-vram` hook,
  `server/xollama_attached_vram.go`, `llm/engine_draft_head.go`): `predictVRAM`
  adds `attachedVRAM` (an attached drafter's file and draft context), `load`
  adds `drafterBatchVRAM(...)` to `predictedForLoad`, and
  `req.drafterAwareBatch(...)` (after `batch-unknown-memory`) steps the
  automatic `num_batch` down until the drafter's share and `projectorVRAM`
  fit. The projectors (opencoti loads them at the first image) are headroom
  for the batch only, never in the prediction that places and evicts. The
  draft context is priced from the engine's own last report for that drafter
  (`w.noteDraftHead(line)` first in `parseLineLocked`, `llm/llama_server.go`;
  `llm.DraftHeadContext`, keyed by blob name), scaled to a longer context
  only; before a report, the solidPC line (5 MiB plus 2 per 32768 of
  context). Since c14 r16 (0851, our #2311) the head's draft context has
  max(32, drafting sequences) rows and does not grow with `num_batch`: 6 MiB
  at 32768, 20 at 256000, at 512 and 2048 alike (solidPC 3090). Under r15 it
  was the target's batch: 76 to 430 MiB on solidPC, 2338/4424 on eleven2go,
  priced linear in the batch until `v0.40.16-xollama.1`. Without
  it an IQ2 got 2048 and the Q4_K_M 1024 (mail #2308). Stock llama.cpp keeps
  upstream's numbers. Guards: `TestTheLighterQuantStaysLighter`,
  `TestTheEnginesHeadReportIsRecorded`, `TestDraftHeadContextBeforeAReport`,
  `TestDraftHeadContextFromTheEnginesReport`,
  `TestTheProjectorIsNotInTheLoadPrediction`, `TestStockEngineBatchIsUpstreams`.
- **A stated `kv.pool_cells` is priced as launched** (`pool-cells` hook):
  `predictVRAM` passes its context through `llm.PoolCellsFor`
  (`llm/engine_pool_cells.go`), the same `-c` the opencoti launch gets; a load
  stock llama.cpp may serve keeps upstream's `num_ctx x num_parallel`.
- **A load evicts only when its weights do not fit; the KV cache never
  evicts.** `needsEviction` evicts when more than `maxWeightsOffCardPercent`
  (20; owner, 2026-10-11) of the weights would not fit the card. Below that it
  loads, the engine keeps the rest in host memory (the rolling window), and a
  load that does not fit whole says so at Info. Never a guessed threshold.
- **Where the GPU's memory is the host's, a load that does not fit whole
  evicts** (`sharesHostMemory`: every device `Integrated`; Apple silicon):
  `needsEviction` is `predicted > free` there, since there is no other memory
  to keep the rest in (Mac mini, 2026-10-13: every decode failed with
  "Compute error."). After an unload there `waitForHostMemory` waits until
  three quarters of the runner's memory is back or `waitForRecovery` has
  passed; nil for any runner that is not opencoti on shared memory.
- **`busyGPUs` reads the whole device list, before a pin or the GPU policy
  narrows it.** `gpuKey` is the PCI ID when discovery has one, so the CUDA and
  Vulkan copies of one card are one GPU and a runner on the card's other
  backend still counts; else backend and index. Never move the call below
  `selectModelDevices`.
- **An eviction takes a runner on the load's own GPUs**
  (`findRunnerToUnloadOn`): a runner on another card frees nothing the load can
  use. With none there, or a load stock llama.cpp serves, it is upstream's
  `findRunnerToUnload`. `busy` was read before the load was tried, so a runner
  that has gone since (`s.loaded[r.modelKey] != r`) is not waited for.
- **Unread free memory keeps the default batch on opencoti**
  (`batch-unknown-memory` hook, one marked line in
  `applyAutomaticGenerationBatch`): `batchForUnknownMemory`
  (`server/xollama_batch_unknown.go`) returns
  `llamaServerGenerationBatchDefault` when `availableMemory` is 0; upstream
  read 0 as room for any batch (pandorum 2026-10-16: 2048 instead of 512, and
  the drafter dropped). Stock llama.cpp keeps upstream's answer. Guard
  `TestUnknownFreeMemoryKeepsTheDefaultBatchOnOpencoti`.
- Guards in `server/evict_last_test.go`:
  `TestALoadEvictsOnlyWhenItsWeightsDoNotFit`,
  `TestARunnerIsOnBothBackendsOfItsCard`, `TestTheEvictedRunnerIsOnTheLoadsGPUs`,
  `TestSchedLoadsBesideALoadedModelWhenOnlyTheCacheIsShort`,
  `TestALoadThatDoesNotFitEvictsWhereTheGPUSharesHostMemory`,
  `TestAnUnloadOnSharedMemoryWaitsForTheHostsMemory`,
  `TestAStatedPoolIsWhatThePredictionPricesTheCacheAt`; the engine's
  figures to the MiB in `llm/engine_vram_estimate_test.go`
  (`TestTheEstimateIsTheEnginesRecurrentState`). Prose:
  `docs/features/device-selection.md`.
