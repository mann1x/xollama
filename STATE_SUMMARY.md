# xollama — State Summary

Newest entry first. Each entry is dated and says what changed (commit shas,
release tags, measurements) and what is left. The fixed sections below the
entries are rewritten in place so they always describe *now*. Plans are
indexed in [`plans/MASTER_PLAN.md`](plans/MASTER_PLAN.md).

> **2026-10-02 — Server settings, Phase 5: the engine's auto policies (schema v6).**
> - New settings, on `tweak model` and as `tweak server` defaults:
>   - `kv.rolling_window` (`--kv-rolling-window`: on, off or MiB);
>   - `draft.auto_mtp_policy` (`--auto-mtp-policy`);
>   - `fit.enabled` (`--fit`);
>   - `fit.vram_target_mib` (`--vram-target`).
> - `kv.residency_mode`'s help now carries opencoti #609's semantics.
> - A rolling window under `head` residency is refused, because head disables the window.
> - All but `--fit` are opencoti only.
> - These configs are stamped v6, so an older build refuses them rather than dropping them.
> - Not yet run against a live opencoti: eleven2go is paused for the rolling-window validation.

> **2026-10-02 — Server settings, Phase 4: the link probe in `tweak server gpu`.**
> - The GPU menu now has the engine measure each GPU's host link (`/api/xollama/link-probe`, which runs `opencoti --link-probe`, b62 or later).
>   - It shows the detected PCIe generation and lanes and the measured rate, and the link wizard starts from them.
>   - A backend with a model generating on it is not probed.
> - Measured on solidPC (dev build 2610020719001): the RTX 3090 runs at PCIe 3.0 x8 in a 4.0 x16 slot, 6.7 GB/s host to device.
> - The owner approved opencoti's per-device link force (`PCI=GBPS,...`). opencoti builds it alongside the rolling-window validation; until then a load uses the slowest forced figure among its GPUs.

> **2026-10-02 — Server settings, Phase 3: `tweak server gpu`; opencoti answered #608.**
> - `xollama tweak server gpu` sets the GPU policy:
>   - which GPUs models may use;
>   - priority (fill order, ahead of free memory);
>   - the backend for a GPU reachable through several;
>   - a forced link speed (GB/s, `4x16`, or a generation-and-lanes wizard);
>   - split auto/spread/single;
>   - split mode: layer, or row (CUDA only, unvalidated).
> - A model's own device pin still wins.
> - The engine takes one forced link figure per process, so a load spread over several GPUs gets the slowest forced figure among them; opencoti confirms that's right.
> - opencoti #609: per-device link force is queued at opencoti and needs the owner's go-ahead. It also gave the complete auto-policy list, the residency × rolling-window matrix (FINAL), the split modes (tensor unsupported), and confirmed the link probe is safe on an idle GPU. All of it is recorded in the plan.
> - Verified: mutation on the three placement hooks, race tests, lint, and a live smoke test.

> **2026-10-02 — Server settings, Phase 2: defaults for every model's settings.**
> - `xollama tweak server` now sets server-wide defaults for every `tweak model` setting a server may default: engine, flash attention, KV types, unified KV, residency, slots, session pooling and the speculative type.
>   - It uses the same flags, walk and consistency pass as `tweak model`.
>   - A bare run asks whether to change the defaults or the API key.
> - The defaults are merged under the model's own settings at launch (`WithDefaults`).
>   - The model wins, and a cache pair stays a pair.
>   - A default the model cannot act on is left out and logged.
>   - DCA, devices and council are a model's own settings and are not offered as defaults.
> - `tweak show model NAME` marks each setting `model` or `server`.
> - Verified: mutation, race tests, lint, and a live smoke test.
> - Next: Phase 3, `tweak server gpu`. Still waiting on opencoti #608: the auto policies, and residency mode vs the rolling window.

> **2026-10-02 — Server settings, Phase 1: `tweak envs` and `tweak show`.**
> - The server's `~/.ollama/xollama-settings.json` now overrides the environment.
>   - `envconfig.Var` and `XollamaOnly` read its `envs` section first.
>   - Engines get it through `envconfig.Environ()`.
>   - It is written only through `/api/xollama/settings`: from the server's own machine, never through a proxy, and with the key while one is set.
> - New commands:
>   - `xollama tweak envs NAME=VALUE`, `--unset NAME`, or no arguments to be asked;
>   - `xollama tweak show envs` (`--all` too), `show server` and `show model NAME`.
> - What each change does:
>   - Load-time variables apply at once; the command offers to unload the running models.
>   - Variables read when the server starts say a restart is needed.
>   - The API key and unknown names are refused.
> - With no settings file nothing changes. The Registry has a new `system-settings` row.
> - Verified: mutation (removing the `Var` hook fails two tests), the race detector, lint, and a live smoke test on a scratch server.
> - Next: Phase 2, server defaults for the `tweak model` settings.

> **2026-10-02 — Plan: server settings without environment variables.**
> - Owner: `tweak server` sets only the API key, but every server-wide setting should be real configuration. Asked for: `tweak server` (defaults for every pertinent `tweak model` setting), `tweak server gpu` (which GPUs, their priority, the backend, the PCIe link, split), `tweak envs` (environment overrides) and `tweak show server|envs|model NAME`.
> - Decided with the owner:
>   - The tweak file beats the process environment.
>   - Changes apply immediately where possible.
>   - A forced link is per GPU, set through a generation-then-lanes wizard.
>   - Priority is fill order.
>   - Split is auto, spread or single, plus the split mode.
>   - "Auto" means the engine's fit; residency mode and engine selection are offered as well.
> - Plan: `plans/system-settings.md`.
> - Engine questions to opencoti (#608): a per-device link force, every auto policy, residency mode vs `--kv-rolling-window`, split modes, and probing a busy GPU.
> - No code yet.

> **2026-10-01 — Fixes for 0427/0428's lost time: check tools read, folds stick, housekeeping is plain.**
> - `check_file`, and any tool whose name says check, lint, validate, verify or diagnose, now counts as read-only (5f29e671b). Researchers and critics get it, and the synthesizer's calls no longer count as changes. A write word still wins.
> - **Every fold now sticks.** Two causes, both found while testing the refused-root fold:
>   - `sentTokens` (194c9cab6, this morning) measured the client's raw messages through `apply`. `apply` drops a record that doesn't match, and the raw messages never match the members' view, so every fold was dropped the moment it was made. All 8 server folds of series 4 were dropped; series 3 had none. Now it measures the raw messages directly and the record is never touched.
>   - The refused-root fold and the idle fold folded the client's raw messages (`full`). They now fold the members' view (`hist`), as every apply does.
>   - Guards: `TestARefusedRootsFoldIsOfTheMembersView` (each site fails it alone) and `TestTheSentConversationIsMeasuredWhole` (fails with `apply` back in).
> - **A generic client's housekeeping is answered plainly.** A request without tools and without `council_chat_state`, whose messages already hold tool calls, skips the council: no members, no server fold. In 0427 each of Cerebriline's compaction requests took 13.5 minutes through the council. Edge case, documented in `council-integration.mdx`: a desktop chat that used web search and then turns it off is answered by the model alone after that. Guard: `TestAToolTasksHousekeepingIsAnsweredPlainly`, verified by removal.
> - Not yet run on eleven2go: testing there is paused for opencoti's gates.

> **2026-10-01 — Why council runs 0427 and 0428 lost time (analysis; no code change yet).**
> - Neither run ever got the game to parse. Every oracle run in both was a SyntaxError: the original one-bracket error on a long minified line (`dDec`) became "Unexpected token '{'" on the next method and stayed that way. 0427 ended as a TIMEOUT at 7,204 s. 0428 was stopped at 12:38 by the owner, with about 10 minutes of its cap left.
> - **The members could not locate the error.** Read-only tools are inferred from their names, and `check_file` has neither a read word nor a run word. So researchers and critics never get it, and for the synthesizer it counts as a change. 0428 called `check_file` 0 times (0427: 1), against 47 reads and 11 edits. The synthesizer edited a different long line almost every time, twice resending one unchanged. Plain 0425 called it 6 times, with 16 reads and 21 edits.
> - **Researchers and critics took most of the time.** 0428: 30 round trips with researchers or critics took 4,419 s (79% of the time in requests), against 31 synthesizer steps (1,079 s), over 5 synthesizer cycles. 0427: 3,070 s against 1,973 s.
> - **Server-side compaction is thrown away.** The refused-root fold in `councilChat` folds `full`, the client's raw conversation, while every other fold and apply uses `hist` (after `council.History`, which rewrites tool-call messages). The record's hash never matches, so each fold (about 6 minutes) is dropped by the next apply, the root is refused again, and another fold follows. 0428's last 20 minutes were three folds, each dropped.
> - **A client compaction runs through the whole council.** Each of Cerebriline's compaction requests in 0427 (route=direct, no tools) took 13.5 minutes: a server fold of the same conversation (6 min 16 s), a refused fold (5 min 39 s), then the summary the client asked for. Two of them, plus a 426 s round at 161k, account for most of 0427's last 2,000 s.
> - Analysis files: `/srv/ml/xollama-phase2/as-ollama/e2g-s4/` (`server-1.log`, `losttime-*.txt`).

> **2026-10-01 — A Q4_K_M council fits the 3090 at the same window, on kvarn2.**
> - Owner: IQ2_M costs about 3.6x the steps of Q4_K_M (v6 arms), and every council role pays it. New tags on eleven2go: `omni-council-q4km-kv2` (omnimerge-v4 Q4_K_M MTP, `kv: kvarn2`, `num_ctx` 196,608, 2 live slots, the council settings of `omni-council-think`) and its plain twin `omni-plain-q4km-kv2` (same, council off). A plain arm uses its council's KV type even where it would not need it (owner).
> - Measured on b208, 393,216 cells (196,608 x 2), all fully resident on the GPU:
>
>   | | IQ2_M + kvarn3 | Q4_K_M + kvarn2 |
>   |---|---|---|
>   | weights | 9,238 MiB | 15,339 MiB |
>   | KV cache | 5,672 MiB | 4,136 MiB |
>   | GPU in use | 19,202 MiB | 23,760 MiB (of 24,576) |
>   | one council turn | 282 s, 17,770 tokens, 50.7 tok/s | 221 s, 10,519 tokens, 47.4 tok/s |
>
>   The turn is one sample, not a measurement. About 800 MiB is left free, so 196,608 per slot is the ceiling for Q4_K_M on this card.
> - Also: `omni-plain-v4-iq2m-128k` (IQ2_M, 128k, default KV): `omnimerge-v4-mtp_tb:27b-iq2m-128k` with its `think_budget_message` fixed (it was a lone `"`).
> - Why plain omni went from ~300 s (0129) to ~2,000 s (0425): the oracle. Re-scored with v3, none of eight old fast fixes passes (0129: 16/23). The work grew about 4x (16 to 59 requests, 15.6k to 62k output tokens, 87-94% of it thinking), generation slowed from 60 to 46 tok/s (Q4_K_M MTP on stock to IQ2_M kvarn3 on opencoti), and prefill grew from 32 s to 613 s (contexts 28k to 98k). The 0129 setup reruns on Q4_K_M with the v3 oracle and medium thinking after series 4.
> - Council run 0427 (da40325d, first on the session fix): TIMEOUT in 7,204 s, the page does not parse. No engine abort; its conversation reached 161k.

> **2026-10-01 — A task sent again starts clean.**
> - A request with no assistant turn, on a session the server derived, drops what was kept for that session: the kept deliberation, the review desk, the held resume point and the compaction record. The derived id comes from the conversation's opening, so every run of the harness's task landed on the last run's session (0422, 0424 and 0426 all ran on `xo-efd33195b0dfad24`). The test showed this was worse than a stale note: without the reset, a re-sent task resumed the past run's council mid-turn from its held point, with no planner. A session the client names is never reset.
> - Guards: `TestATaskSentAgainStartsClean` (verified by removal) and `TestOnlyAConversationWithNoAnswerYetIsANewTask`.
> - opencoti fixed the overnight abort (#593 → #594): b32 `2610010811001`, patch 0489. The refusal is now an HTTP 400 `exceed_context_size_error`. It is a dev snapshot; eleven2go stays on b208 until the owner says otherwise.

> **2026-10-01 — Series 3 on eleven2go (2fa24de0): council 0 of 3 fixed, plain omni 1 of 2. A council's reported size is now the request's whole.**
> - Runs (cline 1bd850f60, generic wire, `PROVIDER=ollama`, `THINKING=medium`, 7,200 s cap):
>
>   | Run | Arm | Result | Time |
>   |---|---|---|---|
>   | 0422 | council | TIMEOUT, does not load | 7,203 s |
>   | 0423 | plain omni | broken, does not load | 2,874 s |
>   | 0424 | council | TIMEOUT, 18/23 (friction, run, jump, wall, left_edge) | 7,204 s |
>   | 0425 | plain omni | FIXED 23/23 | 2,012 s |
>   | 0426 | council | TIMEOUT, 22/23 (hud) | 7,203 s |
>
> - The check now runs through the turn: 0426 ran the program at 55, 587, 1,092, 1,469 and 1,861 s (0418: 51 s, then nothing until 1,989 s).
> - Where the council's time went:
>   - **The reported size was still a sum on resumed research rounds** (143k-393k on 0426; the window is 196,608). The compactor measured only the history before the turn's tool traffic. A generic harness's task is one message, so that history was too short to measure, and a turn with no front or planner call fell back to the members' sum. Cerebriline then compacted its conversation (0426: two compactions, about 1,500 s with the crash below). Fixed: the done chunk now reports the whole request the client sent, tool round trips included, rendered with its tools and tokenized (`sentTokens`). Guards: `TestTheSentConversationIsMeasuredWhole` and `TestTheDoneChunkReportsTheConversationsPrompt`, the latter verified by removal.
>   - **The engine aborted five times** (opencoti b208, `server-context.cpp:6852: GGML_ASSERT(n_ctx > 0 && n_prompt_tokens > 0)`, exit 6). Each abort came beside a "session allocation full … compact the session" refusal, while a compaction writer and the front ran together. Each cost about 10 minutes: a retry that waited 5 minutes on a closed pipe, then a 5-minute GPU discovery watchdog before the reload. Reported to opencoti.
>   - Every council run used the same xollama session (`xo-efd33195b0dfad24`), because the session id comes from the conversation's first message and the harness sends the same task each time. What a session keeps (the held resume point, the compaction record, the review desk, the kept deliberation, the root pool) may therefore cross runs. Not yet measured.

> **2026-10-01 — The done chunk reports the conversation's size; a turn's first run is its check.**
> - 0418's done chunks once reported a 229k prompt: the synthesizer's, which holds the plan and the findings on top of the conversation. Cerebriline sizes its context from that number and compacts at about 159k, so the council's own deliberation could fold the client's conversation.
> - The compactor now records what it measured on every pass (folded or not), and the done chunk reports that. Without a measurement it falls back to the front's or the planner's prompt; the synthesizer's is never taken as the conversation's.
> - Guards: `TestTheReportedPromptIsTheConversations`, `TestCompactMeasuresTheConversation`, both verified by removal.
> - A turn's check can now be inferred from one run. 0418 ran the program once at 51 s, before any change, and nothing ran it again until 1,989 s, because the check was inferred only from a call repeated across a change. Now, with no repeat, the turn's first non-reading call is the check when its tool runs something (run, exec, command, shell, bash, test). An edit tool's call is never taken, and a member's own inference still waits for a repeat. Guard: `TestATurnsFirstRunIsItsCheck`, verified by removal.

> **2026-09-30 — Council members get Cerebriline's output budget.**
> - Each member's reply cap is the least of three quarters of the council's window, the ceiling (`council.max_tokens`, default 16,384, settable 1,024-96,000) and the model's `num_predict`. It is the VS Code plugin's rule (#578), with a lower ceiling.
> - The thinking now sits inside the cap instead of on top of it. A level is a share of the cap, and `on` means medium. Each member is told its cap and its thinking share (Cerebriline's Output Budget section).
> - The reserve books the caps alone.
> - The Cerebriline harness now works for both plain arms:
>   - plain omni, 0411: FIXED 23/23 in 1,125 s;
>   - plain glm, 0412: FIXED 23/23 in 2,532 s.
>   Both ran on the CLI's old budget (49,152 / 12,288: the gateway fallback, not the plugin's 96,000 / 24,000), and 0412 overlapped a rebuild of Cerebriline's core (#580). They will be rerun once the CLI matches the plugin.
> - Runner-issued check: a directive's `check_call` (`council_check_call_v1`) is the turn's check, even through a shell tool. A synthesizer that changed something and ends without running it has the council make it. Needed because Cerebriline's `./run_game` goes through `run_commands`, which the council counted as a change (#581).
> - `council_read_many` dropped: `read_files` already takes several files, and one step already carries several calls.
> - **A generic harness now gets the council** (owner: the test is a council tag driven by a harness that does not know it is one).
>   - 0411 showed why this was needed: tools without `council_chat_state` had been a plain chat. Now they are the council's, and the server keeps the resume point itself.
>   - Read-only tools are inferred from their names.
>   - The check is inferred as the call repeated unchanged across a change.
>   - Resent thinking is not read by members.
>   - From this build on, a council tag is no plain arm: plain runs need a plain tag.
> - Plain arms on the parity build (96,000 / 24,000):
>   - plain omni, 0413: FIXED 23/23 in 2,197 s;
>   - plain glm, 0414: FIXED 23/23 in 1,189 s.
> - First council arm, 0415 (`omni-council-think`, bb7434ce): the council engaged on the plain request (front turn, 4,096 think budget), then ABORTED at 692 s. A researcher's forced report carried `maxLength` 2,000, which the engine cannot turn into a grammar. The fix drops the bound from the schema and asks a refused format again once without it.
> - A request whose `num_predict` is below 64 is a probe and is answered plainly. Cerebriline's fixed template probe sends two such requests (`num_predict` 1), and without this each would start a council.
> - Council arm 0416 (`omni-council-think`, 55d4baf7, generic wire, PROVIDER=ollama) ended **broken**: the game loads, but only 18/23 checks pass (friction, run, wall, left_edge and hud fail), after 4,815 s. The plain arms fixed it in 1,189-2,197 s.
> - The lane is paused (owner) while Cerebriline lands its `/chat` template-probe fix (#586/#587); eleven2go is on 19fd62cc. Runs after that fix are a new series, since past thinking may then be replayed.
> - **Why 0416 lost** (log in `/srv/ml/xollama-phase2/as-ollama/e2g-0416/`):
>   1. **Compaction, 1,987 s (41% of the run).** The done chunk's `prompt_eval_count` was the sum over all members, 161,214. The harness took that for its context size and ran its 5-step compaction on a ~50k conversation, each step a council turn. Fixed: the done chunk now reports the prompt of the last call on the conversation.
>   2. **Stopped at 18/23.** The synthesizer called the failures "minor" and said DONE. A reviewer refuted that. The synthesizer then re-sent the same unchanged result, and the second reviewer, fed only a file read, confirmed it. Fixed:
>      - a refutation stands until a change answers it, and DONE is refused meanwhile;
>      - a review with no change since judges the last check;
>      - the verdict wording counts any failure anywhere in the output.
>   3. **Slow to 18/23: 2,588 s, where plain took 309 s.** Six cycles, each fixing the one runtime error the last check named. Each cycle spent 3-5 min on planner, researchers and critics before the synthesizer's minute, and ended at the step bound with a RETEST. Fixed (owner approved):
>      - the synthesizer's step bound counts from its check's last change of output (`cycleSteps`), so it keeps its cycle while each fix moves the check;
>      - a cycle is capped at 4× its steps;
>      - a new research round starts only when the check stops moving, or when the synthesizer asks for one.
> - **Council arm 0417** (9d3a2d2f, Cerebriline d3c58e1a9, new series): TIMEOUT at 7,203 s, the game never loading.
>   - The synthesizer spent nine cycles on edits built on a wrong theory ("trailing semicolons" on class methods), and restored the original file twice.
>   - It ran the check twice in two hours. Every cycle ended at its step bound, never on a check result.
>   - The council-issued check never fired. The check had not been inferred, because the front and each synthesizer cycle start their own turns, so no one member's turns held two runs of it. And a cycle ended by its steps skipped the end-of-turn issue.
>   - Fixed: the check is inferred from the whole turn's tool traffic (`InferCheck`, server side), and a cycle whose steps are spent with changes unchecked has the check made before it ends.
> - **Council arm 0418** (6c03a9cf): **FIXED 23/23 in 3,333 s.** The first council fix driven by a generic harness.
>   - First edit at 788 s. The checks went 20, then 21, then 23 passed between 2,493 s and 2,956 s. No harness compaction ran.
>   - Left: the check was not run from 51 s to 1,989 s. The only earlier run was the front's, and inference needs two runs; a single run of a run-type tool could seed it.
>   - One reported prompt still reached 229k. A member's prompt can exceed the client's conversation.
> - Plain control for the council tag: `omni-plain-kv3-384k` on eleven2go, a copy of `omni-council-kv3-384k` with `council.enabled` off. Plain arms of the new series, one run each:
>   - plain omni, 0419 (`omni-plain-kv3-384k`): FIXED 23/23 in 2,672 s;
>   - plain glm, 0420: FIXED 23/23 in 419 s.
>   - The council, 0418, took 3,333 s: 1.25x plain omni on the same weights.

> **2026-09-30 — 11.29 started: step limits, a spot-checking critic, empty-reply retries.**
> - Plain omni through Cerebriline's harness (`native.sh`, eleven2go lane): FIXED 23/23 in 1,125 s. The engine budget was 12,288; asked Cerebriline why (#577).
> - Researchers take 4 tool steps and critics 3. A step is any turn that called a client tool. Past the limit the call is not made, and the member reports.
> - The critic checks only the claims the answer depends on.
> - An empty plan or report is asked again once without thinking. No two researchers share a fallback brief.
> - The scope of 11.29, with the owner's choices (16k default ceiling; all four structural changes), is in the plan.
> - Every member call is asked again when it fails or stalls (5 min with nothing sent, 20 min in all): up to twice, never for a refusal or a full owner.
> - Members end with typed results the council answers itself, rendered into the flow's existing form:
>   - researchers call `council_report`: proposals with their exact old text, claims, reads by ref;
>   - critics call `council_verdict`;
>   - a checking synthesizer calls `council_done` or `council_retest`.
>   At the step limit the member's call carries its result's schema as the format.
> - Each member step now replays the reasoning of the member's last step. Reasoning that hit its budget is replaced by a short note in the member's own voice (Cerebriline's condensation).
> - A compaction fold is now one call, the writer's. The critics' review and the retrospective are opt-in (`council.context.review`, `.retrospective`).

> **2026-09-30 — glm council fixed hard at 13x the plain cost. The council will be rebuilt on Cerebriline's budgets.**
> - **All-glm council** (every role on glm-5.3-flash:cloud, max_tokens 131072):
>   - Fixed hard, 23/23 traps.
>   - It took 10,866 s, 60 trips, 3.44M tokens in and 875k out. Plain glm took 300-814 s and 29k-70k out.
>   - The first edit came only after 62 min.
>   - One critic made 50 `find_text` calls in a row. The critics used 1.94M input tokens, about 44k per call.
>   - Researchers wrote a median of 10.6k tokens per call.
>   - The synthesizer broke the file 3 times before the fix.
>   - The owner stopped the rest of the batch (glm on one role at a time).
> - **Plain omni, thinking on** (`think_budget "medium"`, num_predict 131072):
>   - Failed, with the original SyntaxError, after 1,815 s and 21 trips.
>   - "medium" is a quarter of the reply room, so the engine budget was 32,768 (`common_reaso: activated, budget=32768`). The engine also forgives spent tokens on a reset sequence (up to 13,435 in this run).
>   - One turn ran 1,062 s with no tool call.
> - **Owner's direction:**
>   - Thinking stays on.
>   - The manic harness here is not reliable enough to decide anything; measure plain and council through Cerebriline's harness (eleven2go lane, oracle v3).
>   - Port Cerebriline's approach into the council: the automatic output budget; a customisable ceiling, low by default; mapping thinking levels and tokens onto the output; reasoning replay; reasoning condensation; and one-step agentic replay compaction instead of the 5-member fold.
>   - Asked Cerebriline for source pointers and the harness commands (mail #573).

> **2026-09-30 — solidPC's service (11434) moved to `0.35.0-dev.562eea98`.**
> - The owner asked for it. The payload was assembled with `scripts/docker-assemble.sh` from the pins: runtime b11081 (the inputs digest matches), upstream GPU backends, and opencoti b208. No native code was rebuilt.
> - It was swapped by `/srv/ml/xollama-phase2/solidpc-swap-0.35.0.sh`. The old binary and `lib/ollama` are kept as `.bak-20260930-103348`. The unit's drop-ins (11434, debug, parallel 1) are unchanged.
> - Checks: `/api/xollama` lists 11 features (council included); CUDA0 is on opencoti and the Vulkan iGPU on llamacpp; `qwen3.5:2b` ran on the opencoti engine at 130 tok/s.
> - The service now serves council models, which the 05c16dfa build refused.

> **2026-09-30 — Harness v3 (the logic oracle), the glm plain baseline, and 11.27 (a role's cap and window are its own model's).**
> - **Harness v3** (`/srv/ml/xollama-phase2`, `manic/README.md`). `manic/bin/run_game` is a compiled, obfuscated build of `manic/work/run_game.logic.js` (root 0600). It prints the old verdict plus `game_logic`: 23 behavioural traps on the game's own rules. A run is fixed only when the page loads and the traps pass. The model sees which traps fail but cannot read them.
> - **Re-score of every earlier run.** 12 of 28 old "fixed" runs keep the game's rules: omni council 2/12, omni plain 8/13, glm 2/3. Both of a0968aea's hard council fixes fail (empty `pop()`; falling through the floor). Table: `manic/work/logic-rescore-all-20260930.tsv`.
> - **Fixed in the oracle:** a countdown-trap false positive, which jumped the clock without frames, found live. Rebuilt; sha f13e481f. Sent to the ollama session for Cerebriline (#569-#571).
> - **Plain glm-5.3-flash:cloud, hard, thinking on, `num_predict` 131,072:**
>
>   | Run | Result | Trips | Wall | In | Out |
>   |---|---|---|---|---|---|
>   | 1 | fixed, 23/23 | 6 | 814 s | 80,463 | 69,755 |
>   | 2 | fixed, 23/23 | 13 | 572 s | 367,051 | 57,588 |
>   | 3 | fixed, 23/23 | 10 | 300 s | 144,875 | 28,985 |
>
>   Mean 562 s, 197k in, 52k out. The longest replies were 59k, 48k and 24k tokens, mostly thinking. The old 16,384 cap would have cut every run.
> - **11.27.** The council's built-in caps (1024-3072 plus 2048) would have cut every glm role. Now a role's stated `max_tokens` holds on its own model. Unstated, a cloud or other-model role inherits its template, and a lead role takes the council model's `num_predict`, else the built-in cap. Only lead roles are booked in the owner (`councilReserve`). There is a new `council.<role>.num_ctx` (schema v5), and members on another model shed the client's `num_predict` and `num_ctx`.
> - **11.28.**
>   - The owner's reserve now books each lead member's think budget beside its reply cap: a stated `think`, else the builder's ceiling of 4096. It had counted only the cap, although a member is sent cap + budget.
>   - A token budget is sent with the model template's `think_budget_message`, to local members and xollama endpoints alike.
>   - Measured in the omni councils: 22-29% of thinking calls exhausted the 2048 budget.
> - **Also fixed:** the route decision, the direct answer and the front turn ran on the lead whatever their role's model, as the first all-glm smoke run showed. They now run on the planner's and the synthesizer's model.
> - **Next.** Set glm roles to `max_tokens` 131,072 in the four glm councils; run all-glm, then critic, researcher and planner one at a time, with glm as the builder.

> **2026-09-30 — Hard on eleven2go with a0968aea, stopped at 4 pairs (the owner's call): council 2/4, plain 3/4.**
> | Pair | Council | Plain |
> |---|---|---|
> | 1 | fixed, 81 trips, 4,695 s | fixed, 59 trips, 1,353 s |
> | 2 | fixed, 46 trips, 2,278 s | fixed, 56 trips, 499 s |
> | 3 | lost at trip 41: a health dial failed (fixed on dev, `engine-health-retry`) | not fixed, 120 trips, 1,298 s |
> | 4 | lost at trip 118: owner full, waited out (fixed on dev, 11.26) | fixed, 64 trips, 590 s |
>
> The council's first fixes of hard. Neither council loss was a reasoning
> failure; both are fixed on dev and not yet measured live. Council run 5 was
> stopped at about 10 minutes, with its run directory marked `-STOPPED`. Next,
> the owner's direction: test with the cloud model.

> **2026-09-30 — 11.26: a full owner compacts and the turn resumes.**
> Council run 4 of hard (a0968aea, eleven2go) failed at trip 118. A 120k
> root plus a 39k stage filled the 196608-cell owner, and the next worker's
> 39662 cells did not fit. The engine said "compact the session"; xollama
> waited two minutes and failed. The owner's choice: compact on the refusal
> and retry the member. Built: owner-bound members are refused at once
> (`llm.ErrOwnerFull`, 507). They wait only while a sibling may give cells
> back. Otherwise the turn lets its pools go, folds the conversation, rebuilds
> the root, and resumes from the last checkpoint.
> `server/council_owner_full.go`, the `context-window` hook row extended.
> Four tests, each piece mutation-checked; server, llm and council pass under
> `-race`, lint clean. bug-186. Deploy after the batch.

> **2026-09-30 — Hard on eleven2go with a0968aea: 5 of 6 fixed so far; a failed health dial lost council run 3.**
> - Council: run 1 fixed in 81 trips (4,695 s), run 2 in 46 trips (2,278 s).
>   Before this build the council had never fixed hard. Run 3 ended at trip 41:
>   a health check to the live engine failed on loopback (`connectex: A
>   connection attempt failed…`), and the engine answered three seconds later.
> - Plain: run 1 fixed in 59 trips (1,353 s), run 2 in 56 trips (499 s).
> - Council run 1 kept its layers on 76 of 81 requests, and 30 builds prefilled
>   261k tokens. The layer log shows no per-researcher split.
> - Fix on dev, not deployed until the batch ends: `engine-health-retry`
>   (`llm/engine_health_retry.go`, one hook line). On opencoti only, while the
>   engine runs, a dial that failed is tried up to four more times with
>   backoff; stock llama.cpp keeps upstream's single failure. Three tests
>   (mutation-checked), `-race`, lint, 31 hooks registered. bug-185.
> - The batch continues on a0968aea (pairs 3–5), so all ten runs share a build.

> **2026-09-30 — 11.25: with broadcast on, researchers never shared their stage.**
> The pools of the same length on solidPC's hard run (13 and 14, 6724) were
> round 2's two researchers, one layer each. The member log shows their
> messages equal up to each one's own instruction, then the mates' notes. The
> layer was cut before the last user message (the notes), so it held the
> instruction. Now `council.OwnPart` cuts at the member's first instruction
> after the last plan. That also covers the synthesizer's appended system
> prompt and the nudges in a tool loop, which had put a member's own turns into
> its layer. Pool texts and where siblings diverge are logged
> (`council_layer_log.go`). Tests (mutation-checked); server and council under
> `-race`, lint clean. bug-184.

> **2026-09-29 — 11.24: a turn's stage layers are kept across its tool round trips.**
> On hard, 265,334 of the 582,509 tokens the pool builds prefilled were
> layers the previous request had just released. Each round trip is a new
> request, and only the conversation's root survived one. Now a request
> ending with the members' calls stashes the layers it used, and the next
> request of the same turn adopts them, on the same runner and kept root.
> Anything else releases them, as does 10 minutes without a round trip.
> `server/council_layers_kept.go`. Six tests (mutation-checked); server and
> council suites pass under `-race`, lint clean. Live on solidPC's 3090
> (569747788, `omni-council-ab5`, hard, 40 trips): 37 of 40 requests stashed
> their layers and the next adopted them. Resumes built no pool. Pool builds
> prefilled 275,292 tokens in 34 builds (6.9k a trip), against eleven2go's
> 582,509 in 68 (12.1k a trip, a different host and model, so indicative).
> Nothing failed to release. Not fixed in 40 trips (3,012 s). Open: two pools
> of the same length (6,724) built 7 s apart on one parent; the texts were not
> logged, so a duplicate build is not ruled out.

> **2026-09-29 — 11.23: the 12800-token windows were a reviewer sized by a character estimate.**
> The small windows on hard were the members sized to their own request (a
> background reviewer, the builder), not the council's working members (116
> requests in 196608-token pool windows). `ownWindow` counted characters/3 as
> tokens; a critic's 13196-token review was sized 12800 and refused, and that
> review was lost. Now the request is rendered and tokenized as sent (the
> tree's counter, handed to the background reviewer's member set); half the
> characters only if counting fails. Tests (mutation-checked), server and
> council suites under `-race`. Open: a reviewer shares no prefix with the
> tree and prefills its whole request each time.

> **2026-09-29 — 11.22: a stuck re-plan has to change the task list.**
> From the hard analysis: the planner said it would replace the failing part
> whole and kept every task open. Now, while the stuck note is in force, a
> re-plan whose list update refutes no task or adds none is asked again once
> with `keptNote` (which says what it left undone, and that a whole
> replacement is a task a researcher writes out and the synthesizer applies in
> one write); the second answer stands. `approachKept`/`keptNote`
> (`stuck.go`), `ReplanAgain` (`steps.go`), `run.go`. Tests on the real
> round-2 list and a run-level one (mutation-checked); council and server
> suites pass under `-race`. Not measured live yet (eleven2go lent to
> opencoti). Next, in the owner's order: the 12800-token member windows (the
> owner: not low priority), the engine memory note to opencoti, the hard
> rerun when eleven2go is free.

> **2026-09-29 — Why the hard pair ended as it did: the host ran out of virtual memory, and the council's change of approach never became a task.**
> Engine deaths (eleven2go server log, Windows event 2004): both followed a
> low-virtual-memory condition. 15:34:55: our b208 engine 33.2 GB + opencoti's
> own test engine on the 9070 (2609291336001) 21.8 GB; 15:36:37 SIGSEGV in the
> CUDA graph path. 16:00:30: 37.3 + 18.6 GB; 16:00:40 `cudaGraphInstantiate`
> out of memory, abort 0xc0000409; Edge crashed OOM the same second. Commit
> limit 63 GB on 31 GB RAM. Our engine's host footprint is large for a 10 GB
> model: 10 sequences, up to 32 context checkpoints each at 85-250 MiB (8.3 GiB
> live at the second death) plus the prompt cache. Contention, not a new defect.
> Plain did not localize the fault either: 20 trips on the same template-literal
> theory with the check unchanged, then `write_file` of the whole file at trip
> 22, which passes `run_gamefull.js` exactly as `reference.html` does. The
> council never used `write_file` (16 local edits). The stuck note fired (58
> times, "or replace the failing part whole"), and planner round 2 wrote "instead
> of piecemeal fixes, I'll replace the entire JavaScript section", but its
> briefs kept the refuted theory (task #4, template literals), left every task
> `open`, and assigned the rewrite to nobody; the synthesizer applies what
> members propose, so it made local edits again. Round 5 narrowed to "the Level
> class methods for unclosed braces" (the actual fault, `Level.dDec`) two
> minutes before the engine died, at trip 48 of 120. Also: four member requests
> got a 12800-token engine window; one critic's 13196-token request was refused.

> **2026-09-29 — A quantized V the engine refuses without flash attention is retried at f16 (`kv-fa-retry`); hard: plain fixed, council not, both runs ended in an engine death.**
> llama.cpp resolves `--flash-attn auto` from the actual placement
> (`llm_fused_op_flash_attn_probe`) and then refuses a quantized V. Upstream
> v0.35.0 sends the same flags and has no retry, which corrects my earlier
> claim that upstream falls back to f16 (only its old Go runner did). The
> owner chose a single retry: on exactly that refusal, relaunch once with V at
> f16 and warn, only for a V xollama asked for (the model's `kv.v` or
> `XOLLAMA_V_CACHE_TYPE`). `OLLAMA_KV_CACHE_TYPE` alone keeps upstream's
> failure. `llm/engine_kv_fa.go` plus three marked lines in
> `llm/llama_server.go`; four tests, the upstream gate mutation-checked.
> Verified live on solidPC's xollama-dev (`council-c365.sh`, now the
> `xollama-kvfa-wip` build, b208 at the pin): `omni-council-64k` (`kv.v`
> q8_0) CPU-only was refused, retried with `v=f16` (KV 98 → 128 MiB) and
> answered; the failed start cost 11 s. solidPC's 3090 was busy with
> opencoti's gemma-4-31B run, hence the CPU-only probe.
> Hard on eleven2go (5ce5f7e7, 120 trips, `MANIC_NUM_PREDICT=16384`):
> **plain fixed** in 24 trips, 232 s (oracle PLAYING), and then its next
> request died with the engine (`wsarecv: forcibly closed`); **council not
> fixed**, 48 trips, 1440 s, ended by a critic request over its 12800-token
> slot and then `CUDA error: out of memory` in `cudaGraphInstantiate` on the
> 3090. The first plain start ran without the reply cap and was aborted after
> one reply ran more than 20 minutes (kept as `*-ABORTED-uncapped`). Next:
> the eleven2go server log for both engine deaths.

> **2026-09-29 — Every load names its model in the server log (`load-log`).**
> The engines log a load only by its blob path. `server/load_log.go`, from one
> hook line before `newServerFn` in `server/sched.go`, now writes three Info
> lines first: `loading model` (name:tag, blob, shards, drafter, projectors,
> adapters), `model file` (family, quant, parameters, size, layers, embedding,
> heads, experts used/total, sliding window, trained context) and
> `model placement` (devices, num_ctx, parallel, batch, predicted memory). All
> from the GGUF metadata the estimator already parsed and the placement
> already made: no read, no computation. Logged on every engine, llamacpp
> included, by the owner's decision (a Go log line, not behaviour). Tests
> `TestALoadIsLoggedByTheModelsName` (mutation-checked) and
> `TestALoadWithoutAFileStillNamesTheModel`; Registry row `load-log`. Not yet
> deployed: eleven2go is running the hard pair.

> **2026-09-29 — Versions follow upstream's, with release candidates; PR #5 is `release: v0.35.0-rc.1.xollama`.**
> Owner's rule: a version always follows the upstream release dev is based on.
> Three forms: `v<upstream>-rc.<k>.xollama` (a candidate, the dev builds, never
> promoted), `v<upstream>-xollama` (the release, which must ship the tree of
> the last published candidate and so takes a short install check), and
> `v<upstream>-xollama.<n>` (a re-release, no candidates, full check). The
> owner first proposed `xollama.rc<k>`. Measured against both sorters in the
> release path, that sorts after `xollama.3` under semver (the updater) and
> between `xollama` and `xollama.1` under `sort -V` (the workflow), so the
> updater would have offered a candidate to an install already on the release.
> With the candidate before the fork's name, both sorters agree and no
> comparator of our own is needed. `xollama-release.yaml`: the title pattern,
> a `kind` output, the tag filters, and the tree rule for a release.
> `discord-announce.yaml` never announces a candidate. `app/updater`: the order
> is documented, and `TestReleaseNamesOrderAsTheyShip` plus
> `TestACandidateMovesToItsRelease` hold it (compiled for Windows here; they run
> on CI's windows and macos legs). RELEASE.md's "Versions and tags" and step 7
> (the short check) are rewritten; CLAUDE.md and the docker docs follow. PR #5
> (head 339ef4b6, notes as before: v0.35.0 base, b208, council 11.17–11.21)
> is retitled from the never-cut v0.34.4-xollama.3. Budgeted localization is
> dropped (owner); the escalate measurement is later.
> Hard runs started on eleven2go's 3090 (5ce5f7e7, plain then council, 120
> trips each, up from 60, since both arms ran out at 60 on 2026-09-28; plain
> at 120 stays at about 320k of the 384k window). The owner lifted the hold on
> eleven2go for the 3090; the 9070 XT stays opencoti's (mail #556 sent).

> **2026-09-29 — Medium council twice on 2da8f3fd (11.21): fixed twice; the stuck path fires live.**
> - Run 1: FIXED, 27 trips, 939 s, 1.21 M prompt tokens, 6 edits (0 missed).
> - Run 2: FIXED, 45 trips, 2,312 s, 2.99 M prompt tokens, 10 edits (1 missed).
> - The server log now carries `same` 78, `moved` 55 and the stuck note 41 times; on 4770e33b it was `same` 0, `moved` 149, stuck 0.
>   - In run 2 the planner saw "same" at re-plans 2 and 3, with the stuck note. Then the checks moved (4 and 5), and it fixed the task.
> - Medium council across today's builds: 26 fixed / 60 not / 31 fixed / 60 not / 27 fixed / 45 fixed. Plain: 11–15 trips, 125–132 s.
> - Deployed `5ce5f7e7` (the line-number hint) to eleven2go.

> **2026-09-29 — The consultants' answer to csl-2026-09-29-1156-aa48, and why they read old code.**
> - Their code review of `quote.go` was mostly wrong: no replay, a guard that disables itself, a per-turn budget. The main checkout they read (their `cwd`) was on `sync/upstream-v0.35.0` at `ce7f5147`, two fixes behind. Everything since was committed from a separate worktree on `dev`. My mistake, not the skill's.
>   - Fixed: the main checkout is back on `dev`, and the worktree is removed.
> - Kept from the review: a quote that copies the read's line numbers is now answered in place (`numberedQuote`, `TestAQuoteWithLineNumbersIsAnswered`, which fails with the branch off).
> - Held by the owner: their #1, budgeted localization (a narrowing brief when the check names no place), and the escalate measurement ("first we stabilize"). Their refined hidden-progress rule is recorded, not built.

> **2026-09-29 — Why the council missed the brace: every check read as progress (11.21).**
> - 4770e33b run 2 had 7 identical `SyntaxError`s, yet the planner was told "changed ... progress" every cycle: 149 `moved` notes in the log, 0 `same`. So the stuck note never fired, and the first theory (template literals) was never refuted.
> - The cause is `lastCheck` again. "The first read after the last change" took the synthesizer's read of the file it had just edited, after it had run the check and edited again. The e75c7c3e logs show the same: 140 `moved`, 0 `same`.
> - Fix: the check is the read-only call the member calls most after its first change. One repeating the previous check is taken first. Replayed over run 2, the stuck note fires from cycle 3.
> - Guard `TestTheCheckIsTheCallTheMemberChecksWith`, which fails on the old rule exactly as live.

> **2026-09-29 — Medium council twice on 4770e33b (replay): fixed once, unfixed once.**
> - Run 1: FIXED, 31 trips, 784 s, 1.51 M prompt tokens, 8 edits (1 missed). That is the council's fastest medium fix so far.
> - Run 2: UNFIXED at the 60-trip cap, 1,644 s, 4.10 M prompt tokens, 11 edits (2 missed).
>   - It failed in a different way: all 7 checks returned the same first error (the stray `}` ending `dGrid`), and the council never located it.
>   - It made 36 `find_text` searches, 14 of them by researcher 2, and edited elsewhere.
> - Misses at the client fell from 13 of 32 edits (`cf223635`) to 1 of 8 and 2 of 11.
> - Sent to the consultants as follow-up `csl-2026-09-29-1156-aa48` (7 of 11 in the chain), with a review request for `quote.go` and `directive.go`, the re-plan cost, and the escalate measurement.
>   - Per the owner, n = 5 per cell with the seed fixed is planned for later.

> **2026-09-29 — 11.20: the read follows the member's own changes.**
> - The member's own changes to a target since its last read, when they went through, are now replayed onto that read. A whole write is taken as the text itself.
> - So a misquote right after an edit is answered in place too. On `cf223635` those were nearly all of the 13 misses.
> - Guard `TestTheReadFollowsTheMembersOwnChanges`, which fails with the replay off.
> - Next: deploy, and run medium twice.

> **2026-09-29 — Medium council after 11.20: fixed once, unfixed once; 78e49747 deployed on eleven2go.**
> - Run 1 on `96ff1f5d`: FIXED, 26 trips, 1,078 s, 3.20 M prompt tokens, 6 edits (1 missed at the client). This is the council's best result on medium so far (earlier: 34 / 54 trips).
> - Run 2 on `cf223635`: UNFIXED at the 60-trip cap, 3,786 s, 5.67 M prompt tokens, 32 edits (13 missed at the client).
>   - This build adds the cue line in the front's prompt. At n = 1 each, run-to-run variance can't be told apart from the change.
> - Why the guard let run 2's misses through: almost all of them follow a successful edit to the same file (trips 34→35, 39→40, 45→46). The guard then treats the member's last read as stale, by design, and forwards the edit. One followed only a partial `find_text` read, which it also skips.
>   - Next step for 11.20: replay the member's own successful edits onto its last read, so the read stays current after a change.
> - Deployed `78e49747` (v0.35.0 + everything above) to eleven2go: `0.35.0-dev.78e49747`, `council_directive_v1` advertised, `/v1/systemone` answering.

> **2026-09-29 — Upstream v0.35.0 synced (fork manifest `b723d1ce`).**
> - `sync/upstream-v0.35.0`:
>   - `1ab773d7` merged the tag. One conflict, `server/routes.go`: the carried tokenize routes stay beside upstream's new `/v1/systemone`.
>   - The 24 patches followed as `--no-ff` merges at the manifest's shas, in order (`9d7b8793` … `7d479188`; the table is in CARRIED-PATCHES).
>   - Then `dev` was merged in.
> - Every rebased patch has the same diff as at v0.34.4 (`git patch-id`). Conflicts were resolved `-X ours`, owner-authorized; every merge was checked to add nothing over its first parent.
> - Fixed: six new upstream tests in `cmd/bench/bench_test.go` set `OLLAMA_HOST`, which the fork ignores by design, so they reached a live server (404). They now set `XOLLAMA_HOST`, as the file's other tests do.
> - Checks: `go test ./...`, `-race` on the main packages, `golangci-lint` 0 issues, `check-hooks` and `check-compat-origin` all pass.
> - llama.cpp stays b11081, and the runtime pin does not move. `main` stays v0.34.4 until the next release PR.

> **2026-09-29 — The council for harnesses: instructions, the directive and the user's cues (plan Phases 1–3).**
> - `ChatRequest.Council` (`council_directive_v1`) states the turn's `mode`, `instructions`, `build`, `evidence` and `check`.
>   - `answer` is the synthesizer alone; `escalate` and `deliberate` start the council with no front turn or route decision.
>   - A stated build replaces the builder, and its `max_tests`/`max_steps` stand within the builder's bounds.
>   - The evidence is verbatim, and the agent's last check is the council's first comparison point.
>   - A named check tool replaces the `lastCheck` guess. An unknown mode or slot is a 400.
> - Instructions by slot, from the model (`council.instructions`, `council.<role>.instructions`) and the request. The council-wide ones sit in the charter's shared prefix.
> - The route decision and the front read the user's cues about care. A stated mode skips them.
> - Also: the medium council on `96ff1f5d` (11.20) is running on eleven2go. The upstream v0.35.0 sync is on `sync/upstream-v0.35.0`, paused at `up-think-budget`'s conflicts. All 24 rebased patches have the same diff as before (`git patch-id`), so HEAD's side is right; resolving them needs the owner's go-ahead.

> **2026-09-29 — The consultants answered; a misquoted change is now answered in place (11.20).**
> - `csl-2026-09-29-0846-320d` (34 min). Ranked: (1) a hidden-progress signal, (2) repairing edit quotes, (3) the harness directive, (4) batching by a runtime prompt, (5) runtime escalation, (6) Stream-and-Sift. Measure n = 5 per cell with seeds fixed and jitter 0 before the cloud tests.
> - Built (2): `internal/council/quote.go`. When a writer's own last read of the same target shows a long start of the change's quote but not the rest, the call is answered in place with the read's own text where the match stops. No trip. At most 4 per member. Four tests, three of which fail with the guard off.
> - Not built (1), as proposed. Its signal ("same check output, but a write was accepted, so continue") is exactly 96edc4ae's failure: accepted syntax conversions and the same error six times. It would undo 11.19. The correction also stands: on e75c7c3e the rewrites of `dIt`/`dPw` were no progress, and the real faults surfaced one per check, as in plain.
> - Not built (4). It is a prompt at the step budget, and plain also edits once per trip. The cost is the re-plan cycles (≈1,300 of 1,954 s), not the synthesizer's trips.
> - The harness review is recorded in `plans/council-harness.md` (Review): verbatim evidence, `answer` skips the route, instructions in the charter, the mode holds for the turn, and an optional check tool named by the harness.

> **2026-09-29 — Plain medium on e75c7c3e: fixed in 15 trips / 125 s. A correction: `dIt`/`dPw` were never faults.**
> - Plain: 15 trips, 125 s, 549 k prompt tokens, 3 edits, all applied. It changed exactly three places: the extra `}` ending `dGrid`, a missing `sX`, a missing `initClouds`.
> - Correction to earlier entries and to the consultants' brief: `} })};` at the end of `dIt` and `dPw` is valid JavaScript (a class body allows a stray `;`; checked with node).
>   - The council's seven failed edits on `dIt`'s ending were attempts to fix text that was never broken. Those rewrites were no progress, not hidden progress.
>   - What diagnosing a non-fault costs is now part of the consultation (injected into `csl-2026-09-29-0846-320d`).
> - Plain run 2 confirms it: fixed in 11 trips / 132 s, 305 k prompt tokens, 3 edits, all applied. It fixed `dGrid`'s brace, added `sX`, and added the missing top-level functions in one block. Again it never touched `dIt` or `dPw`.
> - Council vs plain on this build: 34 / 54 trips vs 15 / 11, and 1,954 / 3,054 s vs 125 / 132.

> **2026-09-29 — Medium council run 2 on e75c7c3e: fixed again, 54 trips / 3,054 s.**
> - Two of two runs fixed on this build (34 trips / 1,954 s, then 54 / 3,054). Plain: 12 / 114.
> - Run 2 cleared the syntax errors by trip 35, then followed each runtime error one per check (`initClouds`, `setupLevel`, `sX`, `gameLoop`). 6 of its 21 edits missed their text.
>   - Re-plan cycles cost 140–290 s per trip; the synthesizer's own trips cost 15–30 s.
> - Usage (calls / prompt tokens): synthesizer 62 / 2.08 M, researcher 50 / 1.44 M, critic 19 / 0.51 M, planner 7, reviewer 6, front 5, builder 1. Total 4.21 M.
> - Both runs and today's fixes went to the consultants as a follow-up of `csl-2026-09-28-1844-26be`. The harness plan went with them for review.

> **2026-09-29 — Plan: the council for harnesses, and an integration guide.**
> - The owner wants the council adaptive in a chat, and usable by agents and as an escalation path, with Cerebriline first. A harness should drive the builder and the council, and say how the council is built, to save trips and avoid mode switches.
> - New plan `plans/council-harness.md` (PROPOSED):
>   - Phase 1: instructions for the council and the builder, at model and request level.
>   - Phase 2: the harness directive `council_directive_v1` (`mode` answer/escalate/deliberate/auto, a stated `build` that replaces the builder call, `evidence` as prior failed checks).
>   - Phase 3: adaptive chat from the user's cues.
>   - Phase 4: Cerebriline, built by the ollama session, with quality feedback.
> - Phase 0 built: `docs/xollama/council-integration.mdx`, the integration guide for harness authors. It covers today's contract, the three use cases, and the planned parts marked as planned.
> - Phases 1–4 wait for the consultants' answer to the follow-up, which is sent after medium run 2.
> - Owner's decisions: the field is `council`; `answer` mode offers no hand-off; a harness `build` may override the model's `max_tests` / `max_steps` up or down (a user setting in the harness), within the builder's bound.

> **2026-09-29 — Fork patch 24 merged: `up-modelfile-roundtrip` @ `c8e22c11` (manifest `23a43c77`, fork-only).**
> - `show --modelfile` writes `TEMPLATE` only for a model that carries one. `create` refuses an unterminated `TEMPLATE`/`SYSTEM` quote that would have swallowed the directives after it.
> - One conflict, in `server/images.go`: xollama's own `modelfile-roundtrip` hook carried the same condition. Resolved to the patch's side. The hook and its registry row are retired, since the fork now supplies the fix.
> - `go test ./parser ./server ./cmd` passes, and `check-hooks` reports 28 hooks.

> **2026-09-29 — Medium council on e75c7c3e (eleven2go 3090): fixed.**
> - **Fixed** in 34 trips and 1,954 s, and declared DONE. The first council fix of medium since d17af426 on b203 (19 trips / 778 s).
>   - Nine checks: five identical SyntaxErrors, then a changed error (`missing ) after argument list`), then back to the first error, then passing.
>   - Twelve edits, three of which missed their text.
> - Plain on the same build and host: 12 trips / 114 s, so the council is 17× slower.
> - Usage (calls / prompt / output tokens):
>   - synthesizer 35 / 1.20 M / 18 k;
>   - researcher 28 / 0.87 M / 35 k;
>   - critic 8 / 0.20 M / 9 k;
>   - planner 4, front 5, reviewer 4, builder 1.
> - The first attempt on this build died at engine startup: an engine orphaned by the test deploy script held its pinned memory. The script now unloads models before stopping xollama.

> **2026-09-29 — Why the council kept a refuted explanation: its "moved" signal was wrong.**
> - In the 96edc4ae transcripts, the planner converted one kind of syntax, then more of it, then another kind, while every check returned the same SyntaxError.
> - The runtime told it each check had moved. It took the cycle's last read-only call as the check, and that was a search after `run_game`. The stuck note never fired.
> - Fixed (plan 11.19): the check is the first read after the cycle's last change. The stuck note now also says the explanation behind the unmoved changes is refuted.
> - `TestTheCheckIsTheReadAfterTheLastChange` fails on the old code exactly as live.

> **2026-09-29 — Medium council on 96edc4ae (eleven2go 3090, 384k context back): unfixed, no stalls.**
> - 60 trips (the harness limit), 1,695 s (was 4,137 s on 837a1fce). No cut replies and no admission refusals: the cap and placement fixes held.
> - 23 edits "succeeded". Every check still returned `SyntaxError: Unexpected token '{'`, with no line number.
>   - The members rewrote syntax they suspected: arrow functions to `function` (which breaks `this` in `gen`) and template literals to concatenation.
>   - The extra `}` ending `dGrid` was never touched.
> - Plain fixed the same task in 12 trips / 114 s by reading the file and rewriting the broken class whole. The council's failure is now diagnosis, not capacity.
> - Usage: synthesizer 59 calls / 1.81 M prompt tokens; researcher 40 / 1.11 M; critic 13 / 0.34 M; planner 6; front 5; reviewer 3; builder 1.
> - 688d1f53 (owner makes room) deployed afterwards.

> **2026-09-29 — The owner makes room for the builder and the reviewers.**
> - A member booked on its own session beside the conversation's owner could wait out its whole admission budget when the owner held the whole window. That was the builder's 2-minute refusal on 62c7d5ec.
> - `roomFor` (plan 11.18, `server/council_room.go`) reads `/kv`'s `largest_admissible`. When it is short of the member's window, the owner shrinks by the difference, never below its used cells plus the turn's reserve. The shrink is applied at once, or deferred if the engine refuses. The next turn grows the owner back.
> - Two tests; the call was checked by removal.
> - Not yet deployed: the medium council run on 96edc4ae is still going on eleven2go.

> **2026-09-29 — Medium council rerun on 62c7d5ec: stopped by the placement fix, now fixed again.**
> - The first rerun without the CUDA pin ended after 5 trips. The engine refused the builder for 2 minutes ("no room"). Nothing to do with the cap fix:
>   - `opencotiPlacement` (b22e5649) filtered the GPUs only where the model is placed;
>   - `liveSlots` and the deny-list still saw the 3090 and the 9070 XT together, answered "not opencoti", and dropped `slots.live 2` to one slot;
>   - the engine launched with `-c 196608`, not 393216, and the conversation's owner booked the whole window.
> - Fix: the filter runs in `processPending`, right after the device pin, so the whole load sees it. The new `TestAModelOnlyOpencotiServesLoadsWithItsOwnSlots` fails with the old position ("launched with 1 slots, want slots.live's 2").
> - Also seen: an unpooled member (the builder) waits for its full timeout when the owner holds the whole window. Not changed here.

> **2026-09-29 — Why the medium council failed, and the fix: a writer has room for its edit.**
> - Plain medium on 837a1fce + b208 (eleven2go 3090): **fixed** in 12 trips, 114 s, 208 k prompt tokens, 5.2 k output. It read the file and rewrote the whole broken class in one `edit_file`.
> - The council's front and synthesizer set out to make the same rewrite. The front's last three calls each stopped at exactly 3,072 output tokens, the cap, inside the tool call, so only the prose survived. The class is about 3.3 k tokens, and an edit carries old and new (about 6.6 k).
> - Built (plan 11.17, `internal/council/cut.go`):
>   - a writer's cap on a tool turn is at least 16,384;
>   - `done_reason "length"` reaches the council as `Reply.Cut`, and a cut writer is asked again once for smaller steps.
>   - Four tests, each checked by removal.
> - Next: deploy to eleven2go with b22e5649, drop the `--device-backend=cuda` workaround, rerun the medium council. Cloud tests wait until the council works.

> **2026-09-28 — Medium council on 837a1fce + b208 (eleven2go 3090): unfixed.**
> - 57 trips, 4,137 s. Every one of the 10 checks returned the same `SyntaxError: Unexpected token '{'`, which carries no line number.
>   - The fault is in `dDec`, which no member ever read, the same miss as simple run 7.
>   - The members edited template literals, braces elsewhere and `sX`, and 5 edits missed their text.
>   - The run ended with 4 trips of narrated intent and no tool call (3 nudges), which the harness scored as DONE.
> - For reference, d17af426 on b203 fixed medium in 19 trips / 778 s.
> - Usage (prompt share / output share):
>   - synthesizer 44 % / 31 %;
>   - researcher 36 % / 45 %;
>   - critic 10 % / 13 %;
>   - front 9 % / 7 %;
>   - planner 1.3 % / 3.5 %.
> - Totals: 5.39 M prompt tokens (2.5 M cached), 145 k output.

> **2026-09-28 — A model only opencoti can serve stays on opencoti's GPUs.**
> - eleven2go has an RX 9070 XT (Vulkan) beside its RTX 3090. The medium council model (kvarn3, 384k) fits no single GPU, and upstream's placement picked the Vulkan backend by free memory. The launch refused it: kvarn3 needs opencoti.
> - `opencotiPlacement` (registry row `opencoti-placement`) now keeps such a model on the GPU groups opencoti serves; nothing changes for other models or with `XOLLAMA_ENGINE=llamacpp`.
> - Workaround for the current run: `omni-council-kv3-384k` pinned to `--device-backend=cuda`.
> - Also measured on eleven2go (2k prompt, 512 gen), xOllama on the 3090:
>   - omnimerge-v4 IQ2_M: 48 tok/s gen, 1,150 prefill;
>   - qwen3.6 35b-a3b: 189 / 3,040;
>   - v9-agentic: 124 / 4,600.
> - On the 9070 XT, xOllama (Vulkan) matches stock Ollama within noise. b208 has no Windows Vulkan payload, so there is no rolling-KV on AMD yet.

> **2026-09-28 — xOllama's own icon, and an installer that stops only xOllama.**
> - The icons are the llama with a red X painted on its chest, from `scripts/xollama-icon.py`; the owner picked red over violet (preview artifact).
> - The installer and uninstaller no longer run `taskkill /im llama-server.exe`, which stopped a stock Ollama's runners and missed the opencoti engine.
>   - `app/xollama-stop.ps1` stops what runs from the install directory, the engines and runners those started, and orphaned opencoti engines, then waits for them to exit.
>   - It runs from `PrepareToInstall`, before any file is copied, and from `[UninstallRun]`. It replaces the 5 s timeout hack.
> - Not yet run on Windows: eleven2go is rebooting for updates.

> **2026-09-28 — The desktop app says xOllama.**
> - Found on eleven2go: two identical trays, both called Ollama.
> - Tray tooltip, menu (Open/Quit xOllama), notifications and window title now come from `wintray.AppName`.
> - The UI's ~170 "Ollama" strings are rewritten at build time by `app/ui/app/xollama-brand.ts` (a Vite plugin), not edited, so UI merges stay clean. "Ollama account" and "Ollama.com" stay, since they name ollama.com.
> - Registry row `app-brand`. The new icon followed (entry above).

> **2026-09-28 — The xOllama tray no longer exits when a stock Ollama tray started first.**
> - Found on eleven2go after the 9070 XT reboot. At logon the xOllama app logged "existing instance found, exiting": its single-instance check is `FindWindowW` on the tray window class, and the class was still upstream's `OllamaClass`, which Ollama's own tray had already registered.
> - It also sent Ollama's app a focus request. The class is now `xOllamaClass` (the `app-state` hook). The source guard in `internal/onboarding` flags `"OllamaClass"` and fails without the fix.

> **2026-09-28 — Engine pin moved to b208 for v0.34.4-xollama.3 (owner's ruling).**
> - opencoti `2609281805001` (b208, rev `f8fc1116`, mail #540): the V100 fixes (KVarN collapse 0447, the engine picking the CUDA 12 payload below cc 7.5), the #530 scatter assert (0448), the drafter-KV fixes (0449/0450) and the wider TinyBLAS GEMM (0451).
> - Measured on these bytes (`as-ollama/b208-ab`), against b177: compat 8/8; llama3 76.8 tok/s (75.5); multislot 144 (142); gemma4 identical.
>   - Overflow read 3.32 against b177's 3.85. Paired, alternating reruns read b177 at 3.28 and 3.34, b208 at 3.36 and 3.29: noise.
>   - Two-turn `/api/chat` held on two models, with 0 REFUSED. The argv probe (`probe208.sh`) matches b177 on every row. All four shas were checked on HF at the rev.
> - No defect row is retired. Docs name b208.
> - Next: release PR `release: v0.34.4-xollama.3`, a pre-release (not promoted).

> **2026-09-28 — council 11.16: the builder gets its own model.**
> - `council.builder` (`model`, `host`, `think`, `max_tokens`; no `prompt`, no `count`); unstated, it runs on the planner's, as before.
> - For the owner's cloud tests: the builder on `glm-5.3-turbo:cloud` while the critic, researcher or planner moves there, one at a time.

> **2026-09-28 — council 11.15: loop guards from Cerebriline, the front's handoff, no idle researchers.**
> - The consultants' #3–#6, as the owner took them (#2 waits for the flow-modes follow-up):
>   - a no-op change is refused in place;
>   - a changing call resent with the same result gets Cerebriline's steering ladder, and at 4 strikes the member reports;
>   - a refused change is redone from the text as it is now;
>   - the front stops at 3 steps, and its current reads reach every member as research, not as a failed attempt;
>   - the planner keeps every researcher working.
> - Next: deploy, then measure.

> **2026-09-28 — b203 holds on eleven2go; the council missed simple on it.**
> - opencoti #530 rerun: b203 host (2609281335001) with the b201 Win DLL, the `head` relief removed (`auto` residency, POSITION_WINDOW on), 4 live slots, unified KV.
>   - The simple council ran 60 trips in 2028 s, 5 cycles, with no assert; log sent (#538).
>   - eleven2go now runs b203 through `XOLLAMA_ENGINE_PATH`. Revert: clear it, and set `XOLLAMA_ENGINE_ARGS=--kv-residency-mode head`. The pin is unchanged.
> - The council did not fix simple on d17af426: the same "missing )" for all 13 checks.
>   - The planner moved off template literals to "unbalanced parentheses or brackets", but only ever counted `()`/`[]`, as the error names. `dDec`, which holds the stray `}`, was never edited.
>   - This is one run against run 6's fix (34924cbe), with the engine and the prompt both changed and sampling varying, so it needs repeats before it says anything.
> - Usage: the synthesizer took 66 % of the prompt (59 calls, 2.6M tokens), the researchers 22 %, the critic 10 %, the planner 0.7 %.
> - opencoti #535: #528's 20 % loss was a bug (quantized drafter KV read in the wrong basis). It is fixed in b206, where an inheriting q8_0 drafter costs nothing; the pin is the owner's call.

> **2026-09-28 — council 11.14 measured: medium 778 s (was 1101 s); first real per-role usage.**
> - Medium on d17af426 (eleven2go): council **fixed**, 19 trips, 778 s (was 27 trips, 1101 s; plain: 11, 91 s). One batch of edits fixed the stray brace and every missing function at once.
> - Per-role usage (`council_usage_v1`, tokens), as prompt share / output share:
>   - synthesizer: 52 % / 22 %
>   - researchers: 28 % / 54 %; they also spent the most decode time (310 s)
>   - critic: 10 % / 13 %
>   - front: 7 % / 5 %
>   - planner: 2 % / 5 %
>   - reviewer and builder: 2 % / 2 %
> - About 45 % of the prompt came from cache.

> **2026-09-28 — council 11.14: the synthesizer applies the proposals together.**
> - The consultants council (csl-2026-09-28-1441-4bbf) traced medium's slowness to our own synthesizer prompt: "one at a time … after each change, run its check".
> - Now the synthesizer makes every non-conflicting proposed edit in one reply and checks once. It fixes a next failure its check already shows, and hands back only what needs investigating. Researchers report every fault in their part.
> - Hard on 032db6c2: **a cliff for both arms**.
>   - Council: unfixed at the 60-trip cap, 4129 s; last error `Unexpected token ')'`.
>   - Plain: unfixed at the cap, 538 s; last error `Unexpected token '{'`.
>   - Both got past the first "missing )" and stalled on the misplaced braces further down.
>   - Plain's usage (the first run recorded): 60 calls, 4.2M prompt tokens sent (2.06M cached), 21k written.
> - Next: deploy, medium against plain's 91 s; then the role-upgrade matrix (follow-up csl-2026-09-28-1521-242f).

> **2026-09-28 — council: medium fixed, 12× slower than plain; usage per role reported.**
> - Medium on 032db6c2 (eleven2go): council **fixed**, 27 trips, 1101 s. Plain **fixed**, 11 trips, 91 s. It read the file once, fixed seven faults in four edits and checked once; the council paid a whole check cycle per fault.
> - The consultants council has been asked for optimizations (brief: `/srv/ml/xollama-phase2/consult-council/BRIEF.md`).
> - Built: `council_usage_v1`. Each council done chunk reports what each role spent (calls, prompt sent and cached, written, durations), and the manic harness records it per trip and per role (plan 11.13).
>   - First reading, medium: the researchers take 45 % of the prompt and the synthesizer 43 %; the critic 7 % and the planner 2 %.
>   - Prompts are about 68× the output.
> - Hard is running (council, then plain).

> **2026-09-28 — council: simple fixed for the first time; task ids keep the list's numbering.**
> - Sixth simple run (34924cbe, eleven2go, `head` relief): **fixed**, 40 trips, 1401 s (plain: 27 trips).
>   - Cycles 1–2 chased template literals (seven harmless edits) against the same check output.
>   - The stuck note fired, and cycle 3 replaced the `dDec` method whole. The error moved to `sX`, then to `collide`, and the council followed each one to the fix: 11.10–11.12 working as designed.
>   - The planner's re-plans numbered the task list from 0 again, so its updates landed on the wrong tasks.
> - Fixed: plans are kept with the list's ids, tasks are matched by their words before their id, and the rule says never to renumber (`TestARenumberedListUpdatesTheTasksItNames`, checked by removal).
> - Next: medium on 032db6c2 (running), plain medium for a baseline, hard once, then the b203 rerun for opencoti #533.

> **2026-09-28 — council 11.10–11.12: a task list for the planner, stuck checks, reviews with verdicts.**
> - Fifth simple run (b336b144): unfixed, 60 trips, 1619 s. Six reviews fired and were mostly right to refute, but only one carried its `REVIEW:` line. Every check returned the same "missing ) after argument list". The plain arm fixed the task in 27 trips, with a whole-file rewrite at trip 20, then followed the new errors.
> - Built:
>   - the planner keeps a task list in its plan (`tasks.go`, with rules the runtime enforces, carried across turns and dropped on a rebuild), and the builder writes its instruction as a coordinator's;
>   - two checks in a row with the same output tell the council to change approach (`stuck.go`, topic-agnostic), a changed output counts as progress, and a whole-part replacement is allowed;
>   - a review without its verdict is sent back once with the structure, then marked unclear.
> - Tests were checked by removal; the full sweep, race, lint and hooks are clean.
> - Next: deploy to eleven2go, rerun simple; if it passes, medium and hard once each.

> **2026-09-28 — council: the builder reads only the user; unsent checks are sent; reviews stream.**
> - Fourth simple run (5b331d1b) peeked mid-run: `n_slots = 4 (live = 4)`. The builder no longer names a place or a cause. The synthesizer never called `council_review`.
> - Built: the builder reads the system prompt and the user's messages only, on its own session (`5b331d1b`). A check the synthesizer moves on from (its next call changes something) is sent for it, and the DONE's check reuses that id. Reviews stream as `Reviewer N` thinking at delivery (`b336b144`).

> **2026-09-28 — council 11.9: the critics review the synthesizer's checks while it works.**
> - Third simple run on eleven2go (311010f9, `--kv-residency-mode head` relief): unfixed, 60 trips, 3466 s, no engine fault. The synthesizer made 27 edits against 11 checks with no second reader.
> - Built, as the owner designed it: `council_review` queues a check (the change plus what the calls returned). A per-conversation desk runs one reviewer per critic in the background, and each finished review reaches the synthesizer before its next step. Only DONE waits for the reviews still out, and sends its last check itself when it was never sent.
> - Next: deploy, rerun simple on eleven2go.

> **2026-09-28 — council 11.8: all the engine's parallel slots; cloud members counted apart.**
> - The council ran with 2 live slots of eleven2go's 4, because xollama sized them to its widest step. It now starts with the engine's parallel ceiling (4 by default, `slots.max`), with its local width as the floor.
> - Members on a cloud model take no engine slot: they run `council.cloud_parallel` at a time (default 3, `--council-cloud-parallel`), one count per council model.
> - Next: 11.9, the critics reviewing the synthesizer's checks asynchronously (owner's design: queued, each finished review returned as it lands).

> **2026-09-28 — council 11.7: every message names its writer; earlier turns attributed.**
> - eleven2go simple rerun on b96e3c96: unfixed, 667 s / 24 trips. The engine asserted in cycle 3 (opencoti #530: position-window scatter, two sequences prefilling, kv-unified, `auto` residency). Cycle 2 had edited the right line and found both missing functions. The bounds held (front 3 steps, synthesizer 6 per cycle).
> - The owner's points: the history should show the member; wrong claims must not stay in view; council messages must name their role and the user's stay plain; no anchoring. Built: `council.History` (calls split per member and headed, working notes dropped, repeats pointed at), source headers on every council message plus `sourcesNote`, and the front's report cut to its calls and results.
> - Relief on eleven2go: `XOLLAMA_ENGINE_ARGS=--kv-residency-mode head` (user env) until #530 is fixed.
> - Next: redeploy, simple on eleven2go, then medium and hard once each.

> **2026-09-28 — council 11.6: ab-5 read, and the fixes it asked for.**
> - ab-5 simple: plain fixed it on both hosts (solidPC 339 s / 30 trips capped, eleven2go 162 s / 20). The council did not (solidPC 3405 s / 60, unfixed; eleven2go 659 s / 24, declared done unfixed).
> - Members are served exactly as a plain turn: same ChatHandler, template and options; 2 of 140 replies hit a cap. The delta is context. The client history carries every member's calls as one assistant, five copies of the file and the front's wrong claims. Findings arrived as user messages and were obeyed. The builder anchored every role on the front's theory. The synthesizer investigated for 16 trips instead of testing.
> - Built (A–J): the builder never names a cause; failed checks are carried across turns (`Progress.Prior`, state field 10); the verdict is nudged once and never re-streamed; a synthesizer step budget (`MaxSteps` 6, builder `max_steps`, Build field 5); a test note that forbids investigating on its own; the front's attempts reach the council; localize-first coding example; critics told they cannot test; findings framed as claims; the front forwards before investigating (4 steps, then forced). Guards are in `internal/council/checks_test.go`, each checked by removal.
> - #468 (opencoti) on the 3090, 70B q3_K_S, 32k: `--kv-residency-mode auto` beats `head`. Decode 2.45 vs 2.10 tok/s at 1k and 0.24 vs 0.16 at 24k: auto keeps 59 layers on the GPU against head's 48. Mailed as #529.
> - Next: redeploy to eleven2go, rerun simple, then medium and hard once each.

> **2026-09-28 — council Phase 11 opened: caps raised, a repeated call pointed out.**
> - ab-4 simple on eleven2go (kvarn3, `-c 393216`, two live slots): the council was unfixed at 1463 s against plain's 233 s. The researchers could not test, were capped at 384 tokens, and read once each. The diagnosis went untested. The synthesizer repeated one failing edit 15 times, and each of its resumes re-prefilled about 18k tokens.
> - 11.1: reply caps 2048/2048/1024/2048 (planner/researcher/critic/synthesizer) with terse prompts, evidence carried whole to 4000 characters and capped at 16000, notes 600 characters.
> - 11.2: a result that repeats an earlier identical call's result gets a generic note (`TestARepeatedCallWithTheSameResultIsPointedOut`, checked by removal).
> - 11.4 test loop: researchers propose, the synthesizer checks, and a failed check (`VERDICT: RETEST`, with its evidence) goes back to every member of the next cycle, up to 6 cycles, carried in the state (Progress field 7). Loop and prefix checked by removal.
> - Owner's answers applied: the planner re-plans each cycle from the failed checks (`Replan`, state field 8). The user gets the synthesizer's brief status in the same response while the report after the verdict is held back for the council (`holdBack`).
> - 11.5 builder built. On a request's first trip to the council it reads the system prompt, the requests and the tools, then writes per-role instructions, think budgets for the roles the user left unset, and the check bound (0..12), plus a target. The user's council stands. The build is kept across turns (state field 9), and the route decision can `rebuild`.
> - 11.5 front: on a tool turn the synthesizer takes the request first, on the conversation's session. It answers, or calls `council_forward` (the builder runs first when there is no build) or `council_rebuild` (it is told the new setup, then forwards). Only it may route. Turns without tools keep the planner's route decision, with `rebuild`. Checked by removal.
> - Preemption: a broadcast verdict (`kind` confirmed/refuted) interrupts the mates generating, who keep their partial text and read it before going on (at most twice). Ordinary notes still wait for the next call.
> - 11.3: member sessions are no longer closed after a call. The council lives until its client leaves (the owner's ruling), and each member resumes from its own cache (opencoti #526, traced in code). They are closed only when the client drops mid-turn.
> - ab-5 (simple, plain vs council) is running on the solidPC dev server (127.0.0.1:22440, b177, kvarn3, 131072 per slot). It was started before 11.3, so it measures 11.1–11.5 only. 11.5 (the builder) is designed in the plan. No live run yet.

> **2026-09-28 — v0.34.4-xollama.2 published (pre-release) and installed on eleven2go; a council says --kv-unified once.**
> - PR #4 merged as `ac1af15c` (23 checks green); release run 36356726357 published the pre-release, not promoted. Installed on eleven2go with `/SILENT` through the scheduled task: 22 s, exit 0, the think-budget Ollama untouched (same process, files, models and uninstall entry); `xollama.exe` byte-identical to the release, PAYLOAD_ID equal. Docker image dispatched on the tag.
> - ab-4 simple on eleven2go: the council launched `-np 2` (live 2), both researchers in the engine at once (slots 0 and 1 interleaved, no "defer task"). The run was stopped at round 6: the model's `num_ctx 16384` gave the whole council one 16k pool; it is re-run with kvarn3 at the largest context that fits.
> - The council launch carried `--kv-unified` twice (`councilSlotArgs` and `appendSlotArgs`); `appendSlotArgs` now writes it only when the argv lacks it. Guard `TestACouncilWithPoolsSaysKVUnifiedOnce`, checked by removing the fix.

> **2026-09-28 — Engine pin moved to b177 for v0.34.4-xollama.2 (owner's ruling).**
> - `llm/engine/pin.txt` → `opencoti-0.10.5-c7-2609272353001`, rev `7fc93cc8` (#515): fit growth-free (MTP reserve as it runs, no RS for idle slots), the KV-window sizer fix, the bundled dlopen helper, q6_0 mixed pairs served. sha256 of bin, DSO and CUDA 12 payload checked on download.
> - Re-probed: every cache type and ring shape as on b171, except a KVarN key over a plain value with a ring, now accepted by the engine (promoted); xollama keeps refusing it. A/B vs b171: compat 8/8 (was 7/8), llama3 75.5 tok/s, multislot 142, gemma4 identical, 70B overflow 3.85 tok/s (was 3.05).
> - Docs name b177; the q6_0 warning says the pairs now run, slower. Left: the `:dev` image on this pin, verified on the 3090; then merge PR #4.

> **2026-09-27 — xollama was passing opencoti a fit target for every vision model; stopped.**
> - Chris's V100 (32 GB, 18 GB model) ran at 2.9 tok/s with 4 GB of KV on the host and 4.9 GB of VRAM free. The engine's "runtime margin 1909" was ours: upstream's mmproj stopgap (ollama#16996) sets `LLAMA_ARG_FIT_TARGET` = projector + 1 GiB (885 + 1024), and an explicit target switches opencoti's automatic 256 MiB margin off. The `engine-fit` hook (`llm/engine_fit_target.go`) drops the pad on opencoti; stock keeps it (bug-167).
> - Left with opencoti (#510/#511): the MTP booking (2360 MiB held, 1348 used) and RS growth held for slots that are not live (1122 MiB for 3 slots at live 1).

> **2026-09-27 — A silent install tried to uninstall Ollama; fixed before v0.34.4-xollama.2.**
> - Installing v0.34.4-xollama.1 on eleven2go with `/SILENT` (RELEASE.md step 6) ran the think-budget Ollama's uninstaller: the Ollama-found page was built in silent mode and its default is "uninstall". Ollama survived (it was running; files, 210 GB of models, registry entry and app data all intact, app data also backed up). The in-app updater runs the full installer `/SILENT`, so every update on a host with Ollama was exposed. `app/xollama-setup-pages.iss` now never builds or acts on that page silently (bug-166).
> - v0.34.4-xollama.1 had this installer; withdrawn to draft on the owner's word (23:40), so the updater no longer offers it (the tag stays, on `dd739034`). Left: install .2 on eleven2go once it is published.

> **2026-09-27 — Council 10.6: a broadcast channel between parallel members, off by default.**
> - `council.broadcast` (tweak `council-broadcast`): members with a same-role mate get a server-answered `council_post` tool, 200 chars and 4 notes per member per turn, delivered before the mate's next model call and never waited on; the board travels in the sealed state. Tests: `TestANoteReachesTheMateBesideIt` (40/40 under race), `TestTheNotesBoardTravelsInTheState`.
> - Left: A/B it on and off in ab-4 (10.7) on eleven2go; drop it if it costs more turns than it saves.

> **2026-09-27 — Council 10.5: one council across turns; v0.34.4-xollama.2 cut (not promoted).**
> - A follow-up message is feedback to the same council: the planner routes `direct` (done/trivial), `continue` (the synthesizer carries on from the kept plan, findings and critiques) or `council` (fresh). Kept per session in memory and in `council_chat_state` fields 6-8, bound to the conversation it answered. ab-3's nudges restarted the whole council twice; they now continue it.
> - Release PR #4 `release: v0.34.4-xollama.2` (the Docker GPU fix, V100 discovery, council 10.1-10.6, Docker and KV docs). Owner: not promoted.

> **2026-09-27 — KV cache values and combinations documented, from opencoti's matrix (#507).**
> - `docs/xollama/kv-cache.mdx` "Values and the combinations that work": presets (quality / balanced / max context, dense vs sliding-window), every accepted type with its flash-attention and backend limits, the mixing rules (plain+plain and KVarN+KVarN may differ; KVarN+plain is silently promoted, so not offered), the ring rules, and the CUDA `q6_0` mixed-pair crash (opencoti bug-3705, fix not pinned). The stale "ring not in this build" Warning is gone: the pin carries `feature swa-cache-types`.
> - `docs/xollama/docker.mdx` and the Docker Hub page carry the presets and link the section; the Hub page is published.
> - Left: `CacheShapeError` does not yet refuse KVarN+plain or the three crashing `q6_0` pairs; opencoti 0431 (a bundled dlopen helper) lands with the next snapshot, after which the image's own helper is redundant but harmless.

> **2026-09-27 — The Docker image could not use any NVIDIA GPU; fixed. A V100 is kept through discovery.**
> - Chris (V100) got `support for --gpu nvidia was explicitly requested, but it wasn't available`. Reproduced on solidPC's 3090 with `:dev`: the engine (a Cosmopolitan APE) found `ggml-cuda.so` but `dlopen() isn't supported on this platform` -- it builds a libc helper with the system `cc` into `$HOME/.cosmo`, and the image has no compiler. `Dockerfile.xollama` now builds the helper in a gcc stage and ships it in both engine homes; a derived test image loaded qwen3:0.6b 100% on GPU at 195 tok/s. Bare-metal Linux hosts without `cc` are affected too: reported to opencoti.
> - Docker docs: README `## Docker` (ollama-style one-liners, GPU passthrough), `docs/xollama/docker.mdx` (tags, volumes, every operator env var, slots/KV, NVIDIA toolkit setup), `docs/dockerhub/README.md` for the Hub page; `engines.mdx` coverage corrected for the V100 payload.
> - Discovery listed CUDA devices through the main (CUDA 13) engine only, so a V100 on a 570 driver would have been dropped; it now also asks the `engines/cuda_v12` engine and keeps the longer list (`discover/opencoti_cuda12.go`).

> **2026-09-27 — v0.34.4-xollama.1 pre-release published; council Phase 10 started from the ab-3 analysis.**
> - PR #3 merged (`dd739034`); release run 36345410380 published the pre-release (setup 797 MB, update 14 MB, both binaries, sha256sum, payload-id). Docker image for the tag: run 36346010621 (`:dev`, a pre-release). Promotion waits on the eleven2go install check.
> - Upstream's `test-llamacpp-update.yaml` guarded to `ollama/ollama` (`docker-release` hook): it fired on the release PR because `LLAMA_CPP_VERSION` moved, and targets runners the fork lacks.
> - ab-3 finding: the council's members were serialized by the engine (`-np 1`, no slot for the second worker, opencoti #501) and the council restarted on each nudge. Built: a slot per parallel member (`-c` unchanged), one critic by default, shared reads, `VERDICT: CONFIRMED`. Next: the council kept across turns, broadcast, ab-4 on eleven2go. See plans/agentic-council-chat.md Phase 10.

> **2026-09-27 — Engine pin b171 with a CUDA 12 (V100) payload; runtime pins on v0.34.4 (owner: assemble the pre-release and the Docker image).**
> - `llm/engine/pin.txt`: rev `7b836910` (b171 plus `ggml-cuda-cu12-x86_64.so`, sm_70, driver ≥ 570, opencoti #498).
>   - It is read from `#! dso-cuda12` / `#! cuda12-sass`.
>   - `cudaPayload` sends a 7.0 card to `engines/cuda_v12`, where `docker-assemble.sh` stages the payload beside a copy of the engine; a load spanning both payloads goes to llama.cpp.
>   - Guard: `llm/engine/pin_cuda12_test.go`.
>   - Pinned on the owner's say-so despite the b171 gate: its 70B first-load failure is bug-3702, which b145 also has; the fix is in opencoti's next build.
> - Linux runtime pin: `v0.34.4-thinkbudget` (tgz `9769cb4b`, inputs `17ab8578` = ours) with upstream v0.34.4's GPU tarballs.
> - Windows runtime pin: `runtime-windows-amd64-b11081-3023ebe12b5e` (zip `2b5fbffa`, run 36342602471).
> - Docs: device-selection, docker-release, and the engine-pin rule.

> **2026-09-27 — Manifest e314b235 consumed: lint fixed, compat README by sha, inputs digest now equals the fork's.**
> - Merged `up-think-budget`@b463e532 → `9b2b56a6`, the new `up-compat-readme`@ddde8473 → `20ad1fd4` (README conflict taken from its side, as the fork predicted), and the rebuilt `up-response-scope-think-budget`@87d417bf → `4fd3de2c`.
> - Now: `golangci-lint` 0 issues; digest `3023ebe12b5e` (= the fork's); build, vet and test pass.
> - Left: check-compat-origin flags the superseded `15ebdeca`, since the fork's rebuild left it on no ref (asked for an archive ref in the reply); the Linux runtime release waits on the owner (fork item 3).

> **2026-09-27 — b171 (2609271900001) measured and NOT pinned; gemma4-toolcall-in-thinking is fork-only.**
> - b171 carries opencoti's fit rework: an automatic margin, lazy vision, and the rolling-KV window inside the fit. It went through phase2-engine-ab.py as `ollama`; results are in `/srv/ml/xollama-phase2/as-ollama/b171-ab`.
> - Throughput 74.9 tok/s and gemma4 are fine. Two axes regress against b145:
>   - the 70B q3_K_S first load fails allocating its 5.6 GiB KV buffer, because the fit leaves 256 MiB and does not count it;
>   - multislot serializes (142 vs 590 tok/s): `kv-reservation: REFUSED … need 32768`.
> - Sent to opencoti in #491. The pin stays on b145.
> - A Docker image with CUDA 12 (V100) plus CUDA 13 for a tester: upstream's `cuda_v12` is already in the image. The engine's own v12 payload is still compiling at opencoti; asked for its pin shape in #490.
> - CARRIED-PATCHES follows manifest bump `78333d54`: `gemma4-toolcall-in-thinking` is fork-only (#18307 was closed upstream and stays closed).

> **2026-09-27 — Synced to upstream v0.34.4 and the fork's v0.34.4 manifest (branch `sync/upstream-v0.34.4`).**
> - `c43d0a68` merges the v0.34.4 tag. xollama's hooks (council, engine-session fields, api-key, context-window) are kept, upstream's `thinkingparser` / `ThinkingClose` is adopted, and the structured-outputs double request is dropped as upstream did.
> - The fork's 22 patches (manifest `3edc2006`, integration `e74b1daa`) are merged at their shas in order, `a9b28ffe`..`7f560400`. The table is in `docs/protocols/CARRIED-PATCHES.md`.
> - Verified:
>   - no file differs from the fork's tree beyond xollama's own set;
>   - `go build`, `go vet` and `go test ./...` pass;
>   - `check-hooks` passes (26 hooks);
>   - `check-compat-origin` passes after the README fix below.
> - `LLAMA_CPP_VERSION` is b11081. Three new upstream tests set `OLLAMA_HOST` and now set `XOLLAMA_HOST` (default-port rule).
> - Left:
>   - the fork's combined `llama/compat/README.md` (`3f1fcb62`) reaches no patch branch, so ours is the line-boundary copy until the fork puts it on one;
>   - two lint findings in fork code (`server/think_budget_resolution_test.go` bodyclose, `server/routes_generate_test.go` trailing blank line) were sent to the fork;
>   - the Windows runtime pin must be rebuilt (`xollama-runtime.yaml`) and the Linux pin waits for a fork v0.34.4 runtime release, both before a release PR.

> **2026-09-27 — Documented how the context pool is shared; the default stays num_ctx × 1.**
> - Owner's decision: the pool stays `num_ctx × XOLLAMA_PARALLEL` (32k × 1 by default on a 24 GB card). A request that states no window gets the whole pool, so by default requests take turns, as on Ollama.
> - A request that asks for a window (`placement.num_ctx`) gets that window, and up to `XOLLAMA_MAX_PARALLEL` of them share the pool: a 256k pool serves 1 × 256k, 2 × 128k or 4 × 64k from one load.
> - Measured on b171: `-c 131072` allocates the whole pool at load (qwen2.5:1.5b KV 896 → 3584 MiB). opencoti's grow-on-demand pool is planned for c9, stage 8 (mail #497). A prototype that sized the pool for the ceiling was discarded.
> - `docs/xollama/slots.mdx` gains "How the context is shared", and its claim that several conversations run side by side by default is corrected. `docs/xollama/sessions.mdx` links to it.

> **2026-09-27 — Promoted releases are announced on Discord.**
> - New `.github/workflows/discord-announce.yaml`, copied from mann1x/osync's "Announce on Discord" step.
> - It runs on `release: released`, so on the RELEASE.md step-8 promotion and never for a pre-release. It posts the notes as an embed via the `TECH_CORNER_DISCOWH` secret, which the repo already has. `workflow_dispatch -f tag=` re-announces a tag.
> - Validated with actionlint and a local payload dry run on the v0.34.2-xollama.1 notes. Nothing was posted.
> - It takes effect once it reaches `main` with the next release PR.

> **2026-09-27 — Installer setup pages; XOLLAMA_PARALLEL replaces OLLAMA_NUM_PARALLEL on opencoti; server-wide KV types fall back on stock.**
> Owner's requests, for moving pandorum (RTX 5080, an `Ollama think-budget` install) to xOllama.
> - **Installer** (`app/xollama-setup-pages.iss`, included from `app/xollama.iss`, full installer only): the pages are Ollama found (uninstall `/SILENT` after backing up `%LOCALAPPDATA%\Ollama`, or keep it), port (22434 / 11434 greyed out while an Ollama stays / custom), API key (generate / copy / skip) and KV cache (opencoti K and V + legacy type). Silent installs change nothing. Compiled clean with Inno Setup 6.7.1 (CI's version) on pandorum against a stub payload; the real installer comes from the dry-run release build.
> - **`XOLLAMA_PARALLEL`**: on opencoti `liveSlots` takes `slots.live`, else `XOLLAMA_PARALLEL`, else 1, and never reads `OLLAMA_NUM_PARALLEL` (shared with any stock ollama). Stock llama.cpp keeps `OLLAMA_NUM_PARALLEL`. `XOLLAMA_MAX_PARALLEL` unchanged. Guards: `TestXollamaParallelReplacesNumParallelOnOpencoti`, the updated `TestLiveSlotsBindOnlyOnOpencoti` and `TestTheSingleSequenceRuleBindsOnlyStockLlamaCpp`; a mutant restoring the old count fails both slot tests.
> - **KV fallback**: a server-wide `XOLLAMA_K/V_CACHE_TYPE` stock llama.cpp cannot parse no longer fails a load stock serves. `resolveKVCacheTypesOn(..., stock)` keeps the legacy type (`XOLLAMA_KV_CACHE_TYPE` / `OLLAMA_KV_CACHE_TYPE`), and `startLlamaServer` relaunches with `stockKV`. A model's own `kv` is still refused. Guard `TestServerWideOpencotiTypesFallBackOnStock` (mutant killed).
> - pandorum backup before any change: `J:\xollama-migration-backup\20260927` (Ollama app data, `~\.ollama`, machine `OLLAMA_*`, uninstall key).
> - Left: the dry-run installer for the owner to test on pandorum; b160 still unpublished on HF (asked opencoti, #475); the Gemma-4 swallowed-key parser check must come from the fork (#476).

> **2026-09-27 — The opencoti engine logs at ollama's level with --log-memory-plan, not at 5.**
> - xollama forced `--log-verbosity 5` so the memory scrapers would see the buffer-size lines. opencoti shipped `--log-memory-plan` for exactly this (0302, requested 2026-09-18), but xollama never adopted it.
> - Now `logArgs` in `llm/engine/opencoti.go` passes `--log-verbosity 4 --log-memory-plan` when the pin declares `feature log-memory-plan` (the committed pin does), and keeps 5 otherwise.
> - Measured on 2609271108001:
>   - solidPC llama3 8B: every non-zero buffer line, "MiB free" and "offloaded N/M layers" still print; ~3,630 → 32 log lines per request; speed unchanged (~81 tok/s).
>   - eleven2go, Windows build: the same flags, no memory-parsing warning, identical reported VRAM; qwen3:8b 4,265 / 123.0 tok/s, the same as at 5.
> - Also measured: xollama vs ollama on eleven2go. Dense models are within 1% between xollama's defaults and an ollama-like setup (`xollama tweak --slots=off --kv-unified=off`); the gap to ollama is the engine (qwen3:8b −15% prefill, −4% gen). qwen3.6:35b-a3b loses ~18% gen under the defaults (elastic recurrent state on the CPU is the lead); a 7-model re-run is in progress, and opencoti gets the full report.

> **2026-09-27 — v0.34.2-xollama.2 published as a pre-release (PR #2, merge 69f1a658) and verified on eleven2go.**
> - Release run 36319352313: plan, windows, linux, publish all green. Six assets; `sha256sum -c` all OK; payload-id `fcdf0b73…`.
> - eleven2go install (RELEASE.md step 6, scheduled task): `--version` names the tag, PAYLOAD_ID matches, `*:22434`, `/api/xollama` lists 9 features, the tray app names the fork's feed, and ollama on 11434 is untouched.
> - Devices: the RTX 3090 goes to opencoti (CUDA); the Radeon iGPU (Vulkan) goes to llama.cpp.
> - Measured: 700 tokens at 123.0 tok/s on the engine from `lib\ollama\engines`. The omni-council-idle tools turn passes (9 rounds, 87 s).
> - The council models were copied to eleven2go losslessly by recreating them. osync 1.3.1 is lossy against a Windows target: see `/shared/dev/handover/2026-09-27-osync-xollama-copy-issues.md`.
> - Left: promote to latest (step 8) when the owner says so.

> **2026-09-27 — Engine pin moves to opencoti 2609271108001, the first with a Windows CUDA engine; tested live on eleven2go.**
> - Pin: HF rev `ed6430b9`, bin `32287454` (one APE for x86_64, aarch64 and Windows), dso `868ed520` (Linux CUDA) and `ee622711` (Windows CUDA DLL), mail #439. It carries the b145 line: council/PolyKV surface plus opencoti's media runtime and STT.
> - Windows packaging now accepts a dev snapshot's shape (`bin win-x86_64` + `dso win-x86_64`):
>   - `Pin.ArchFor` falls back to it when no `win-x86_64-gpu` bin exists; `cmake/opencoti-engine.cmake` and `xollama-release.yaml` make the same choice.
>   - The engine is staged as `<name>.exe` with `ggml-cuda.dll` in `lib\ollama\engines`, not beside `llama-server.exe`: ggml's loader falls back to `ggml-cuda.dll` in the exe's directory.
> - New pin directive `cuda-sass 86 120`: the payloads carry SASS for sm_86 and sm_120f only, so `Pin.CoversCUDA` routes 7.5/8.0/9.0/10.x to llama.cpp, naming why, instead of letting them run on the CPU.
> - Measured on eleven2go (RTX 3090), with a cross-built xollama installed over the manual install:
>   - qwen3:8b ran on the engine on CUDA0 at 123.7 tok/s.
>   - omnimerge-v4 MTP IQ2_M at 128k made a correct tool call, at 60.7 tok/s with a 131072 window.
>   - `XOLLAMA_ENGINE=llamacpp` stays on stock llama-server on the GPU (109.5 tok/s, no header).
> - Linux A/B on solidPC (`phase2-engine-ab.py` as ollama) against b111:
>   - compat 8/8; llama3 75.4 vs 75.6 tok/s.
>   - multislot 590 vs 461 tok/s; 70B overflow 3.69 vs 1.21 tok/s (single run).
>   - gemma4 tool call and thinking split correct.
>   - No defect row accuses a dev build, so none is retired.
> - CI red on PR #2 since the API key commit, fixed: `app/ui/apikey.go` lacked the `windows || darwin` tag its only caller has (lint `unused` on Linux); `TestLiveAppUpdate` now skips when GitHub rate-limits the unauthenticated feed (403) instead of failing. Release dry run `36317134651` passed (plan, windows, linux, publish).
> - Left: merge PR #2 and the CI pre-release.

> **2026-09-27 — A client's pooled worker is refused fast too; a never-fitting request is a 400, not a "runner stopped".**
> - Cerebriline's first live run of both paths (mail #430) worked on dev 58cd5bf1, b137, omnimerge-v4-mtp:IQ2_M:
>   - The lead pooled (n_pool_shared 941–960) with `X-Context-Window: 31827`.
>   - The swarm owner got 98304, and its workers pooled.
>   - Negotiating asks got fast 429s.
> - Their ask 2 is done: a plain `/api/chat` with `placement.pool_id >= 0` now negotiates as `placement.num_ctx` does. The engine's 429 comes through at once, including the "session allocation full" of a full owner, on which their worker grows its owner. `X-Context-Largest-Admissible` is sent only when the engine names one; `Retry-After` is always sent.
> - Bug found on the way (bug-157): `ErrNeverFits` was never mapped in `Completion` or `Chat`. So the fail-fast of a01f3043 reached clients as "model runner has unexpectedly stopped". It is now a 400 naming the numbers, for requests that do not negotiate.
> - Their ask 1: omnimerge-v4-mtp:IQ2_M went to `session.client_pools: 3`, and the re-test (#433) attached all 3 swarm layers. A lead with a window beside the swarm then exhausted the reservoir (lead 2 + swarm 3 = 5 > 4), so the model is now at 5. `model-settings.mdx` says to count the client pools alive at once: 2 per windowed lead, 3 per swarm tree.
> - The re-test also passed ask 2: a full owner's 429 arrived in 0.4 s with the engine's reason, and their resize-and-grow path works through `/api/engine`.
> - Mutants: 5 more, all killed.

> **2026-09-27 — The engine's granted window reaches the client (`X-Context-Window`), and a window a client negotiates is refused fast.**
> - Cerebriline asked for this in #418. opencoti sets `X-Context-Window: <granted>` on every admitted response when it books windows; before this, xollama dropped it.
>   - The header is now on `/api/chat` and `/api/generate`, streamed or not, and on the OpenAI and Anthropic routes.
>   - A council turn reports its owner's grant.
>   - No header still means no guaranteed window: stock llama.cpp is unchanged.
>   - The value reaches the handler through a collector on the request context. A writer wrapper sets it before the first byte, on the handler's goroutine, so nothing races.
> - A client that states `placement.num_ctx` is negotiating. When the engine cannot book even `num_ctx_min`, the client gets the engine's 429 at once, with `X-Context-Largest-Admissible`, `Retry-After` and the engine's reason, instead of waiting 2 minutes for a 503. Requests that state no window still queue.
> - Feature `context_window_v1`. Registry row `context-window`. Docs in `docs/xollama/sessions.mdx` ("The session's window").
> - Tests in `server/context_window_test.go` and `llm/engine_slots_test.go`. 21 compiling mutants, all killed.
> - Live on solidPC: a throwaway server on :22500, CPU-only, engine b137, tinyllama, run as `ollama`.
>   - The header appeared on every route.
>   - Placement 1536/512 was granted 1536; the continuation held 1536.
>   - Asking 2048/1024 with 512 free gave 429 in 0.0 s, with largest 512 and Retry-After 2.
>   - 2048/256 was granted 512.
> - Limits:
>   - The OpenAI and Anthropic shims do not carry `session_id` or `placement`, so negotiation is `/api/chat` only.
>   - `options.num_ctx` still reloads the model; `placement.num_ctx` is the way to ask for a window.
> - API key tests extended with positive twins and edges: every route with the key, the ollama.com-signed request plus `x-api-key`, Bearer precedence, case sensitivity, throttle forgiveness and expiry, key rotation without a restart, `::1`, remote callers refused even with the key, a client with no key, and the desktop proxy (`app/ui/apikey_test.go`, runs on Windows/macOS CI). 7 more mutants killed; 2 equivalent ones noted.

> **2026-09-27 — A local API key for incoming connections (`xollama tweak server --api-key`).**
> - Upstream accepts `api_key="ollama"` and ignores it. xollama can now require
>   a real key on every route, as `Authorization: Bearer` or `x-api-key`
>   (`401` + `WWW-Authenticate: Bearer realm="xollama"` otherwise). It is a local
>   key only: registry and cloud keep ollama.com signing, and `OLLAMA_API_KEY` is
>   never read for it. Set with `xollama tweak server --api-key=generate|set|remove|status`
>   (the key is never on the command line) or `XOLLAMA_API_KEY` on the server,
>   which wins. The server stores only the SHA-256 (`~/.ollama/xollama-server.json`,
>   0600). The client sends `XOLLAMA_API_KEY` or `~/.ollama/xollama-api-key`.
> - Hardening:
>   - constant-time digest compare;
>   - 10 wrong keys per peer per minute → 429, keyed on the TCP peer (not
>     `X-Forwarded-For`), and a missing key does not count;
>   - the key is stripped before handlers, so the cloud passthrough cannot
>     forward it;
>   - the management route is loopback-only and refuses proxied requests;
>   - `ClientFromEnvironment` alone carries the key, and never follows a
>     cross-host redirect with it;
>   - the host probe never sends it and recognises a keyed xollama by its
>     challenge;
>   - the CLI warns before sending it over plain HTTP to another host;
>   - the server's self-calls use a per-process token;
>   - the desktop proxy injects the user's key;
>   - feature `api_key_v1`.
> - Docs: `docs/xollama/api-key.mdx`, with a Warning that the key is clear text
>   over HTTP and the connection must be TLS-encapsulated; Caddy, nginx and SSH
>   tunnel recipes. Registry row `api-key`.
> - Tests in five files, and 20 compiling mutants, all killed. Live on solidPC
>   with a throwaway server on :22500 as `ollama`: open → generate → 401 without
>   the key / 200 with it (Bearer, x-api-key, LAN) → CLI error naming
>   `XOLLAMA_API_KEY` → plain-HTTP warning → remote and proxied admin 403 →
>   429 after 10 wrong keys from the LAN while loopback kept 200 → remove →
>   open. The key never appeared in the server log.
> - Also today: eleven2go's xollama (0.34.2-xollama.1) is exposed on
>   `192.168.178.161:22434`. It uses `XOLLAMA_HOST=0.0.0.0:22434` as a user
>   variable, because that build predates the Expose fix, plus a firewall rule
>   "xOllama 22434" (Private). The solidPC dev server is on `*:22434`. Both are
>   reached from pandorum.
> - Left: remote council members (`council.<role>.host`) send no key yet; the
>   engine subprocess port is loopback-only and not keyed; mail Cerebriline the
>   header contract.

> **2026-09-27 — Plain models can host a client's own PolyKV pools (`session.client_pools`).**
> - Cerebriline asked (mail #411) to drive PolyKV itself on non-council models.
>   The engine had no seats for that: `--polykv-max-pools` defaults to 0, and
>   xollama passed it only for its own automatic pooling or a council.
>   `session.client_pools` / `XOLLAMA_POLYKV_CLIENT_POOLS` now adds seats for
>   the client, counted in the memory estimate, never in xollama's registry.
> - A native chat placed on a client's pool no longer feeds automatic capture
>   (it already did not on the completion path).
> - Contract answered per question in mail #414.

> **2026-09-27 — A council member's own results fit its window; never-fitting requests fail at once; Expose binds.**
> - A member's own tool results fold to refs past three quarters of the window
>   (characters): older turns first, then its last turn's largest, searched
>   with `council_evidence`. A refusal needing more than the whole window is an
>   immediate error, not 2 minutes of retries.
> - The desktop app's Expose set only `OLLAMA_HOST`, which xollama ignores, so
>   an exposed install stayed on 127.0.0.1 (eleven2go). It now sets
>   `XOLLAMA_HOST=0.0.0.0:<port>`.

> **2026-09-27 — Council tools: narrated calls stopped; long results travel by ref (`council_evidence`).**
> - A researcher's first reply that names a tool without calling it is dropped
>   and asked again once. With the stronger note it never fired: live 10+10,
>   council 10/10, plain 8/10.
> - Results over 1,500 characters travel in findings as a ref plus ten lines;
>   members read ranges or patterns back with the council's own
>   `council_evidence` tool, answered in the server. A member's own older
>   results fold to refs past 12,000 characters.
> - Live, 20 KB log + 8 KB config: every completed run right and byte-exact on
>   both sides (council 9/9, plain 12/12). Three council runs never completed:
>   a member whose own results outgrew the owner's window, waited out for 2
>   minutes, then 503. Open, with the owner.

> **2026-09-27 — A council worker with no pool layer runs inside the owner's window.**
> - Booked on its own session it could wait out admission beside an owner
>   holding the whole cache. It now runs on the owner, inside its window, one
>   at a time, as compaction calls do. The test fails without the branch.

> **2026-09-27 — 9.5 fixed: the council is as reliable with tools as the plain model.**
> - The owner rejected "less reliable" as a finding. A per-member debug trace
>   found three faults of ours, with compaction and thinking both ruled out:
>   - tool results stayed private to the member that called for them, so
>     the synthesizer (the only writer) never saw the file it edited, and
>     critics re-read everything. Findings now carry their evidence;
>   - the charter contradicted the tools ("only their own knowledge");
>   - a resumed member's PolyKV layer was cut after its own tool results.
>     That meant a private pool per round trip, or none: it booked cells
>     beside a full owner, and waited out admission.
> - Live A/B after the fixes, interleaved: council 6/6, 27–38 s; plain 6/6,
>   5–7 s. No critic tool calls, no unpooled members.
> - A procedure fault on the way (bug-144): the dev server was found by
>   name, so a build named `xollama-trace` survived a restart and served one
>   batch on the old binary. That batch was discarded. Find it by its port.

> **2026-09-26 — Phase 9.5: the council uses the client's tools (`council_tools_v1`).**
> - A council turn with tools and `council_chat_state` uses the tools.
>   Researchers and critics call only the tools marked `x_read_only` (or MCP
>   `readOnlyHint`); the synthesizer and a direct answer call any. A member's
>   call ends the response like a model's, under an id naming the member
>   (`r2:call_x`); the results plus the state resume it. Without the state,
>   tools stay a plain chat. Every member carries the tools, and so does the
>   PolyKV root.
> - Live on b137, a client loop over a fake repository: parallel members'
>   calls travel together, each round trip resumes only who waits, and only
>   the synthesizer writes. The first prompts made researchers invent tool
>   results, because the charter still said "their own knowledge". With a
>   tools paragraph and role notes, 4/6 runs were fully right. The plain model
>   got 6/6 in about a sixth of the time.
> - bug-143 (a race on the suspended members) caught by `-race` and fixed.
> - Open for the owner: whether easy tool turns should go to the council at
>   all.

> **2026-09-26 — Hugging Face pulls work again; upstream's Dockerfile listens where it says.**
> - Hugging Face now redirects downloads across its own hosts (hf.co →
>   huggingface.co → its CDN), and the v0.34.2 base followed same-host
>   redirects only, so every `hf.co/…` pull failed with "blocked redirect to a
>   different host". Upstream fixed it in v0.34.4 (6383a0fa, #18533:
>   redirects allowed among hf.co, huggingface.co, ollama.com, ollama.ai and
>   their subdomains); cherry-picked with `-x`, so the next sync meets the
>   same hunks. Live: `hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M`
>   pulls on the dev server. Reported by a user's test suite, which skips that
>   scenario on xollama until this ships.
> - Upstream's `Dockerfile` set `OLLAMA_HOST=0.0.0.0:11434`, which xollama
>   ignores: an image built from it bound the container's loopback at 22434
>   and was unreachable. It now sets `XOLLAMA_HOST=0.0.0.0:22434` and
>   `EXPOSE 22434` like `Dockerfile.xollama` (`docker-release` hook). The
>   published image was already right; it is built from `Dockerfile.xollama`.

> **2026-09-26 — Phase 9.4: a council turn resumes from the client's state (`council_chat_state_v1`).**
> - A client that sends `"council_chat_state": ""` gets a sealed state blob
>   after the route, the plan and each member, and on the done chunk.
>   Sending the newest back resumes the turn: finished members are not
>   asked again. The done blob carries the compaction record, which a
>   restarted server takes back. Protobuf (`protowire`), AES-256-GCM, key at
>   `<models>/council-state.key`, bound to hashes of the history and the last
>   user turn.
> - Live on b137: a turn broken off after 4 states (26.8 s), resent with the
>   newest: only the 2 critics and the synthesizer ran, 28.3 s, first token
>   12.9 s.
> - bug-142 fixed on the way: a client leaving mid-turn left the turn waiting
>   forever. The scheduler drops a cancelled request unanswered, so a member
>   being scheduled never returned, the root was never promoted, and the next
>   turn on that conversation waited on it. The member's read now ends with
>   the context.
> - Next: mail the Cerebriline session; then 9.5, tools on council turns.

> **2026-09-26 — The compaction writer fits its window: compact instruction, measured on b137.**
> - The writer's instruction went from 2.5–3k tokens to about 500: a compact
>   replay prompt (565 → 247), marker (156 → 63), and no requests block (the
>   summary still quotes them). The trigger is capped so the writer fits.
> - A/B on opencoti b137 (0412 in): both arms 6/6 turns and 6/6 recall, the
>   same wall time; mean first token 19.2 s against 24.1 s. At 16k the
>   writer now fits (pooled folds), with no text path and no refused-root fold.
> - opencoti b137's pool-owner-evict fired in the council's pattern: no
>   500s, bug-139 closed.

> **2026-09-26 — Phase 9.3: the council shares one prefix.**
> - Every member sends an empty system message, then the conversation. The
>   charter opens the planner's route and plan requests. The client's system
>   prompt goes only to the synthesizer and a direct answer.
> - A/B on b133, today's layout against 9.3: same routing (6/6 once the route
>   request carries the charter; 4/6 without it) and the same format adherence
>   (9/9). A council turn takes 38.6 s instead of 26.4 s, because researchers
>   and critics are no longer held to the client's answer format and write to
>   their caps.
> - A live check of 9.3: a Cerebriline-style P0 (`[{system:""},{user:…}]`,
>   8 tokens, owned by the session) is now what a council's root forks from.
>   No fallback; 5,249 of 6,033 prompt tokens were cached.
> - bug-141 fixed: roots the engine made unowned (their session had ended) are
>   no longer kept, which leaked pinned pool seats.
> - Left: tools on council turns (9.5) and the sealed state (9.4); the
>   compact-writer A/B waits for opencoti 0412.

> **2026-09-26 — Phase 9.2: client placement (`client_placement_v1`).**
> - `placement {pool_id, num_ctx, num_ctx_min}` on `/api/chat`. A plain turn
>   hands all three to the engine. A council turn forks its conversation root
>   from the named pool when its prompt starts with it, else builds its own.
>   It never releases the client's pool.
> - Live on b133: a plain turn served 285 of 316 prompt tokens from a client
>   pool. A council turn with a pool it does not start with fell back cleanly
>   (engine 400), and the pool was left alone. Findings for clients: the model
>   needs pools on (`XOLLAMA_SESSION_POOL`), the pool must be rendered from the
>   exact text sent, and on a council model it must belong to the conversation's
>   session.
> - `chat_render_v1`: `_debug_render_only` on `/api/chat` is the renderer a
>   client cuts P0 from (Cerebriline, mail #390); on a council model it now
>   renders what the members send instead of convening the council.
> - Left: sharing on council turns needs 9.3's layout (Cerebriline's P0 =
>   render([{system:""},{user:SENTINEL}] + tools) cut at the sentinel, #390);
>   the compact-writer A/B waits for opencoti 0412.

> **2026-09-26 — Phase 9.1: `/api/xollama` lists features; council thinking is tagged per member.**
> - `features: ["council", "council_compaction_v1", "council_tags_v1"]` on
>   `/api/xollama`. Cerebriline gates each piece on these names.
> - Every thinking chunk of a council turn carries `council: {role, index,
>   round}` and holds one member only. Content (the answer) and the done chunk
>   are untagged; the text headings stay for clients that do not read tags.
> - Cerebriline decisions recorded in the plan (mails #379–#384): the state is
>   `council_chat_state`, a sealed protobuf blob for resume only, emitted at
>   every checkpoint; Cerebriline sends `placement.pool_id` on council turns.
> - Left: the compact-writer A/B on b133 (running; compaction changes held
>   back until it is measured), the opencoti 500 on pool create (#385, fix in
>   a new build), then 9.2 client placement.

> **2026-09-26 — Council compaction live on b133: five faults fixed, six turns clean of stalls.**
> - Live runs (six turns, 16k council, ~9.5k tokens of history) found and
>   fixed bug-134…138: the writer is asked only when it fits, the text path
>   goes in pieces on the owner (serially), the kept root is released before
>   a fold from text, a resize's new window is `window_new` (misread since
>   Phase 6), and a review with no room to fork is skipped.
> - Fifth run: every turn 45–77 s, first token 10–34 s; idle folds 2–3 min;
>   no admission waits. Turn 2 went from 153.9 s (first token 100.4 s) to
>   57.7 s (11.2 s).
> - Open: the writer rarely fits at the trigger on a 16k window (every fold
>   came from text), and bug-139 (text calls leave private cells on the
>   owner, so the next root is refused once); asked opencoti (#376).

> **2026-09-26 — Council compaction: fits its window; dev server on b133.**
> - The first live run (b128) failed on turn 2's idle fold: the owner held a
>   6,656 grant under a 9.5k conversation, so the writer was refused three
>   times, and the one-piece text request waited out admission while holding
>   the next turn (bug-134).
> - Fixed: the writer runs only when conversation, instruction and reply fit;
>   the text path goes in pieces that fit; the budget floor scales to
>   window/8 below 32k. New test and 6 mutants.
> - The dev server (22434) runs opencoti b133 `2609261655001` (b128 + 0410
>   ckpt-periodic + 0411 adm-running-priority), copied from bs2 and
>   hash-checked, as the owner asked; `council-b133.sh`.
> - Cerebriline provider design (mails #366/#367): the owner's decisions are
>   recorded — sealed council state in-band, PolyKV driven by the client,
>   tools on council turns with read-only members and a writing synthesizer,
>   the council compacting, and one prompt layout (empty system, tools,
>   conversation; role prompts as user messages; the client's system prompt
>   after the synthesizer's). Answered in #370–#372.

> **2026-09-26 — Council compaction: Phase 8 built, Cerebriline's ported.**
> - `server/council_compaction.go` and `council_compaction_prompts.go`
>   replace Phase 6's compaction: a record per conversation applied every
>   turn, the writer as the owner's next turn on the root, two critics on
>   the replay's halves and a synthesizer on a fork of it, a retrospective,
>   incremental folds, and Cerebriline's fallbacks. It runs on every engine.
> - Settings: `council.context.compaction` (`agentic`/`basic`), `review`,
>   `retrospective` (schema v4, unreleased).
> - Faults found while building it: an unowned tree's per-request grant was
>   taken as the window, and hung the idle fold; the refused-root retry
>   compared message counts; a fold could grow the conversation.
> - Tests under `-race`; 28 compiling mutants, all caught but one
>   equivalent. Live run on b128 pending.

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

`v0.34.4-xollama.2` is the latest pre-release; `v0.34.4-xollama.3` (engine
b208) is being cut. The Agentic Council Chat is in Phase 11 (agentic tool
turns, measured on the manic benchmark on eleven2go). The engine pin is b208
(`2609281805001`), measured 2026-09-28.

## What exists today

- Soft fork of ollama v0.34.4 with full upstream history. The engine seam is
  opencoti-llamafile, pinned to b208 in `llm/engine/pin.txt`.
- Release protocol and hosted CI: `docs/protocols/RELEASE.md` and
  `.github/workflows/xollama-release.yaml`. The Windows CPU runtime is pinned
  in `llama/runtime-pin.txt`, and delta updates are keyed on `payload-id.txt`.
- Model config and `xollama tweak model`, device selection, store ownership,
  the 22434 port with the `XOLLAMA_HOST` namespace, the rebrand, and the
  Windows installer. See the `docs/features/` list in `CLAUDE.md`.
- Guards: `scripts/check-hooks.sh`, the compat-origin check, and gitleaks.

## In flight / waiting on others

- **opencoti:** the pin is on b208 (2026-09-28). Older, from b111: the paired re-run (#336) did not
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

1. Run the Phase 8 compaction live on b128 over four and more turns
   (`council-idle.py`): tokens sent per turn, time to first token, and the
   summary's size by generation. Then assess the council's use of the
   shared prefix on PolyKV (the owner's request).
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
