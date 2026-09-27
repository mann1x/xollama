---
paths:
  - llm/engine_context_window.go
  - llm/engine_admission.go
  - llm/llama_server.go
  - server/context_window.go
  - server/context_window_test.go
  - server/council.go
  - server/council_polykv.go
  - server/routes.go
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
- **Only a client that states `placement.num_ctx` negotiates.** That is
  `clientPlacement` calling `llm.WithNegotiation`; a council turn never reaches
  it. A negotiating request gets the engine's 429 at once, with
  `X-Context-Largest-Admissible` and `Retry-After` (at least 1 s, rounded up).
  Everything else keeps ollama's queue: `postWaitingForAdmission` waits.
- **`options.num_ctx` reloads the model.** It is part of the load; the window
  is `placement.num_ctx`. Measured on b137: the "continuation ignores num_ctx"
  looked broken until the runner restarts were counted.
- Registry row `context-window`; feature `context_window_v1`; docs
  `docs/xollama/sessions.mdx`.
