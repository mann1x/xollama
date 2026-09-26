# xollama — State Summary

Newest entry first. Each entry is dated and says what changed (commit shas,
release tags, measurements) and what is left. The fixed sections below the
entries are rewritten in place so they always describe *now*. Plans are
indexed in [`plans/MASTER_PLAN.md`](plans/MASTER_PLAN.md).

> **2026-09-26 — Council compaction: Phase 8 proposed, a port of Cerebriline's.**
> - The owner asked whether the council's compaction followed Cerebriline's
>   agentic council compaction. It did not: only the 0.85 pressure trigger
>   came from the guide. Reading the code also showed faults:
>   - it forgets a compaction, so it likely flips between compacted and full
>     turns;
>   - it re-summarises all the old turns;
>   - its budget is wrong;
>   - it edits the system message, which breaks the cached prefix.
> - The plan's Phase 8 now ports Cerebriline's flow, read from
>   `/shared/dev/cline`:
>   - trigger min(0.9 × usable, room) and target 0.25 of the message budget;
>   - the kept tail by recency bounds;
>   - one user-role summary message at the front: the user's requests
>     verbatim, a retrospective and the replay;
>   - a writer, two critics on the replay's halves, and a synthesizer;
>   - carried forward and incremental, with Cerebriline's fallbacks.
> - Approved 2026-09-26, sized on the granted window as Cerebriline does;
>   being built.

> **2026-09-26 — The council holds the conversation once; idle compaction tested live.**
> - Scripting the idle test found a design flaw: the planner's session and P1
>   were two copies of the conversation, both charged to the owner. The tree
>   was full at ~45 % of the window, so compaction never fired, and refused
>   pools ended the turn with a 503 after 2 min.
> - Fixed as the owner chose (guide §6.2 arm C, `server/council_polykv.go`):
>   - P1 is built first and the planner attached to it.
>   - The first turn's P1 is unowned (`pool_unowned_v1`), and the idle council
>     promotes it to an owned one. The next turn waits for that and forks it.
>   - The summary is asked as a turn of the conversation.
>   - A root refused with "compact the session" (`llm.ErrSessionFull`)
>     compacts and retries.
>   - Seats: `councilPoolSeats` + 2 for the root chain.
> - Live on b128, `omni-council-idle` at 16k, ~7,000 tokens of history. With
>   the idle summary the next message's first token came at 2.5 s (turn 47 s);
>   without it, 18.3 s (turn 64.5 s). The owner's pressure after turn N was
>   0.867.
> - Phases 6 and 7 closed. Left: the pin moves to a published build with
>   `pool_unowned_v1`, on a measurement.

> **2026-09-26 — opencoti fixed #349 (b128); `num_ctx 0` and `slots.live` tested live on it.**
> - opencoti b128 `2609261427001` (dev, local only) builds the recurrent
>   state before the attention window and reserves the MTP draft context.
>   Their gate: b125 fails our `-c 524288 -np 4` launch, and b128 loads it.
> - Our councils on b128 (a copy in `/srv/ml/xollama-phase2/engines/`, dev
>   launcher `council-b128.sh`):
>   - `num_ctx 0` → `-c 262144`, the trained context, 22.9 GB. The pools
>     were created `owner ''` (unowned), and the turn took 74 s.
>   - `slots.live: 4` at 131k → `-c 524288 -np 4`, 23.0 GB, turn 86 s.
> - The pin stays on b111 until opencoti publishes to the HF dev repo and it
>   is measured. Left: idle compaction on a long conversation.

> **2026-09-26 — Council roles on eleven2go tested live; three bugs fixed.**
> - Researchers ran on eleven2go's ollama (the think-budget fork, `:11434`,
>   treated as stock: `think=true`, 56 s), on its xollama through an SSH
>   tunnel (`think=2048`, 66 s), and on a cloud model through that xollama
>   (`think=true`, 53 s). A tunnel killed mid-reply fell back (58 s).
> - Fixed:
>   - A cloud reference's `/api/show` carries no `remote_host`. A cloud
>     reference is now cloud by name on every server.
>   - A dropped connection ended the stream silently, and the council went on
>     with no research. A remote reply now needs its `done` line.
>   - A role on another model had `think` cleared to nil, which upstream reads
>     as true, so `qwen3:8b` reasoned away its whole cap and answered nothing.
>     `think: false` is now kept, and an empty reply from elsewhere falls back.
> - opencoti #356: #349's cause is found (rs built after the attention
>   window; draft context not reserved). b126 boots the main context, and
>   the draft-aware sizer is in progress. We stay on 131k.

> **2026-09-26 — Cloud roles tested live; think budgets only where understood.**
> - `gemma4:31b-cloud` as researchers, critics, planner and synthesizer,
>   and in all four roles: every turn answered, 17–54 s. The all-cloud
>   council took 17 s. A researcher on a missing model fell back to the
>   council's model and said so (59 s, 9 members).
> - The owner's "hang" on `omni-council-think` was the pre-2048 build.
>   `on` was `medium`, 32,768 tokens per member, and each researcher ran to
>   the cap at about 40 tok/s (13.5 and 14 min). The same question on the
>   current build takes 2 min 11 s.
> - Found live: ollama.com refuses a numeric `think`. `councilTakesBudget`
>   now sends a token budget only to this server's own models and to a model
>   another xollama serves itself. Cloud is decided by manifest or
>   `remote_host`, as cerebriline does, never by name. Everything else gets
>   `think: true`. Retest: 47 s, no error.
> - The dev home's unregistered key is linked to the service's signed-in key
>   (the old one kept as `id_ed25519.dev-unregistered`), at the owner's
>   request.
> - opencoti #353: 0408 does not fix #349. b125 fails the 4 × 131k launch
>   the same way, and they are investigating.

> **2026-09-26 — Council Phase 7 built: roles on cloud models and other servers.**
> - The owner decided the open points. A researcher or critic that fails on
>   another model or host is answered by the council's own model, and the
>   turn goes on; a planner or synthesizer failure fails the turn. A literal
>   host URL is fine. `host` joins schema v4, which no release has shipped.
> - A cloud role already worked: `council.<role>.model: …:cloud` goes
>   through `ChatHandler`'s cloud proxy.
> - New `council.<role>.host` (needs `model`; tweak
>   `--council-<role>-host`). It is served by `server/council_remote.go`
>   over `api.Client`, with no session and no pool.
> - Hosts are gated by the operator's `XOLLAMA_COUNCIL_HOSTS` (default
>   none): a pulled council model could otherwise send every conversation to
>   the host it names.
> - Unit tests with a stub ollama over HTTP; every guard fails under a
>   mutation. The live test is pending.

> **2026-09-26 — Council Phase 6 built: think 2048, pressure and idle compaction, `num_ctx 0` as unowned pools, `slots.live`; Phase 7 proposed.**
> - The owner approved Phase 6. A role's `think: on` is now a 2048-token
>   budget (`DefaultCouncilThinkBudget`), and mode and budget stay per role.
> - Compaction follows the owner's raw `/kv` pressure: at `compact_at`
>   (0.85) before a turn, and at the new `council.context.idle_compact_at`
>   (0.75, tweak `--council-idle-compact-at`) in the background after the
>   answer, so the next message finds the summary ready. Without a pressure
>   reading, the token budget still decides.
> - `num_ctx 0` under PolyKV only (hook `polykv-window`,
>   `llm/engine_window.go`): the launch takes the trained context as the
>   pool, and with `pool_unowned_v1` the council's pools are unowned and the
>   planner sends no `num_ctx`. Stock llama.cpp and opencoti without pools
>   keep upstream's clamp to 4.
> - `slots.live` (hook `slots-live`): the slots a model loads with, in place
>   of `OLLAMA_NUM_PARALLEL`, on opencoti only.
> - Unit tests at every layer, each mutation-checked. No live run: the owner
>   tests it on the next promoted build. Not built: warming the owner session
>   with the compacted prefix after an idle summary.
> - opencoti #350 on #349: the fit bug is the main context's two-pass window
>   re-size ignoring its recurrent-state cells, not the MTP context. Fix
>   0408 comes in the next dev build.
> - Phase 7 proposed: a role on another model **or another ollama instance**
>   (a cloud model, or another machine), via `council.<role>.host`.

> **2026-09-26 — Council members can think; the MTP IQ2_M runs councils at 131k; Phase 6 proposed.**
> - `council.<role>.think` (`off` | `on` = medium | level | token count), with
>   `tweak --council-<role>-think`. The council resolves the level against the
>   member's window and sends an explicit token budget. It raises
>   `num_predict` to the reply cap plus the budget. It never shows the
>   reasoning, and the model's `think_budget_message` closes it at the cap.
>   The routing call never reasons. Tests at the schema, runner and server
>   level; each fails under a mutation.
> - Live, the defaults proved unusable. `medium` at a 131,072 window is
>   32,768 tokens per member, and a researcher looped ("Wait, I should
>   check…") past 29k tokens. The run was stopped after 11 min. The default
>   budget for members is an open decision.
> - Stores: `mannix/omnimerge-v4-mtp:IQ2_M` (MTP, 65 blocks) replaces the
>   non-MTP IQ2_M in both. The service store's
>   `omnimerge-v4-mtp_tb:27b-iq2m-128k` is recreated on it. The old tag's
>   CRLF Modelfile had swallowed its RENDERER, PARSER and think_budget into
>   the TEMPLATE, so the new tag takes them from `…:27b-q4km-128k`. The
>   council store holds `omni-council` and `omni-council-think` (qwen3.5
>   renderer, `num_ctx 131072`, `kv {k: f16, v: q8_0}`).
> - 131k with MTP: it failed only because the test script set
>   `XOLLAMA_NUM_PARALLEL=4`, which makes `-c = 4 × 131072`. Without it: `-c
>   131072 -np 1 --max-parallel 4`, 10.4 GB, and a full council turn in 52 s.
>   The engine's fit omits the MTP context's recurrent-state cells
>   (opencoti #349).
> - Phase 6 is proposed in the plan: PolyKV-only sizing, `num_ctx 0`,
>   compaction on the owner's pressure (0.85, and 0.75 while idle), per-model
>   `slots.live`. Stock llama.cpp and opencoti without PolyKV stay as they are.

> **2026-09-26 — `continue_pool` planned, waiting for an HF dev publish.**
> - New plan [`plans/council-continue-pool.md`](plans/council-continue-pool.md)
>   (WAITING). It starts when an opencoti build carrying patch 0406
>   (`continue_pool`, unowned pools; b124 is local only) is published to
>   the HF dev repo, as the owner decided. Phase 0 measures turn-over-turn
>   prefill, dense and hybrid, at 16k and 131k.
> - The Docker image plan's row is brought up to date: the first `:dev`
>   image is published and GHCR is public; user testing remains.
> - The installed service (`/usr/local/bin/xollama`, `05c16dfa`, 11434)
>   predates the council and refuses a council model. Councils are tested
>   with the `dev` build on 22434 against the isolated store.

> **2026-09-26 — Desktop app shows councils (option C); GHCR confirmed public.**
> - A council model gets a *council* badge in the model picker, and a
>   **Deliberation** toggle in place of the Think button. The toggle is on
>   by default and remembered per browser. The app's backend dropped every
>   `think:false`, so nothing could hide the deliberation before. It now
>   forwards `false` for a council only (`app/ui/council.go`, `council`
>   hook). The UI builds, vitest passes 201 of 201, and the Go test runs on
>   Linux and compiles for Windows. Not yet tried in the running desktop app.
> - `ghcr.io/mann1x/xollama` was created public by its first push. An
>   anonymous token lists `dev` and `0.34.2-dev.1f14838e`;
>   `docs/features/docker-release.md` says so.
> - opencoti b124 (dev build, not published) adds `continue_pool` and unowned
>   pools (#343). They are request fields on routes `/api/engine` already
>   proxies, so it needs no change. The pin stays on b111.

> **2026-09-26 — Council Chat Phase 5 closed: docs, the one-shot CLI turn,
> councils at 131k on a hybrid model; bug-117 fixed.**
> - Docs: `docs/xollama/council.mdx` (users, in the navigation and the index)
>   and `docs/features/council.md` (maintainers). `compact_at` wording
>   corrected in the schema, `tweak` and the validation error.
> - CLI walked live as `ollama` on b111. One-shot `xollama run <council> "…"`
>   went to `/api/generate` and silently skipped the council; it is now sent
>   as a chat turn (`cmd/council_run.go`, `council` hook in `cmd/cmd.go`).
> - bug-118, at the model's own 131k (`-c 524288`, `-np 4`). The engine's
>   elastic recurrent-state cache stayed at 4 committed cells and refused a
>   critic with 429. Three things went wrong in turn, each fixed. The chat path did not wait out a 429,
>   as the completion path does, and now it does (503 after 2 min). ggml's
>   routine `failed to allocate graph, reserving` line read as a runtime OOM
>   and expired every model; on opencoti it is no longer an error. And the
>   tree's four pinned layers held every cell, so the synthesizer could never
>   be seated. On a recurrent engine the tree now releases each finished
>   stage's layer. Live: 3 of 3 full council turns (115, 66, 54 s), 0
>   refusals, 0 errors, no expiry.
> - bug-117: a runner swap no longer waits on stock-llama-server discovery
>   for devices opencoti serves (`discover/refresh_opencoti.go`; timed-out
>   dirs cool down 15 min). The reaper no longer logs a stop it asked for as
>   an ERROR. Refresh-to-load went from 9.3 s to about 1 s, and a swap's
>   round trip from 15.8–20.2 s to 10.1–11.8 s.
> - Desktop toggle not built. The app already serves a council tag, with
>   the deliberation shown as thinking; the options are under Open
>   decisions.
> - Sent to opencoti: the elastic `rs` cache grew 4 → 8 at 16k but not at
>   `-c 524288`.

> **2026-09-26 — b65 vs b111 re-measured, paired and interleaved (opencoti
> #329): the pin stays on b111.**
> - Five repeats per cell, order alternating, never more than one compute
>   app on the 3090 in any cell (sampled every second).
> - Multislot aggregate: b65 median 541.9, b111 500.8 tok/s (−7.6 %). Each
>   build's range is about 18 %; b111 was lower in 3 of 5 pairs. At most a
>   small effect.
> - 70B overflow: b65 median 1.39, b111 1.87 gen tok/s; b111 faster in 4 of
>   5. The earlier deficit does not reproduce. Both builds follow load time
>   (host page cache), not the build.
> - Sent to opencoti (#336). Raw data:
>   `/srv/ml/xollama-phase2/as-ollama/regress-paired/`.

> **2026-09-26 — Council Chat Phase 4: members share the conversation's KV
> through PolyKV; `/api/engine` reaches every opencoti route.**
> - `llm/engine_council.go` is the PolyKV client: a per-request `Placement`,
>   plus pools, fork, release, session close, `/kv` and resize.
>   `server/council_polykv.go` is the per-turn tree. The owner books the
>   window; P1, P2r, P2f and P3s are each built once; workers attach and are
>   closed; pools are released newest first. The owner's window shrinks,
>   deferred, under `/kv` pressure and grows back when the pressure clears,
>   and the conversation is compacted past `compact_at`. A PolyKV council
>   launches with `2 + 2 × rounds` pool seats. Hunks: `council` Registry row.
> - A/B on b111 (omnimerge v4 IQ2_M, 16k, `-np 4`, ABAB, 4 council turns per
>   arm): computed prefill 3,139 → 525 tokens per turn (−83 %), cache hit
>   48–52 % → 89–94 %, peak KV cells about −40 %, 4 of 4 pool seats used, no
>   refusals, no pools left after a turn. Wall time at parity (60.2 s vs
>   57.6 s): the turn is decode-bound. Plan Phase 4 has the table.
> - `/api/engine` now serves GET, POST and DELETE over opencoti's whole
>   management surface (`kv`, `elastic`, pools and their capacity, `tps` SSE,
>   sessions close and resize, locks, `apply-template`…), from a route table
>   mirrored from the engine's registration. Inference, `/cors-proxy` and
>   `/tools` stay out. `?live=1` and other queries are forwarded.
> - Fixed: the members saw the model's `MESSAGE` turns twice (bug-116). Open:
>   every runner swap waits about 2.3 s for a discovery subprocess and logs an
>   ERROR killing it (bug-117).
> - Docker `:dev` run 36221348282 (the b111 image) succeeded.
> - Left: Phase 5 (surfaces, `docs/xollama/council.mdx`).

> **2026-09-26 — Council Chat Phase 3: a council model answers through
> every chat API.**
> - `internal/council/` (the errgroup runner) and `server/council.go`
>   (members as in-process chat turns, each on its own engine session), plus
>   one `council` hook line in `ChatHandler`. Tools, a `format` and every
>   member's own turn bypass it.
> - Live on b111 (omnimerge v4 IQ2_M council tag): "Hello!" answered
>   directly in 1.4 s warm; a council turn in 54–102 s with 7 members; the
>   second turn, OpenAI (`reasoning` + content) and Anthropic all correct.
> - Corrected: a council tag does not share its base tag's runner (upstream's
>   `ManifestDigest` is in the launch config); the docs said it did.
>
> Next: Phase 4, the PolyKV path (owner session, pools, pressure and resize).

> **2026-09-26 — Engine pin moved to b111 (2609252051001).**
> - `llm/engine/pin.txt`: rev `1d0f1dcd`, bin `a66e9e27`, CUDA dso
>   `f32ebf54`. b111 brings `kv_pressure_v1`, `kv_resize_v1` and
>   `kv_resize_deferred_v1`, the surface Council Chat Phase 4 builds on.
> - Cost: **no Linux Vulkan payload** in these bytes, so the Vulkan dso and
>   accel rows are retired and Vulkan loads route to llama.cpp again.
> - Measured as `ollama` beside b65: compat 8/8, throughput and gemma4 parsing
>   equal; argv surface unchanged. Medians lower on b111 for multislot
>   (485 vs 595 tok/s) and the 70B overflow (2.17 vs 3.18 tok/s), spreads
>   overlapping, reported to opencoti. No engine-defect row changes.
>
> Next: Council Phase 3 live on b111; the `:dev` image picks this pin up.

> **2026-09-26 — ollama#4165's single-sequence rule binds only stock
> llama.cpp.**
> - Qwen3.5, Qwen3-Next, Qwen3-VL, LFM2, Nemotron-H and mllama now take
>   `OLLAMA_NUM_PARALLEL` and dynamic slots when opencoti serves them. The
>   scheduler asks `llm.WouldUseOpencoti` before capping; with
>   `XOLLAMA_ENGINE=llamacpp` the cap is upstream's, unchanged.
> - A load predicted for opencoti that lands on stock anyway (artifact
>   missing, or the opt-in retry) relaunches at one sequence
>   (`servedSequences`). Embedding models stay at one on every engine.
> - Measured on b111 as `ollama`: qwen3.5:2b launched `-np 4`, 4 streams at
>   ~107 tok/s each. `probe/parallel_correctness.py` (4 checkable tasks,
>   serial then concurrent, greedy, 3 rounds): omnimerge v4 IQ2_M 12/12
>   correct, 12/12 identical to serial; qwen3.5:2b the same 9/12 correct
>   serial and parallel (the misses are the model's), 0 cross-task bleed.
>
> Next: the b111 pin (a multislot/overflow gap against b65 is being
> re-measured) and Council Phase 3 live.

> **2026-09-26 — Docker image: assembled on hosted runners from pinned
> artifacts.**
> - `docker-release.yaml` no longer compiles anything, and no longer needs
>   the bs2 runner, which was never registered. On `ubuntu-latest` it runs
>   `scripts/docker-assemble.sh`, which stages:
>   - the fork's CPU runtime tgz (`33ac42c1…`);
>   - upstream v0.34.2's GPU and MLX tarballs, pinned by sha256;
>   - the engine from `llm/engine/pin.txt`;
>   - a Go-only `xollama` (go1.26.8, AlmaLinux 8).
>
>   `Dockerfile.xollama` then builds, smoke-tests and pushes the image.
> - The pins are in `llama/runtime-pin-linux.txt`. Its inputs digest leaves
>   out `llama/compat/README.md`; the fork at `d6e24119` and `dev` both come
>   to `eff6800e…`.
> - Validated locally:
>   - the assembly, and a 5.43 GB image;
>   - the container answers `/api/xollama` on `0.0.0.0:22434`;
>   - actionlint and shellcheck are clean.
> - The channel is now the GitHub pre-release flag, not the hyphen, and
>   `:latest` never moves from a branch.
> - Found: upstream's `Dockerfile` sets `OLLAMA_HOST`, so an image built from
>   it would be unreachable. The new image sets `XOLLAMA_HOST`.
>
> Next: push `dev`, then
> `gh workflow run docker-release.yaml --ref dev -f push=true -f channel=dev`
> for the first `:dev` image, then user testing.

> **2026-09-26 — Council Chat Phase 2: a council is a model setting.**
> - `council` in `xollama.json` (schema v4, `types/xollama/council.go`),
>   with 23 `xollama tweak model` rows (`--council`, `--council-charter`,
>   prompts from `@file`), `show` rows and the Modelfile round-trip, all
>   tested.
> - Fixed before it could ship: a council tag would have had its own runner,
>   a second copy of the weights. The launch config now leaves the council
>   out (`LaunchConfig`, inside the `model-config` hook).
> - Found for Phase 3: qwen35 (omnimerge) is on upstream's single-sequence
>   list, so through xollama its council members would run one at a time.
> - The eval harness no longer holds the library implementations (their
>   notes are kept), so no `go.mod` in the repo links eino, langgraphgo or
>   trpc-agent-go.
> - The plan gains "Context, pressure and compaction": every member states
>   its context, and compaction follows the owner's PolyKV pressure.
>
> Next: Phase 3, the runner on the llama.cpp path.

> **2026-09-25 — Council Chat Phase 1: the in-house `errgroup` runner wins
> the library bake-off.** The same council was built in eino, langgraphgo,
> trpc-agent-go and on `errgroup`, all over one shared core and one suite, in
> `plans/council-eval/` (its own module). All four pass 11 tests under
> `-race`, but every library needed sibling cancellation and error ordering
> added by hand. Per request the baseline costs 56 µs per council and 3.2 µs
> per direct turn; langgraphgo costs 69/17 µs, eino 130–180/60 µs, and
> trpc-agent-go 0.9 ms/278 µs, rising to 22.5 ms and 33 MB at 1 MB of state.
> The libraries would bump sonic, protobuf, go-sqlite3 and testify in
> xollama's go.mod. Against b65 with omnimerge v4 IQ2_M, all four run a
> council in 65–70 s, which is noise. Also found:
> - On the hybrid qwen35 model the pools share on exact matches: 33–77
>   tokens prefilled per member, and 54 s pooled against 65–69 s unpooled.
> - Members must state `num_ctx`. Without it the second parallel member is
>   refused with a 429.
> - The planner's calls must share a session: the direct path takes 3.3 s
>   that way, against 6.1 s.
> - The IQ2_M omnimerge tag has no MTP head.
> - One unexplained session leak in 18 runs; the TTL reclaimed it.
>
> Next: Phase 2, the `Council` config in `types/xollama` and `tweak`.

> **2026-09-25 — Council Chat Phase 0 measured on b65.** On b65 with
> llama3.1:8b, the council's pool tree shares as designed. Researchers,
> critics and the synthesizer each prefilled 29–73 tokens of 2.5k–3.4k-token
> prompts, and the owner held 3,343 cells for the whole tree against ~16k
> unpooled. The council's wall time was 13–17 s. Close released everything.
> Confirmed: a request with a `session_id` and no `num_ctx` books the full
> 65,536-cell `session_ctx_max` per request. Routing is the weak link, so the
> planner's decision becomes route-only: 86/100 trivial messages direct,
> 60/60 hard messages to the council, +0.13 s to the first token. Probes are
> in `plans/council-eval/probe/`. Next: the same probes on b111 when it is on
> HF, and Phase 1 over a stub model.

> **2026-09-25 — Project record started; Agentic Council Chat planned.**
> `STATE_SUMMARY.md` and `plans/` created, with a standing rule in `CLAUDE.md`
> to keep them current. New plan
> [`plans/agentic-council-chat.md`](plans/agentic-council-chat.md), now at
> Phase 0. The target is opencoti **b111**; development and smoke tests run on
> the pinned b65 until b111 is on the HF dev repo. The library research
> shortlisted cloudwego/eino, smallnest/langgraphgo and trpc-agent-go, plus an
> in-house `errgroup` baseline, for the Phase 1 bake-off. The Docker image
> design is written down as [`plans/docker-image.md`](plans/docker-image.md)
> (PARKED).

> **2026-09-25 — Toolchain and dependency security (`ad5842ce`).** Releases
> build on upstream's `go` line (go.mod `go 1.26.0`) at its newest patch,
> derived from go.dev in the `plan` job. Today that is go1.26.8, and every
> binary is checked for it. `golang.org/x/{crypto,image,net,sync,sys,mod,term,text}`
> are bumped under the `security-deps` hook. govulncheck went from 25
> reachable vulnerabilities to 0. The 88 UI lockfile Dependabot alerts are
> inherited from upstream (50 are dev-only) and were left as they are.

> **2026-09-25 — llama.cpp comes from the fork, enforced (`10a88f1b`).**
> `scripts/check-compat-origin.sh` and `.github/workflows/compat-origin.yaml`
> refuse any change to `LLAMA_CPP_VERSION`, `llama/server` or `llama/compat`
> that is not reachable from a mann1x/ollama or ollama/ollama ref. For a merge,
> every file must match one of its parents. The fork's 005 was merged by sha
> (`89ae39d3`). The inputs digest is now `73387c282b7f` on both sides.
> `llama/compat/README.md` is agreed to become static, with each patch
> documenting itself; the fork commits that change first.

> **2026-09-25 — First release: `v0.34.2-xollama.1` (PR #1, `990e35e2`).**
> Built on hosted CI and promoted to latest after the eleven2go install check:
> 126.4 tok/s, and the installed exe's sha256 matched the release. The Windows
> legs are pinned to `windows-2022`: the `windows-latest` and `windows-2025`
> images both ship VS2026, which breaks CUDA 13.0 and ROCm 7.1. Payload-id is
> `54e16442…`. First-time-contributor auto-approval was removed on
> mann1x/xollama and mann1x/ollama.

## Where we are

`v0.34.2-xollama.1` is the latest release. The Agentic Council Chat has
closed Phases 0–7, tested live on the b128 dev build, the cloud and
eleven2go: PolyKV sizing, pressure and idle compaction with the conversation
held once, `num_ctx 0`, `slots.live`, and roles on cloud models and other
servers. The engine pin stays on b111 until a build with `pool_unowned_v1` is
published and measured.

## What exists today

- Soft fork of ollama v0.34.2 with full upstream history. The engine seam is
  opencoti-llamafile, pinned to b65 in `llm/engine/pin.txt`.
- Release protocol and hosted CI: `docs/protocols/RELEASE.md` and
  `.github/workflows/xollama-release.yaml`. The Windows CPU runtime is pinned
  in `llama/runtime-pin.txt`, and delta updates are keyed on `payload-id.txt`.
- Model config and `xollama tweak model`, device selection, store ownership,
  the 22434 port with the `XOLLAMA_HOST` namespace, the rebrand, and the
  Windows installer. See the `docs/features/` list in `CLAUDE.md`.
- Guards: `scripts/check-hooks.sh`, the compat-origin check, and gitleaks.

## In flight / waiting on others

- **opencoti:** the pin is on b111. The paired re-run (#336) did not
  reproduce the overflow deficit, and multislot is at most −7.6 % median,
  inside the spread; no bisect, agreed with opencoti (#339). Also waiting
  on: an HF dev publish
  of patch 0406 (`continue_pool`, #343), and of the #349 fix (b128,
  verified locally). The Linux Vulkan `.so`
  comes in their next dev publish. Also waiting on the spent-response port
  and the E2B/E4B gate, which needs an HF repo@rev.
- **mann1x/ollama (fork):** the static `llama/compat/README.md` commit. When
  it lands, xollama takes it by sha.

## Known gaps

- The Docker image is published to `:dev` (run 36221348282). It is amd64
  only, and b111 carries no Vulkan payload, so Vulkan loads go to llama.cpp.
- Upstream's `Dockerfile` still sets `OLLAMA_HOST`/`EXPOSE 11434`, which
  xollama ignores; only `Dockerfile.xollama` makes a reachable image.
- `/api/engine` control routes are unauthenticated, like the rest of the
  Ollama API. On a server bound beyond localhost, anyone who can reach the
  port can close sessions or release pools (stated in
  `docs/xollama/introspection.mdx`).
- On a hybrid model at a large context (b111, `-c 524288`) the engine keeps
  4 recurrent-state cells and refuses a fifth sequence. This is intended
  (opencoti #348): the cache grows only into free VRAM beyond a 1 GiB margin
  (`OPENCOTI_RS_VRAM_MARGIN_MIB`), and the base KV reservation leaves none.
  A council copes by releasing finished layers, and other parallel work
  queues.
- Open fork-sync items: `docs/protocols/FORK-SYNC.md` § "Open items".
- Carried upstream PRs: `docs/protocols/CARRIED-PATCHES.md`.
- UI lockfile Dependabot alerts are inherited from upstream and not addressed.

## Immediate next steps (in order)

1. Test council Phase 6 live on the next promoted opencoti build (0406
   unowned pools, 0408 rs-window reserve): `num_ctx 0`, idle compaction, and
   `omni-council-think` with `think: on` (now 2048).
2. When b128 (or later) is on the HF dev repo, measure it with
   `scripts/phase2-engine-ab.py` and move the pin; then retire the #349
   workaround notes (a 4 × 131k launch loads on b128).
3. Try the council badge and the Deliberation toggle in the running desktop
   app, which needs a Windows or macOS build.
4. Move the engine pin only on a measurement. The `rs` question (#345) is
   answered: working as intended.
5. When an opencoti build with patch 0406 is on the HF dev repo, start
   `plans/council-continue-pool.md` Phase 0.

## Open decisions

- None open on the council; the next choices come from the live tests.

## Maintenance protocol

Every session that changes code, releases, pins or plans adds a dated entry
at the top of this file, in the same commit as the change. That session also
rewrites any fixed section the change makes stale. When a plan changes status
or phase, update its row in `plans/MASTER_PLAN.md` and the plan itself in the
same commit. Keep entries factual: shas, tags, numbers, and what is left.
