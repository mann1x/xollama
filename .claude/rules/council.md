---
paths:
  - internal/council/**
  - server/council.go
  - server/council_test.go
  - server/council_polykv.go
  - server/council_polykv_test.go
  - server/council_remote.go
  - server/council_remote_test.go
  - llm/engine_council.go
  - llm/engine_council_test.go
  - llm/engine_window.go
  - llm/engine_window_test.go
  - cmd/council_run.go
  - cmd/council_run_test.go
  - app/ui/council.go
  - app/ui/council_test.go
  - app/ui/app/src/hooks/useCouncil.ts
  - app/ui/app/src/components/CouncilBadge.tsx
  - app/ui/app/src/components/DeliberationButton.tsx
  - docs/xollama/council.mdx
  - docs/features/council.md
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
  `ChatHandler`, thinking off unless its role states `council.<role>.think`.
  `on` is `xollama.DefaultCouncilThinkBudget` (2048) tokens, never a level:
  `medium` at 131k let a member loop past 29k tokens live.
  A thinking role is sent an explicit **token** budget
  (`council.ThinkBudget(setting, cm.window)`), never a level: a level is a
  share of `num_predict`, which for a member is its reply cap. `num_predict`
  becomes `max_tokens` + budget. `Stream` reads only `Message.Content`, so
  the reasoning is dropped. The route-only decision never carries `think`.
  The cap message is the model's `think_budget_message`; never set one here. Each parallel member gets its own engine session
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
  `TestEveryParallelMemberHasItsOwnSession`. Prose: `docs/xollama/council.mdx`
  (users), `docs/features/council.md` (maintainers), `docs/xollama/tweak.mdx`
  (setting it).
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
  that gives back at least 4096 cells or 10 %.
- **Compaction follows pressure** (Phase 6): the owner's raw `/kv` pressure at
  `compact_at` (0.85) compacts before a turn; after the answer,
  `councilIdleCompact` summarises at `idle_compact_at` (0.75) for the next
  message. Idle goroutines join `councilIdle`; a test that serves a council
  must `councilIdle.Wait()` before it reads the fake, or `-race` fires.
  Summaries go through `singleflight`, never twice for one conversation.
- **`num_ctx 0` is the whole pool on PolyKV only** (`polykv-window` hook,
  `llm/engine_window.go`). With `pool_unowned_v1` the tree is `unowned`: pools
  carry `"unowned": true`, the planner no placement, and `begin`/`finish`
  never resize the owner. Never let 0 mean this on stock llama.cpp or on
  opencoti without pools: there upstream clamps it to 4.
- **Roles elsewhere (Phase 7).** `council.<role>.model` reaches a cloud model
  through `ChatHandler`'s own cloud branch; `council.<role>.host` (needs
  `model`) is served by `server/council_remote.go` over `api.Client`, with no
  session, placement or pool. A host is called only when
  `XOLLAMA_COUNCIL_HOSTS` lists it: a pulled council would otherwise send every
  conversation to whatever it names. Never relax that to "any" by default.
  The fallback rule lives in `internal/council` `call`: a researcher or critic
  that failed on another model or host is rerun on the council's model; a
  planner or synthesizer failure is the turn's. A canceled turn never retries.
- **`slots.live`** replaces `OLLAMA_NUM_PARALLEL` only when opencoti serves
  (`server/slots_live.go`, `slots-live` hook). Never set the parallel env for
  a council on opencoti: `-c` is `num_ctx × slots`, and 4 × 131k did not fit.
- **Recurrent-state models.** When `/kv` reports an `rs` block (`begin` reads
  it on every turn, including the first, before any booking exists) each
  pool holds one of a few state cells. Building a new layer first releases
  every layer that has no worker on it, no child and is not the
  conversation's; the new layer then forks P1. Without this a 131k turn
  deadlocked on its synthesizer (bug-118). Guard:
  `TestARecurrentModelReleasesEachStageItHasFinished`. A released layer is
  never a parent and never released again at the turn's end.
- The launch adds `councilPoolSeats` (`2 + 2 × rounds`) pool seats, and only
  for a council whose `polykv` is not `off`. Guards:
  `server/council_polykv_test.go` (a fake engine records the tree) and
  `llm/engine_council_test.go`. Measured result: plan Phase 4.
- **Chat only.** The hook is in `ChatHandler`; `/api/generate` and
  `/v1/completions` serve a council model as the plain model. Upstream's
  one-shot `xollama run <model> "…"` uses `/api/generate`, so `RunHandler`
  sends it through `chat()` when `/api/show` says the council is on
  (`runsAsCouncil` / `runCouncilOnce` in `cmd/council_run.go`, one `council`
  hook line in `cmd/cmd.go`); `--format` keeps generate. Guarded by
  `cmd/council_run_test.go`. Found by the Phase 5 CLI walkthrough, where the
  one-shot run silently answered without the council.
- **Desktop app (option C, 2026-09-26).** The app does not change what a
  council is; it only shows it:
  - a `CouncilBadge` in the model picker, from `/api/show`
    `xollama.council.enabled` (`useIsCouncil`). Only pulled local models are
    asked;
  - a `DeliberationButton` in place of upstream's think buttons, whose flags
    are false for a council. It is on by default, like `show_deliberation`,
    and kept per browser in `localStorage`, never in upstream's
    `ThinkEnabled`, which defaults to off.
  The app backend drops every `think:false` ("only set Think if it's actually
  requesting thinking"), so `councilThink` (`app/ui/council.go`, `council`
  hook in `app/ui/ui.go`) puts it back for a council only. `app/ui` is built
  only for Windows and macOS; `council.go` has no build tag so its test runs
  on Linux. Never a per-chat council switch or a write of the model's config
  from the UI: those were options A and B, and the owner chose C. Do not
  Prettier-format upstream's `ChatForm.tsx`; it rewrites about 160 lines.
