---
paths:
  - internal/council/**
  - server/council.go
  - server/council_test.go
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
