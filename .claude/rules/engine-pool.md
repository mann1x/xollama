---
paths:
  - llm/engine_pool.go
  - llm/engine_pool_arch.go
  - llm/engine_launch.go
  - llm/engine_estimate.go
  - llm/llama_server.go
  - server/routes.go
---

# Shared prefix pools

- A pool is materialised from the prefix **tokens** (`POST /polykv/pools`
  with `{"tokens": …}` in `llm/engine_pool.go`), never snapshotted from a
  finished session.
- Length is the whole question: the engine takes the share only when the match
  covers the pool entirely (`P == pool_max + 1`, prompt strictly longer), so a
  pool one token too long can never be matched while a short one still works.
  Never let a pool run past the template.
- What two conversations happen to share is an upper bound, not a measurement.
  `templateBoundary` tokenises this request's probe renderings, keeps only the
  tokens every render agrees on, then clamps that against the prompts actually
  observed.
- Measure against the renderer that produced the prompt. `probeRenderings`
  asks the engine only where the engine owns the template; where ollama renders
  (any renderer, parser, harmony or a Modelfile `TEMPLATE`),
  `poolProbesForRequest` in `server/routes.go` renders `llm.PoolProbeContents`
  there and carries them on `CompletionRequest.PoolProbes`. The wrong renderer
  gives a boundary no prompt on that path has.
- Tokenise with `add_special: true` — `/tokenize` defaults it to `false` while
  the serving path uses `true`, and the off-by-BOS matches at zero.
- Models that keep recurrent state (`modelKeepsRecurrentState` in
  `llm/engine_pool_arch.go`) are pooled only where the boundary was actually
  measured — either path will do, and without one they are skipped. Seats are
  sized by `resolvePoolCount` for every architecture, read through
  `effectivePoolCount` by `llm/engine_launch.go`, `llm/engine_estimate.go` and
  `startProcess` in `llm/llama_server.go`; it returns 0 on a multimodal load,
  where the engine refuses the create outright (HTTP 501).
- Pool ids are `*int` and the engine numbers its first pool `0`; see the
  `engine-session` bullet in `.claude/rules/upstream-tree.md`.
- Cover changes in `llm/engine_pool_test.go` and
  `llm/engine_pool_boundary_test.go`
  (`TestPoolStopsAtTheTemplateNotAtWhatTwoUsersHappenedToShare`,
  `TestPoolStopsAtTheTemplateOnTheOllamaRenderedPath`,
  `TestRecurrentModelIsNotPooledWithoutAMeasuredBoundary`,
  `TestNoPoolSeatsOnAMultimodalLoad`). Prose lives in
  `docs/xollama/sessions.mdx`.
