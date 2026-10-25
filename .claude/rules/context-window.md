---
paths:
  - llm/engine_context_window.go
  - llm/engine_admission.go
  - llm/engine_council.go
  - llm/engine_ctx_hold.go
  - llm/llama_server.go
  - server/context_window.go
  - server/context_window_test.go
  - server/xollama_ctx_hold.go
  - server/council.go
  - server/council_polykv.go
  - server/routes.go
  - api/xollama_placement.go
  - docs/xollama/sessions.mdx
---

# The engine's window, passed on

- `X-Context-Window` is **opencoti's**. xollama passes it on and never invents
  one. No header means no guaranteed window, and stock llama.cpp must stay
  header-free: off means off.
- **Never write response headers from the runner goroutine.** The runner
  reports into the `llm.ContextWindow` collector on the request context. The
  `windowWriter` from `exposeContextWindow` sets the header before the first
  byte, on the handler's goroutine. `streamResponse` touches the header map
  concurrently otherwise.
- **First report wins.** A council's members and a structured-output second
  pass run inside the first admission's window. The council reports its owner's
  grant after `begin` (`tree.ownerGrant()`).
- **Only a client driving the engine itself negotiates**: `placement.num_ctx`,
  or `placement.pool_id >= 0` (a worker of its own pool tree; pool 0 is real).
  That is `clientPlacement` calling `llm.WithNegotiation`; a council turn never
  reaches it. A negotiating request gets the engine's 429 at once, checked
  BEFORE `neverFits` (the client can grow its owner), with `Retry-After` (at
  least 1 s, rounded up) and `X-Context-Largest-Admissible` only when the
  engine named one. **A named 0 is sent**: `llm.NoLargestAdmissible` (-1) is
  "the engine did not say", 0 is "nothing is free", and a client releases a
  window of its own on the second (unsent, Cerebriline waited five minutes on
  pandorum, 2026-10-11). Guards: `TestARefusalWithNothingFreeSaysZero`,
  `TestARefusalWithoutALargestWindowSaysNone`. Everything else keeps ollama's queue:
  `postWaitingForAdmission` waits.
- **An unbooked window** (`context-window-unbooked` hook, owner 2026-10-15):
  `Placement.NumCtxHold` (`api/xollama_placement.go`) goes to the engine as
  `num_ctx_hold` only beside a placed window, on opencoti declaring
  `elastic_window_unheld_v1` (`ctxHoldFor`, `llm/engine_ctx_hold.go`); nil
  otherwise, so every other body is unchanged. `/api/xollama` lists
  `context_window_unbooked_v1` only when `unbookedServed()`
  (`server/xollama_ctx_hold.go`). Guards
  `TestNumCtxHoldGoesOnlyBesideAWindowToAnEngineThatTakesIt`,
  `TestUnbookedWindowsAreAdvertisedOnlyWhenTheEngineTakesThem`.
- **A queued engine request gives up.** `engineRequestQueued`
  (`llm/engine_council.go`) waits out the engine's 429s for at most
  `engineQueueBudget` (`admissionRetryBudget`), then fails with `errNoAdmission`
  naming the request and the last refusal, at Warn — never until the client
  gives up. Guard: `llm/engine_council_queue_test.go`.
- **`ErrNeverFits` must be mapped** (400, the numbers in the message) in
  `Completion` and `Chat`. Unmapped it fell into "model runner has
  unexpectedly stopped" and lost the reason (found 2026-09-27).
- **`options.num_ctx` reloads the model.** It is part of the load; the window
  is `placement.num_ctx`. Measured on b137: the "continuation ignores num_ctx"
  looked broken until the runner restarts were counted.
- Registry rows `context-window` and `context-window-unbooked`; features
  `context_window_v1`, `context_window_unbooked_v1`; docs
  `docs/xollama/sessions.mdx`.
