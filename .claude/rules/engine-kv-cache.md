---
paths:
  - llm/engine_launch.go
  - llm/engine_ring_shape_test.go
  - llm/llama_server.go
---

# KV cache types and the sliding-window ring

- `resolveKVCacheTypes` in `llm/engine_launch.go` builds one `kvCacheTypes`: the
  whole-cache types `appendKVCacheArgs` writes as `-ctk` / `-ctv`, which both
  engines accept, and the ring `KSWA` / `VSWA`, which `appendKVCacheRingArgs`
  writes only after opencoti was the engine actually chosen.
- Two refusals guard the ring, in this order inside the
  `// xollama-hook: launch-config` region of `llm/llama_server.go`:
  `engine.SlidingWindowRingUnavailable()` asks whether the pinned artifact has
  the feature at all, then `kv.ringShapeError()` asks whether this shape of it
  would be accepted. Available is not askable — the engine takes the flags,
  states the problem and exits during startup, which reaches an operator as a
  model that would not load, so refuse where the setting was made.
- The shape rules were MEASURED against the pinned bytes, not read off the
  vendor's flag table, which states only the second of them:
  - a ring needs a KVarN base — `kv.k` and `kv.v` must both be a `kvarnN` width
    (`isKVarNCacheType`); an `f16` or `q8_0` base is refused;
  - `kv.k_swa` and `kv.v_swa` are set together and are either both KVarN or both
    plain; half a ring and a mixed ring are both refused.
- Re-probe, never re-reason, when `llm/engine/pin.txt` moves: run the artifact as
  `<artifact> --server <flags> --model /nonexistent.gguf`, and read reaching
  `failed to load model` as accepted, anything earlier as refused. Every row of
  `TestRingShapeMatchesWhatTheEngineAccepts` in `llm/engine_ring_shape_test.go`
  is one such run; the probe date and artifact tag are recorded there and above
  the `feature swa-cache-types` row in `llm/engine/pin.txt`.
- `llm/llama_server.go` is upstream and on the read-line-by-line list: keep the
  check inside the existing hook region, and keep it inert when opencoti was not
  chosen. See `.claude/rules/upstream-tree.md` and `.claude/rules/engine-pin.md`.
