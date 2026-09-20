---
paths:
  - llm/engine_pool.go
  - llm/engine_pool_arch.go
  - llm/engine_launch.go
  - llm/engine_estimate.go
  - llm/llama_server.go
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
  `templateBoundary` renders the leading system messages plus tools against the
  stand-in users in `templateProbes`, keeps only the tokens every render agrees
  on, then clamps that against the prompts actually observed.
- Tokenise with `add_special: true` — `/tokenize` defaults it to `false` while
  the serving path uses `true`, and the off-by-BOS matches at zero.
- Models that keep recurrent state (`modelKeepsRecurrentState` in
  `llm/engine_pool_arch.go`) are pooled from the **chat** path only, because
  the boundary can be measured just where the engine owns the template. They
  are no longer excluded from pooling, and seats are sized by
  `resolvePoolCount` for every architecture — `llm/engine_launch.go`,
  `llm/engine_estimate.go` and `startProcess` in `llm/llama_server.go` all read
  the same count.
- Pool ids are `*int` and the engine numbers its first pool `0`; see the
  `engine-session` bullet in `.claude/rules/upstream-tree.md`.
- Cover changes in `llm/engine_pool_test.go` and
  `llm/engine_pool_boundary_test.go`
  (`TestPoolStopsAtTheTemplateNotAtWhatTwoUsersHappenedToShare`,
  `TestRecurrentModelIsNotPooledFromACompletion`). Prose lives in
  `docs/xollama/sessions.mdx`.
