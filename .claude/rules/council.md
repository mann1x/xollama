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
  - server/council_state.go
  - server/council_state_test.go
  - server/council_continue.go
  - server/council_continue_test.go
  - server/council_tools.go
  - server/council_tools_test.go
  - server/council_cloud.go
  - server/council_cloud_test.go
  - api/xollama_tools.go
  - api/xollama_tools_test.go
  - llm/engine_council.go
  - llm/engine_council_test.go
  - llm/engine_council_slots.go
  - llm/engine_council_slots_test.go
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
- **The prompt layout (9.3) is one shared prefix.** Every member sends an
  explicitly empty system message (explicit, or ChatHandler adds the model's
  SYSTEM), then the conversation. The charter opens the route and plan
  requests (`routeRequest`, `planMsg`); without it the route decision sends
  real questions direct (measured: 2 of 6). `Config.System` (the client's,
  else the model's system prompt) goes only to the synthesizer and a direct
  answer, after their role. `conversationEnd` finds the council's first message
  with `council.IsPlannerRequest`, never a bare `HasPrefix("ROLE: PLANNER.")`:
  the charter now precedes it.
- **Keep only a root the engine gave the owner** (`PoolInfo.OwnedBy`). The
  engine answers `owner: null` -- and creates the pool anyway -- when the
  asked session holds no live allocation, and it releases only *owned* pools
  when a session ends. An unowned root kept in `councilRoots` leaks a pinned
  pool seat forever (bug-141: three orphans on b133, half of `pools_max` 6,
  from idle promotions after a client closed its sessions). `buildRoot` keeps
  a root only if owned; `promoteRoot` releases one that came back unowned.
- `server/council.go` is additive; the hook is one `if councilServes(...)` in
  `ChatHandler` (`server/routes.go`), after the remote-model branch and before
  the capability checks. Registry row `council` in
  `docs/protocols/UPSTREAM-SYNC.md`.
- `councilServes` is false for a model without an enabled council, a request
  with no messages, tools without `council_chat_state` or a `format` (the
  client is steering the output itself), and any member's own turn. Members
  are marked with the
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
  Workers attach with `pool_id` and no window. A council is a living thing
  until its client leaves (the owner's ruling, 2026-09-28): `leaveWorker`
  only takes a worker off its layer, and its session stays open across calls,
  trips and turns, so the engine resumes it from its own cache (opencoti #526;
  closed after every call, a resumed synthesizer re-prefilled ~18k of 21k
  tokens, ab-4). `closeSessions` closes the turn's member sessions only when
  the request context ended, i.e. the client left. Pools are released newest
  first, and the owner is never closed. Guards:
  `TestAResumedMemberReattachesToItsStage`,
  `TestAClientThatLeavesClosesItsCouncilsSessions`.
- A layer is the rendered prompt up to `councilSentinel`, and must be a byte
  prefix of the member's own rendered prompt, or the member has no layer. On
  an owned tree it then runs on the owner's session inside its window
  (`ownerWindow`), one at a time under `councilTree.onOwner` (`place` returns
  the unlock); booked on its own session it is never admitted beside a full
  owner (b137). Only an unowned tree runs it unpooled. Pool
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
    `min(4096, window/8)`. The trigger is capped by `compactionWriterTrigger`
    so the writer fits when it fires (conversation + instruction + largest
    budget), never below half the window;
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
- **A council starts with a slot per parallel member** (`CouncilSlots`, from
  `councilSlots` in `server/council_polykv.go`; `councilLive` /
  `councilSlotArgs` in `llm/engine_council_slots.go`, `council` hooks in
  `llm/llama_server.go` and `llm/engine_estimate.go`). Workers are charged to
  the owner, so they need no cells, but each needs a slot: with `-np 1` the
  engine deferred researcher 2 until researcher 1 released slot 0 (ab-3,
  opencoti #501), and the elastic controller did not grow (3 s saturation +
  512 MiB free-VRAM guard). `-np` = the engine's parallel ceiling
  (`resolveSlotPlan(...).Max`: slots.max, `XOLLAMA_MAX_PARALLEL` or 4), never
  below the council's local width (owner's ruling 2026-09-28: a council
  follows the engine's parallel slots). `-c` is unchanged (never × the width),
  and `--kv-unified` is forced. None of this applies on stock, to a
  single-sequence model, or with `kv.unified: false`. The estimate counts the
  live slots as sequences only.
  `councilSlots` is the floor and counts local members only: max(researchers,
  critics + 1 for the synthesizer beside the critics' reviews), at least 1. A
  role with a host, or on a cloud model, takes no slot here.
- **Cloud members are counted apart** (`server/council_cloud.go`):
  `council.cloud_parallel` (default 3, at most 16) at a time, one count per
  council model shared by every turn (`councilCloud`). `takeCloud` in `stream`
  runs after the host branch; a remote host is not counted.
- **Defaults: 2 researchers, 1 critic** (owner's ruling 2026-09-27: a second
  critic of the same model added nothing; it pays when it is another model).
  Server tests state two critics in `councilOn()` to cover indices.
- **Shared reads** (`internal/council/reads.go`): `SharedReads` indexes the
  turn's read-only results from the client's tail (key = tool + canonical
  args → the forwarded id that answered it; a non-read-only result clears
  it); `cfg.result` answers a member from it in place, `forwarded` skips it,
  `once` forwards a repeated read of one step once. Never pushed to a member
  that did not ask (owner's condition). Evidence points at the source ref.
- **`VERDICT: CONFIRMED path:line`** (`Confirmed`, steps.go): offered to
  critics on tool turns; the first confirmation cancels the other critics
  (`stoppedCritique`, their suspensions dropped), overrides `REVISE`, and
  `confirmedNote` tells the synthesizer to make that change first.
- **One council across turns (10.5)**: `internal/council/continue.go`
  (`RouteContinue`, `Kept`, `continueNote`), `server/council_continue.go`
  (`councilKept` per session, `keepDeliberation`, `previousDeliberation`), state
  fields 6-8 (`kept`, `kept_n`, `kept_prefix`). The planner is offered
  `continue` only with `cfg.Previous`; continue runs the synthesizer alone on
  the kept plan + last round, copied into this turn's progress (so a
  suspended synthesizer resumes). A direct answer carries `cfg.Previous`
  forward; a council/continue answer replaces it. Bound to a hash of role +
  content of the messages up to the answered user turn; a newer user message
  must extend it. Guards: `TestAContinuedTurnGoesStraightToTheSynthesizer`,
  `TestTheNextTurnContinuesTheSameCouncil`.
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
- **A member's read ends with the context, never only with its handler.**
  Upstream's `processPending` skips a pending request whose context ended
  without answering it, so a member being scheduled when the client leaves
  never returns from `ChatHandler`. `councilMembers.Stream` closes the pipe
  through `context.AfterFunc`; every send on the turn's `ch` selects on the
  request context. A turn left hanging holds its root, and the next turn on
  the conversation waits on it in `rootRegistry.wait` (bug-142, found by the
  live resume check). Guarded by `TestALeftTurnEndsWhileAMemberIsUnanswered`.
- **`council_chat_state_v1`** (`server/council_state.go`): opt-in by a
  present `council_chat_state` on the request (even `""`). A sealed blob goes
  on a chunk of its own at each checkpoint of `council.RunFrom`, and on the
  done chunk with empty progress plus the compaction record. It is bound to
  the history before the last user message and to that message. Anything
  unreadable is a fresh start, never an error. Add a field with a new protobuf
  number; a changed meaning is a new feature name. The key is
  `<models>/council-state.key`, written through `fsowner`.
- **Tools on council turns (9.5, `council_tools_v1`).** Tools reach the
  council only with `council_chat_state`: a member that calls one is suspended
  into the state, so a client without it keeps the plain-chat bypass. Every
  member request carries the client's tools (`councilMembers.tools`), and so
  does `councilRenderer`, or the PolyKV root stops being the members' prefix
  (`TestAToolTurnsRootHoldsTheTools`). The policy lives in
  `internal/council/tools.go`: researchers and critics call only
  `api.ToolFunction.ReadOnly` tools, the synthesizer and a direct answer call
  any; a refused call is answered in place and never forwarded. The read-only
  mark is `json:"-"` on purpose -- a marshalled mark would change every
  rendered tool prompt; tests must send it as wire JSON (`toolChat`), since
  re-encoding an `api.ChatRequest` drops it. Forwarded ids are
  `MemberKey + ":" + id`; ChatHandler gives parsed calls random ids, so tests
  match the prefix. A step's resume points are read under the lock before it
  launches (bug-143). A researcher's or critic's reply carries its evidence
  (calls and results): the members after it must read what it read, or the
  synthesizer edits blind. A result over `inlineEvidence` travels as its ref
  (the forwarded id, a key of `cfg.Results`) with a preview, read back with
  `council_evidence` (`internal/council/evidence.go`), which `WithEvidence`
  appends to `req.Tools` once, in `councilChat`, so every member and the
  renderer carry the same list. It is answered in `transcript`, never
  forwarded (`local`), and a turn of lookups only loops in-process, bounded by
  `maxLookups`. A member's own results fold to refs past `ResultBudget`
  (window*3/2 chars, set in `councilChat`; `ownBudget` without a tree):
  older turns first (earlier look-ups dropped), then the last turn's largest
  (`folded`) -- or a member that read big files outgrows the owner's window.
  A refusal that needs more cells than its whole window is `llm.ErrNeverFits`
  at once, not a 2-minute admission wait (`neverFits`). A researcher whose first reply names a tool but calls none is
  nudged once and its narration dropped (`narrated`); critics are not, since
  they name the tools the findings used. A worker's PolyKV layer is everything
  before its own instruction (the last user message), never before its last
  message: a resumed member ends in tool results. Debug a turn with
  `OLLAMA_DEBUG=1` and the `council member` lines. Live A/Bs compare against
  the same model as a plain chat (no state, `think: false`). Find the dev
  server by its port.
- **Broadcast (10.6, `council.broadcast`, off by default)** is
  `internal/council/broadcast.go`: `council_post` is added to every member's
  tools by `WithBroadcast` (one list, one shared prefix), answered in
  `transcript` (`local`), offered only by `canPost` (a same-role mate exists).
  `unread` inserts the mates' notes before each model call in `callTools`;
  the board persists through `Progress.Notes`/`Seen` (state Progress fields
  5-6). Keep the caps (`maxNoteChars`, `maxNotes`) and the "do not wait"
  wording: the named risk is members chatting instead of working.
- **Test cycles (11.4)**: `Config.MaxTests` (`DefaultMaxTests` 6) bounds a
  tool turn's cycles. A synthesizer ending with `Retest` ("VERDICT: RETEST")
  carries its evidence like a finding, is recorded in `Progress.Tests` (state
  Progress field 7), and starts the next cycle. `base` adds every failed check
  after the first plan, then `Replan`'s plan for the cycle (`replanRequest`,
  run on the owner; `Progress.Replans`, state Progress field 8), so every
  member of a cycle and its re-plan share one prefix. `holdBack` streams the
  synthesizer's content up to the verdict only: the status is the user's, the
  report the council's. The synthesizer
  key and `Request.Round` are the cycle ("s", "s.2"...). Critics' `REVISE` and
  `NeedsRevision` count rounds from `cfg.cycleStart`. The continue route sets
  `MaxTests` 0. Researchers are asked to propose only when a tool that changes
  something exists. Guards: `TestAFailedCheckGoesBackToTheResearchers`,
  `TestATurnResumesPastAFailedCheck`, `TestTheFailedChecksTravelInTheState`.
- **ab-5 fixes (11.6)**:
  - `MaxSteps` (`DefaultMaxSteps` 6; builder `max_steps` 2..16, state Build
    field 5) bounds a testing synthesizer's tool steps in `callTools`: a
    `budgetNote` at the bound, then a forced `Retest` report two steps later.
  - A testing synthesizer's reply without a verdict gets `verdictNudge` once.
    The nudged call streams nowhere (`onToken` is swapped out), and only its
    verdict joins the reply the user read. Never let it stream again: the user
    would read the answer twice.
  - `Progress.Prior` (state Progress field 10) holds failed checks from before
    this council: `Kept` merges Prior+Tests (`lastPrior`, 6 × 6000);
    `previousPrior` marks them `earlierMark`; `RouteRebuild` and the front's
    rebuild drop them (`dropEarlier`); `frontReport` adds the front's own
    attempts. `withPrior` puts them after the conversation for every member,
    the builder and the planner included.
  - The front is bounded by `frontSteps` (4): at the bound it gets
    `frontBudgetNote`, and two steps later it is forwarded.
  - Findings and critiques are introduced as claims (`findingsIntro`,
    `critiquesIntro`). The builder must never name a cause or a fix (it
    anchored the whole council in ab-5).
  - Guards are in `internal/council/checks_test.go`; each was checked by removal.
- **Sources (11.7, `internal/council/sources.go`)**: `user()` is an instruction
  and is headed `[COUNCIL · INSTRUCTIONS FOR YOU]`. Another member's work goes
  through `sourced(source, ...)`, and the plan reply through `planReply`. Never
  add a council message to a member's conversation without a header: a user
  turn without one is the user's. `sourcesNote` rides in the plan request,
  the route decision, the front and the builder. `IsPlannerRequest` strips the
  header. `noted` compares against the headed note.
- **History (11.7, `internal/council/history.go`)**: `server/council.go` runs
  `council.History(conv)` before compaction. It splits the earlier turns'
  forwarded calls by member key (`callMember`, the `ForwardedID` prefix),
  drops the member's text and thinking beside its calls, and points at a
  repeated long result (`repeatAt`). It must stay a pure function of the
  messages, or the shared prefix breaks between turns. Calls without a member
  id pass unchanged.
- **Reviews (11.9, `internal/council/review.go`, `server/council_review.go`)**:
  `ReviewTool` is `local` and only the synthesizer's (`may`). `sendForReview`
  runs after `post` in `callTools`; `Take(cfg.Turn)` runs before every
  synthesizer call. Never make the synthesizer wait on a review except at
  DONE (`Wait`, `reviewWait`); that is the owner's design.
  - A review judges the calls' results (`checkEvidence`), never only the
    synthesizer's account of them.
  - The DONE gate keeps `gatedReply` (the answer the user read) and mutes the
    reply that follows.
  - Job ids are `Turn/key:call`, since the desk outlives turns.
  - A check the synthesizer moved on from unsent is sent for it
    (`unsentCheck`, id `auto_<call>`): a read-only call after a change, with
    no `ReviewTool` call after it, when the next turn changes something
    (measured: the synthesizer never called the tool). At DONE `sendLast`
    sends that check under the same id, else an unchecked last change
    (`uncheckedChange`, `done_<n>`).
  - With the deliberation shown, `callFrom` sets `showReviews`: each review
    taken streams as its critic's thinking, role `council.Reviewer`, named
    with its index by `memberName` in `server/council.go`.
  - The server keeps one desk per session (`councilDesks.get`, remade when
    the critic count changes), closes it when the client leaves, and closes
    it after `reviewIdle` idle.
  - Background members have no tools. `ownWindow` states their window
    on opencoti, where no window books the whole pool.
  - `sendForReview` also sends an `unsentCheck` when the synthesizer's next
    call changes something (id `auto_<check>`; a read is still checking).
    `sendLast` at DONE reuses that id, or sends an `uncheckedChange`.
  - Reviews are shown at delivery through `cfg.show`, as `Reviewer` thinking
    (set in `callFrom` when the deliberation is shown); `memberName` numbers
    reviewers.
- **Usage per role (`council_usage_v1`, `server/council_usage.go`)**: every
  member call's done metrics go into `councilMembers.usage` (`usageBook`,
  keyed by role, model and host), both locally and through `remote`. The turn's
  done chunk carries `ChatResponse.CouncilUsage` (`council` hook in
  `api/types.go`), plus the desk's reviewers (`councilDesks.usage`, drained).
  `prompt_tokens` = `prompt_eval_count` + cached: what was sent, and what a
  cloud model bills. The manic harness records it per trip and per role.
  Guards: `TestACouncilTurnReportsWhatEachRoleSpent`,
  `TestAUsageBookCountsTheCachedPromptAsSent`.
- **The task list (11.10, `internal/council/tasks.go`)**: the planner's plan
  JSON carries `"tasks"`; `mergeTasks` enforces the rules (no deletion, an
  outcome to close, a real researcher to assign, refuted stays refuted, new
  ids after the last). It is a schema field, never a tool: the planner has no
  tool loop. A task is matched by its words before its id (`updates`): a
  planner renumbers from 0 (sixth simple run). Plans are kept with the list's
  ids (`plan.Tasks` set after the merge), so no member reads the planner's 0s. Carried in `Kept` and state Progress 12/13 (Plan 3); every
  rebuild drops it with `dropEarlier`.
- **Stuck checks (11.11, `internal/council/stuck.go`)**: `Progress.Checks`
  (state Progress 11) keeps each failed cycle's last read-only result;
  `testsBody` marks same/moved and adds `stuckNote` after `stuckAfter` same
  outputs. Keep every note topic-agnostic (owner's condition; the test lists
  forbidden words).
- **A review always has its verdict (11.12)**: `Desk.work` re-asks once with
  `reviewFormatNudge`; a second miss is `REVIEW: UNCLEAR`, never a pass.
- **The builder (11.5, `internal/council/build.go`)**: `Builder` reads
  `builderConversation` only (system + the user's unheaded messages: no
  answers, member work or tool results — it anchored on them twice). It runs
  on its own session `~builder` with `ownWindow`, never on the owner, whose
  cache a different prefix would evict. It runs before the first plan of a
  council route with no kept build, and on `RouteRebuild`. `apply` appends its
  instructions to `prompt(cfg, r)` ("For this work: ..."), sets think only for
  roles the user left unset (levels map to 1024/2048/4096), and sets `MaxTests`
  (0..12). The user's roles, counts, models, prompts and think settings stand,
  and the tool policy is fixed. `Progress.Build` (state Progress field 9)
  travels in `Kept`, the continue route included. A reply that isn't the JSON
  asked for is an empty build (recorded, shapes nothing, offers no target).
  Guards: `TestTheBuilderShapesTheCouncilTheUserDefined`,
  `TestABuilderThatSaysNothingShapesNothing`.
- **The front (11.5, `internal/council/front.go`)**: on a tool turn,
  `WithRouting` (server, after `WithEvidence`/`WithBroadcast`, same list for
  every member) adds `council_forward`/`council_rebuild`. `fronted` (a
  ToolModel and the forward tool in the list) replaces `Decide` and `direct`
  with the synthesizer's front call (`Front` role, key "f", on the owner
  session like the planner). Only `Front` may call the routing tools
  (`refusedRouting`), and they are `local`. A rebuild runs `MakeBuild` inside
  the front loop, and `rebuilt()` answers it with the new setup. A forward
  sets `p.Route` to council. A direct answer keeps `keptWith(Previous,
  Build)`. The route decision offers `continue` only when `canContinue`.
  Guards: `TestTheSynthesizerTakesTheRequestFirst`,
  `TestTheSynthesizerRebuildsTheCouncilForNewWork`, `TestOnlyTheFrontRoutes`,
  `TestAToolTurnGoesThroughTheSynthesizerFirst` (checked by removal).
- **Preemption (11.4, the owner's ruling: test results and verdicts only)**:
  a `council_post` with `kind` confirmed/refuted (`Note.Kind`, state Note
  field 4) calls `board.preemptLocked`, which cancels each same-role mate's
  call registered by `streamPreemptible` (`board.listen`). The mate keeps its
  partial text as an assistant turn and reads the verdict through `unread`,
  at most `maxPreempts` (2) times. No engine can inject tokens into a running
  generation, so this is the approximation. Guard:
  `TestAVerdictInterruptsTheMateGenerating` (checked by removal).
- **A repeated call (11.2)**: `transcript` appends `repeatedCall` to a result
  that repeats, word for word, an earlier identical call's result in the same
  member's turns. Keep it generic: it serves any tool.
