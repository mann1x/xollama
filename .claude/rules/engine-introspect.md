---
paths:
  - llm/engine_introspect.go
  - llm/engine_introspect_test.go
  - server/routes_engine.go
  - server/routes_engine_test.go
  - docs/xollama/introspection.mdx
---

# `/api/engine` — the engine's own surface

- `llm/engine_introspect.go` and `server/routes_engine.go` are additive. The
  `engine-introspect` hook is the `r.GET`, `r.POST` and `r.DELETE("/api/engine",
  s.EngineHandler)` lines in `server/routes.go` plus the `llama` field on
  `loadedModel` in `server/sched.go`. Registry row `engine-introspect` in
  `docs/protocols/UPSTREAM-SYNC.md`.
- **The endpoint is a name from `engineRoutes`, never a path the caller
  composes.** The table is keyed by method and mirrors opencoti's own route
  registration; a `{id}` segment must match `engineIDSegment` (the engine's id
  alphabet — no `/`, `%` or whitespace; an id with those goes in the body of
  `sessions/close` / `sessions/resize`). `ValidEngineEndpoint` refuses anything
  else with a 400 before a connection is opened. A route the engine gains gets
  its row here, with its method, in the same commit.
- **Inference stays out.** `/completion`, the OpenAI and Anthropic shapes,
  embeddings, rerank and infill are served by xollama through the scheduler; a
  request behind its back is work the scheduler cannot see. `/cors-proxy` and
  `/tools` fetch caller-chosen URLs and stay out too. Held by
  `TestInferenceStaysBehindTheScheduler` and
  `TestEngineCallRefusesWhatXollamaServesItself`.
- `EngineIntrospector` is an optional interface, type-asserted at the call
  site, never a method on `LlamaServer` — only the llama-server family has an
  HTTP surface. `EngineDo` hands back the engine's `*http.Response` unaltered:
  a 404 from stock llama.cpp on an opencoti route is the honest answer, not an
  error. The caller closes the body, which cancels the timeout (`cancelOnClose`).
- Timeouts: `engineReadTimeout` for a GET, `engineControlTimeout` for a control
  call that may prefill, none for the `/polykv/tps` stream unless `?once=1`.
  The handler passes `text/event-stream` through event by event, reads any
  other body through a 4 MiB limit and caps a request body at `engineBodyLimit`.
- `?model=` and `?endpoint=` are xollama's; every other query parameter is
  forwarded (`?live=1`, `?once=1`). The list form (no `?model=`) is GET only.
- These are the engine's own controls with no guard of ours in front: closing
  a session or releasing a pool a running chat or council uses takes its cache
  away mid-turn. `docs/xollama/introspection.mdx` says so in a Warning; keep it.
- Guards: `llm/engine_introspect_test.go` (`TestEveryOpencotiRouteIsReachable`,
  `TestEngineDoValidatesBeforeDialing`, `TestEngineDoOnTheWire`) and
  `server/routes_engine_test.go` (`TestEngineControlReachesTheEngine`,
  `TestEngineStreamsArePassedThrough`,
  `TestEngineCallForwardsTheQueryButNotOurOwn`).
