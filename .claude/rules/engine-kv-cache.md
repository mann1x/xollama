---
paths:
  - llm/engine_launch.go
  - llm/engine/capability.go
  - docs/xollama/kv-cache.mdx
---

# KV cache types and the sliding-window ring

- `resolveKVCacheTypes` in `llm/engine_launch.go` resolves each half
  separately, most specific first: the model's `xollama.json`, then
  `XOLLAMA_K_CACHE_TYPE` / `XOLLAMA_V_CACHE_TYPE` (and the `_SWA` pair), then
  upstream's server-wide `OLLAMA_KV_CACHE_TYPE`, then unset. Upstream's setting
  stays the default everything else overrides — never move it.
- `stockCacheTypes` is what stock llama.cpp's own parser accepts. Anything
  outside it — opencoti's `kvarn2`..`kvarn8` widths, the frozen `turbo*` /
  `*_tcq` tiers, `q6_0` — is an engine extension, and
  `requiresEngineExtension` refuses it by name rather than letting it fail deep
  in the engine's argument parser.
- The ring (`--cache-type-k-swa` / `--cache-type-v-swa`) is written by
  `appendKVCacheRingArgs` only after the engine is chosen, and only when both
  halves are set; on anything but opencoti it adds nothing.
- Whether the ring exists at all is a `feature` row on the pin, read through
  `HasSlidingWindowRing` / `featureSWACacheTypes` in `llm/engine/capability.go`.
  Never infer it from the cut number in `tag` — see `.claude/rules/engine-pin.md`.
- Engine precondition, confirmed by opencoti against `common/arg.cpp`: the ring
  requires a KVarN base on **both** `-ctk` and `-ctv`; a plain base is refused
  at startup. Refuse the same combinations before launch rather than after.
- The report against opencoti's `docs/llamafile-flags.md` is **closed**
  (2026-09-20): the live file now carries the deprecation note, the
  `kvarn2..kvarn8` tiers and the SWA pair. Do not re-file it — the history is in
  `docs/features/opencoti-config-gaps.md`.
- Prose for users lives in `docs/xollama/kv-cache.mdx`; measurements in
  `docs/evaluations/phase2-engine-ab.md`.
