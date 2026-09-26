---
paths:
  - internal/council/**
  - server/council.go
  - server/council_test.go
  - server/council_polykv.go
  - server/council_polykv_test.go
  - server/council_compaction.go
  - server/council_compaction_prompts.go
  - server/council_compaction_test.go
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
- **Member tags (`council_tags_v1`).** Every thinking chunk carries
  `ChatResponse.Council` (`api.CouncilTag` in `api/xollama_council.go`,
  `council` hook in `api/types.go`): role, index, round of the one member it
  holds, from `thinkingTags.add` in `server/council.go`. Content chunks carry
  none; the headings stay for clients that read thinking as text. Clients gate
  on `XollamaIdentity.Features` (`/api/xollama`, `xollamaFeatures` in
  `server/identity.go`), never on a version — a dev build is `0.0.0`. A name
  never changes meaning; a changed contract is a new `…_v2`. Guards:
  `TestEveryThinkingChunkNamesItsMember`, `TestTheIdentityNamesTheFeatures`.
- **Client placement (`client_placement_v1`).** `ChatRequest.Placement`
  (`api.Placement` in `api/xollama_placement.go`, `council` hook in
  `api/types.go`) is for a client that builds its own pools through
  `/api/engine`. `clientPlacement(c, req)`, the line before `councilServes`
  in `server/routes.go`, hands a plain turn's `pool_id`/`num_ctx`/`num_ctx_min`
  to the engine through `councilPlacementKey`; a member's own turn keeps the
  council's. A council turn reads only `PoolID`: `createRoot` forks the
  conversation root from it when the prompt starts with the pool's tokens,
  else builds its own beside it, and never releases the client's pool. A
  different pool next turn lets the old root go (`samePool`), since the
  engine releases no pool with a child. Guards:
  `TestAPlainTurnCarriesTheClientsPlacement`,
  `TestACouncilRootStandsOnTheClientsPool`,
  `TestAClientPoolThatDoesNotMatchIsLeftAlone`,
  `TestANewClientPoolLetsTheOldRootGo`.
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
- **Compaction is Cerebriline's, ported** (Phase 8,
  `server/council_compaction.go`). Its numbers are Cerebriline's defaults,
  cited in the constants; change one only against a measurement. The
  prompts (`council_compaction_prompts.go`) are its texts adapted to chat —
  keep the structure. Rules that tests hold:
  - the system message is never changed: the summary is a user message
    after it, so the root's system part survives a fold;
  - a record per conversation, applied every turn and dropped on a hash
    mismatch; a later fold folds only what follows the summary
    (`n = prevN + …`);
  - the window is the grant only on an owned tree: an unowned tree's grant
    is per request (2 in the fake) and hung the idle fold;
  - "did it fold" is the record changing, never the message count (a fold of
    one message plus a summary keeps the length);
  - a fold that does not shrink is discarded;
  - nothing is sent that does not fit the window: the writer only when the
    measured conversation + instruction + reply fit, else the text path in
    pieces (bug-134: a 6,656 grant under a 9.5k conversation refused the
    writer three times, and a one-piece text request waited out admission
    while the next turn waited on it). The budget floor is
    `min(4096, window/8)`;
  - on an owned tree, a compaction call that does not read the root (text
    path, its reviewers, the retrospective) runs on the owner's session
    inside its window (`ownerWindow`), never on a session of its own: that
    one is booked beside the owner and is never admitted once the owner holds
    the pool (bug-135). Calls on one session take turns, and the kept root
    goes first (`dropKept`, bug-137): it holds what the fold replaces;
  - a resize's new window is `window_new`; `window` is the old one
    (bug-136). Test the engine client against opencoti's real bodies;
  - a review that cannot fork the conversation on an owned tree is skipped,
    never run unpooled (bug-138): unpooled it reads the whole conversation
    on a booking the engine never admits beside a full owner;
  - idle goroutines join `councilIdle`; a test that serves a council must
    `councilIdle.Wait()` before it reads the fake, or `-race` fires. Folds
    go through `councilCompacting` (`singleflight`).
  Guards in `server/council_compaction_test.go`.
- **The conversation is held once.** The planner runs attached to P1
  (`buildRoot`, before its first call), never with its own copy beside it:
  two copies filled the owner's tree at ~45 % of the window, so compaction
  never fired and refused pools stranded the turn. The first turn's P1 is
  unowned and needs `pool_unowned_v1`; `promoteRoot` gives the owner its own
  while idle, and the next turn `councilRoots.wait`s for it (built beside the
  turn's own, the two filled the window — measured). A kept P1 is adopted
  only while the owner's allocation lives and on the same runner: a closed
  allocation takes its pools with it, and the id may name another's. The
  engine refuses to release a pool with a child, so extending forks and
  keeps the parent; cap the chain (`councilRootChain`) and rebuild on
  recurrent models. The compaction writer is a turn of the conversation on
  the root, never the old turns pasted anew.
  `llm.ErrSessionFull` ("compact the session", a 503) compacts and retries
  the root once. Guards in `server/council_polykv_test.go`.
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
- **A token budget only where it is understood** (`councilTakesBudget`,
  `server/council_remote.go`). ollama.com refuses a numeric `think` ("think
  must be a boolean or string"), and stock ollama has no budget. Cloud is what
  the server reports (`Config.RemoteHost`/`RemoteModel`, `/api/show`
  `remote_host`), never the name alone: a pulled cloud tag can be called
  anything (cline's rule). Another server is asked `/api/xollama`
  (`api.IsXollama`) and `/api/show`, cached 5 min in `councilProbes`; any
  doubt sends `think: true`, which every server accepts. A cloud *reference*
  (`…-cloud`, `…:cloud`) is cloud by name on every server: its `/api/show` is
  answered by ollama.com with no `remote_host` (measured on eleven2go).
- **A role that does not think sends `think: false`, never nil**, on every
  model: nil on a thinking model means true upstream, and `qwen3:8b` spent its
  whole 384-token cap reasoning and answered nothing. False is accepted by a
  model that cannot think.
- **A remote reply is whole only with its `done` line.** A dropped connection
  ends the stream without an error; `remote()` fails it, and the runner treats
  an empty reply from a member elsewhere as a failure too.
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
- The launch adds `councilPoolSeats` (`2 + 2 × rounds` + `councilRootChain`) pool seats, and only
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
