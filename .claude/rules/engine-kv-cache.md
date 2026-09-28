---
paths:
  - llm/engine_launch.go
  - llm/engine_ring_shape_test.go
  - llm/engine_placement.go
  - llm/engine/capability.go
  - llm/llama_server.go
  - server/placement_opencoti.go
  - server/placement_opencoti_test.go
  - docs/xollama/kv-cache.mdx
---

# KV cache types and the sliding-window ring

- `resolveKVCacheTypes` in `llm/engine_launch.go` resolves each half
  separately, most specific first: the model's `xollama.json`, then
  `XOLLAMA_K_CACHE_TYPE` / `XOLLAMA_V_CACHE_TYPE` (and the `_SWA` pair), then
  upstream's server-wide `OLLAMA_KV_CACHE_TYPE`, then unset. Upstream's setting
  stays the default everything else overrides — never move it.
- `resolveKVCacheTypesOn` with `stock` true is the same for a load stock
  llama.cpp serves after all: a server-wide `XOLLAMA_*` type stock rejects is
  dropped (the ring always), so that half keeps the legacy base
  (`OLLAMA_KV_CACHE_TYPE` / `XOLLAMA_KV_CACHE_TYPE`) — `startLlamaServer`
  relaunches with `stockKV`. A model's own kv setting is still refused.
- **Placement follows the same line** (`opencoti-placement` hook). A model whose
  own `kv.k` / `kv.v` only opencoti runs (`llm.NeedsOpencoti`,
  `llm/engine_placement.go`) is placed only on the GPU groups opencoti serves:
  `opencotiPlacement` in `server/placement_opencoti.go` filters the list
  `server/sched.go` hands to `selectLlamaServerPlacement`. A server-wide
  `XOLLAMA_K_CACHE_TYPE` does not count. No such setting, no opencoti group, or
  `XOLLAMA_ENGINE=llamacpp`: the list is unchanged. Guard:
  `TestAModelOnlyOpencotiServesIsPlacedWhereOpencotiRuns`; prose in
  `docs/features/device-selection.md`.
- `stockCacheTypes` is what stock llama.cpp's own parser accepts. Anything
  outside it — opencoti's `kvarn2`..`kvarn6`/`kvarn8` (**no `kvarn7`**: structural,
  per `llama_kvarn_valid_bits()`), the frozen `turbo*` / `*_tcq` tiers, `q6_0` — is
  an engine extension, and `requiresEngineExtension` refuses it by name rather
  than letting it fail deep in the engine's argument parser. `KnownCacheTypes`
  lists only widths measured against the pinned artifact's own parser.
- KVarN needs flash attention: `cacheShape` (`cmd/tweak/reconcile.go`) refuses
  `CacheNeedsFlashAttention` types with `flash_attention` `off`, never `auto`.
- The ring (`--cache-type-k-swa` / `--cache-type-v-swa`) is written by
  `appendKVCacheRingArgs` only after the engine is chosen, and only when both
  halves are set; on anything but opencoti it adds nothing.
- Whether the ring exists at all is a `feature` row on the pin, read through
  `HasSlidingWindowRing` / `featureSWACacheTypes` in `llm/engine/capability.go`.
  Never infer it from the cut number in `tag` — see `.claude/rules/engine-pin.md`.
- Engine precondition, confirmed by opencoti against `common/arg.cpp`: the ring
  requires a KVarN base on **both** `-ctk` and `-ctv`; a plain base is refused
  at startup, and half a ring or a mixed ring is refused too. Refuse the same
  combinations before launch rather than after.
- Two refusals guard the ring, in this order inside the
  `// xollama-hook: launch-config` region of `llm/llama_server.go`:
  `engine.SlidingWindowRingUnavailable()` asks whether the pinned artifact has
  the feature at all, then `kv.ringShapeError()` asks whether this shape of it
  would be accepted. Keep both inside the hook region and inert when opencoti
  was not chosen — see `.claude/rules/upstream-tree.md`.
- Re-probe, never re-reason, when `llm/engine/pin.txt` moves: run the artifact as
  `<artifact> --server <flags> --model /nonexistent.gguf`, and read reaching
  `failed to load model` as accepted, anything earlier as refused. Every row of
  `TestRingShapeMatchesWhatTheEngineAccepts` in `llm/engine_ring_shape_test.go`
  is one such run; the probe date and artifact tag are recorded there and above
  the `feature swa-cache-types` row in `llm/engine/pin.txt`.
- The report against opencoti's `docs/llamafile-flags.md` is **closed**
  (2026-09-20); do not re-file it — see `docs/features/opencoti-config-gaps.md`.
- Prose: `docs/xollama/kv-cache.mdx`; numbers: `docs/evaluations/phase2-engine-ab.md`.
