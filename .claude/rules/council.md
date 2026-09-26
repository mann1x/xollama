---
paths:
  - internal/council/**
  - server/council.go
  - server/council_test.go
  - server/council_polykv.go
  - server/council_polykv_test.go
  - llm/engine_council.go
  - llm/engine_council_test.go
  - plans/agentic-council-chat.md
  - plans/council-eval/**
---

# Serving a council turn

- `internal/council/` is the runner and knows nothing about HTTP, the
  scheduler or the engine: a `Model` makes each member's call. `Run` in
  `internal/council/run.go` goes `Decide` (route-only) → `Direct`, or plan →
  researchers ∥ → critics ∥ → synthesizer with a bounded revise loop, on
  `errgroup`; the first member error cancels the rest. It is the runner the
  Phase 1 bake-off chose over eino, langgraphgo and trpc-agent-go
  (`plans/agentic-council-chat.md`); `plans/council-eval/` is its own Go
  module and never touches xollama's `go.mod`.
- `DefaultCharter` in `internal/council/steps.go` matches the Phase 0 probe
  (`plans/council-eval/probe/council_tree.py`). Change its bytes and the
  measured numbers no longer describe the shipped prompt — re-measure.
- `server/council.go` is additive; the hook is one `if councilServes(...)` in
  `ChatHandler` (`server/routes.go`), after the remote-model branch and before
  the capability checks. Registry row `council` in
  `docs/protocols/UPSTREAM-SYNC.md`.
- `councilServes` is false for a model without an enabled council, a request
  with no messages, tools or a `format` (the client is steering the output
  itself), and any member's own turn. Members are marked with the
  `councilMemberKey` gin context key — never a header, so no client can set it
  and no member can convene the council again.
- Every member is an ordinary chat turn served in process through
  `ChatHandler`, thinking off. Each parallel member gets its own engine session
  named under the conversation's (`<session>~researcher-1`); the planner keeps
  the conversation's session so a direct answer hits the same cache as a plain
  chat. Deliberation streams as thinking, the answer as content, through
  `writeChatResponse`; `think: false` or `council.show_deliberation off` sends
  the answer alone.
- Parallel members need parallel slots: on stock llama.cpp the ollama#4165
  architectures take turns, on opencoti they run at once — see
  `.claude/rules/dynamic-slots.md`.
- Guards in `server/council_test.go`: `TestToolsAndFormatBypassTheCouncil`,
  `TestAModelWithoutACouncilIsUntouched`,
  `TestEveryParallelMemberHasItsOwnSession`. Prose: `docs/xollama/tweak.mdx`
  ("How a council turn runs").
- **PolyKV (Phase 4).** `server/council_polykv.go` builds one tree per turn
  when the runner implements `llm.PolyKV` (opencoti, `polykv_subpools_v1` +
  `kv_status_v1`, `CouncilPools > 0`, affinity on). The planner is the owner:
  it books the window on the conversation's session. P1 (the conversation),
  P2r (+ plan), P2f (+ findings) and P3s (+ critiques) are each built once,
  forked from the longest prefix already built, and pinned to the owner.
  Workers attach with `pool_id` and no window, and are closed when done. Pools
  are released newest first, and the owner is never closed.
- A layer is the rendered prompt up to `councilSentinel`, and must be a byte
  prefix of the member's own rendered prompt, or the member runs unpooled. Pool
  id 0 is valid: `Placement.PoolID` is `*int` and never compared `> 0`.
- The owner's window follows `/kv` pressure. `begin` grows it back when nothing
  is refused. `finish` shrinks it, deferred, only under pressure and only if
  that gives back at least 4096 cells or 10 %. Compaction folds the old turns
  into the system message past `compact_at` of the grant.
- The launch adds `councilPoolSeats` (`2 + 2 × rounds`) pool seats, and only
  for a council whose `polykv` is not `off`. Guards:
  `server/council_polykv_test.go` (a fake engine records the tree) and
  `llm/engine_council_test.go`. Measured result: plan Phase 4.

