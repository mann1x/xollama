# CLAUDE.md — xollama

@.wolf/OPENWOLF.md

Soft fork of ollama. `dev` = upstream release v0.40.0 + fork changes (`main` is still
v0.35.1 until the next release PR), carrying the
full upstream history so every `git merge upstream/main` has a real merge-base.
Shared agent notes: @./AGENTS.md · Upstream contribution rules: @./CONTRIBUTING.md

## Standing rules

1. **Never rename the Go module path.** It stays upstream's, exactly as declared
   on the first line of `go.mod`. Renaming costs a ~33% conflict rate per sync
   (measured: 155 of the 474 files upstream touched between the v0.34.0 and
   v0.34.2 tags). The binary is xollama; the import path is upstream's.
2. **Off means off.** With `XOLLAMA_ENGINE=llamacpp` and no xollama flags,
   behaviour must be byte-identical to upstream. That is what keeps an A/B
   against vanilla honest.
3. **Keep the record current.** Every session that changes code, releases,
   pins or plans adds a dated entry to `STATE_SUMMARY.md` and, when a plan's
   status or phase moves, updates its row in `plans/MASTER_PLAN.md` and the
   plan itself, in the same commit as the change.

## Read before touching the upstream tree

- `STATE_SUMMARY.md` — where the work stands, newest entry first.
- `plans/MASTER_PLAN.md` — the index of every plan in `plans/`, with status and
  phase.
- `docs/protocols/UPSTREAM-SYNC.md` — every change is either an additive file or
  a marked surgical hook, and hooks go in the Registry in the same commit.
  Before any sync, `git worktree list --porcelain | awk '/^branch /{print $2}'
  | sort | uniq -d` must print nothing: a branch checked out in two worktrees
  shows its own commits as staged reversals in the stale one, and a commit
  there undoes them silently (`think-budget`, 2026-10-03; one branch, one
  worktree in `docs/protocols/FORK-SYNC.md`).
- `docs/protocols/CARRIED-PATCHES.md` — the open upstream PRs this fork carries.
  Each is its own `--no-ff` merge, retired the day upstream takes it.
- `docs/protocols/FORK-SYNC.md` — how patches reach this repo from
  `mann1x/ollama`. Consume by the sha in the fork's `PATCHES.json`, in
  `patches[]` order, each as its own `--no-ff` merge. **Never hand-copy a hunk
  from `think-budget`** — that is how `model/parsers/gemma4.go` ended up three
  different things across two trees. xollama never files upstream PRs, nor asks
  the fork to — that is the repository owner's call; `fork-only` entries stay so.
  The fork **supplies** llama.cpp: `LLAMA_CPP_VERSION`, `llama/server` and
  `llama/compat` (README included) change in the fork first, never here —
  enforced by `.github/workflows/compat-origin.yaml` running
  `scripts/check-compat-origin.sh origin/main..HEAD` (needs `git fetch fork`
  and `git fetch upstream` locally); `llama/clef` is upstream's and outside
  that set.
- `docs/protocols/RELEASE.md` — how a release is cut: a PR `dev`→`main` titled
  `release: v<upstream>-rc.<k>.xollama` (a candidate, never promoted), then
  `release: v<upstream>-xollama` (the last candidate's tree, short check), and
  `release: v<upstream>-xollama.<n>` for a re-release; the version always
  follows upstream's, plain semver orders the three, and the PR body is the notes; hosted CI
  (`.github/workflows/xollama-release.yaml`) builds and publishes a pre-release
  from pinned artifacts only — the Windows CPU runtime comes from
  `llama/runtime-pin.txt`, built once by `.github/workflows/xollama-runtime.yaml`
  and moved in the same PR that changes `LLAMA_CPP_VERSION`, `llama/server` or
  `llama/compat`; the Go binaries build with the newest patch release on the
  `go` line of `go.mod`, never `GOTOOLCHAIN: auto`; promote after the eleven2go
  install check — promotion (the `released` event, never a pre-release) posts
  the notes to Discord via `.github/workflows/discord-announce.yaml`.
  Never push a `v*` tag, never copy exes onto a host as an "update".
- `docs/protocols/VALIDATION.md` — the gates of a release (G1 to G12): host,
  steps, expected result and the last measured baseline of each; the first
  release passes all, a later one the delta its changes touch, and a gate that
  is run updates its row. Scripts as run: `scripts/gates/`.
- `docs/features/engine-opencoti-llamafile.md` · `docs/features/rebrand.md` ·
  `docs/features/store-ownership.md` · `docs/features/windows-installer.md` ·
  `docs/features/model-config.md` · `docs/features/modelfile-roundtrip.md` ·
  `docs/features/docker-release.md` · `docs/features/device-selection.md` ·
  `docs/features/council.md` ·
  `docs/features/system-settings.md`
- `docs/evaluations/phase0-engine-compat.md` — measured engine-compat baseline.

Remotes: `origin` = mann1x/xollama · `upstream` = ollama/ollama ·
`fork` = mann1x/ollama (where PRs to upstream are staged).

## Commands

Go-only iteration against an existing native payload:

```sh
go build -o xollama .
go run . serve
go test ./server/... ./model/... ./thinking/... ./llm/...
golangci-lint run
```

Full native build (prereqs and GPU notes in `docs/development.md`):

```sh
cmake -B build .
cmake --build build --parallel 8
cmake -B build . -DOLLAMA_LLAMA_BACKENDS="cuda_v13;vulkan"
cmake -B build . -DOLLAMA_MLX_BACKENDS=cuda_v13
```

End-to-end suites live in `integration/` and are build-tagged, so they are not
part of the default sweep:

```sh
go test -tags integration ./integration/...
```

Secret scan — `.githooks/pre-commit` runs it locally, `.github/workflows/gitleaks.yml` in CI:

```sh
gitleaks protect --staged --config .gitleaks.toml
```

## Architecture

**Entry**: `main.go` → `cmd/cmd.go` (Cobra) · **HTTP**: `server/routes.go` (Gin),
scheduling `server/sched.go`, model IO `server/images.go` `server/create.go`
`server/prompt.go`.
**Runners**: GGUF as a `llama-server` subprocess via `llm/server.go` +
`llm/llama_server.go`, built from `llama/server/CMakePresets.json`; MLX via
`mlxrunner/` (`runner.go`, `pipeline.go`, `prefix_cache.go`, `cache/`, `model/`,
`tokenizer/`, `xgrammar/`). `llm/engine_args.go` appends the operator's
`XOLLAMA_ENGINE_ARGS` last on the engine command line. On opencoti,
`llm/engine_fit_target.go` drops upstream's vision-projector padding from
`LLAMA_ARG_FIT_TARGET` (`engine-fit` hook) — see `.claude/rules/engine-fit.md`.
A `/health` dial that fails against a live opencoti is tried again with backoff
(`retryHealth` in `llm/engine_health_retry.go`, `engine-health-retry` hook in
`getServerStatusRetry`); stock llama.cpp keeps upstream's single failure.
On Windows an opencoti engine ends with its server: `bindEngineLifetime`
(`llm/engine_lifetime.go`, `engine-lifetime` hook) puts it in a kill-on-close job
object (`engine.BindLifetime`, `llm/engine/lifetime_windows.go`).
`llm/drafter.go` holds
the drafter rules (built-in vs attached head, `--spec-type`) as pure functions
shared by the launch and `show` (`server/drafter_show.go`), so the two cannot drift.
The ollama#4165 single-sequence deny-list binds only on stock llama.cpp:
`server/sched.go` asks `llm.WouldUseOpencoti` before clamping, and
`servedSequences` (`llm/engine_estimate.go`) drops a launch that lands on stock
after all back to one — see `.claude/rules/dynamic-slots.md`.
`/api/engine` (`server/routes_engine.go` + `llm/engine_introspect.go`, the
`engine-introspect` hook) proxies the engine's own management routes — GET,
POST and DELETE, by name from `engineRoutes`, inference excluded — see
`.claude/rules/engine-introspect.md`.
opencoti's granted window (`X-Context-Window`) reaches the client through the
`llm.ContextWindow` collector (`llm/engine_context_window.go`) and
`server/context_window.go`; stock llama.cpp stays header-free — see
`.claude/rules/context-window.md`.
**Decision scoring** (upstream v0.35.1, MLX scoring since v0.40.0): `/v1/systemone` (`SystemOneHandler` in
`server/routes.go`) compiles typed questions with `decision/` (`systemone.go`,
`clef.go`, `types.go`) and scores them through `llm.Scorer` (`llm/score.go`,
`llm/llama_server_score.go`); a model whose GGUF carries `decision.type` gets the
`decision` capability (`types/model/capability.go`), and the CLEF head in
`llama/clef/` is compiled into `llama-server` by `llama/server/CMakeLists.txt` —
see `docs/capabilities/decision.mdx`. A Clef model (`decision.type = clef`) is served by stock
`llama-server` while the pinned opencoti declares no `clef_score_v1` (`clefEngine`,
`server/decision_engine.go`; `engine.HasClefHead`); `nimble` stays on opencoti.
**Prompting**: `model/renderers/` (per-model `Render`) ↔ `model/parsers/`
(streaming output), plus `template/`, `thinking/`, `harmony/`; a model's named
thinking efforts are `types/model/thinking.go`.
**API shims**: `api/types.go`, `openai/openai.go`, `anthropic/anthropic.go`,
`middleware/` (`middleware/thinking.go` maps OpenAI/Anthropic efforts). **Config**: `envconfig/config.go` holds every `OLLAMA_*` var;
the listen address is `envconfig.DefaultPort` (22434), read from `XOLLAMA_HOST`
only — see `.claude/rules/default-port.md`. Where the CLI *connects* with no host
set is `api/xollama_host.go` (`ResolveHost`), run once from `cmd/xollama_host.go`;
it tells the fork apart via `/api/xollama` (`api/xollama_identity.go`,
`server/identity.go`), then the fork's name in `/api/version`, and refuses a
stock ollama found on the 11434 fallback rather than driving it.
**Local API key** (`api-key` hook, off unless a key is configured):
`XOLLAMA_API_KEY` or the key file (`envconfig/xollama_apikey.go`) is checked by
`apiKeyMiddleware` in `server/xollama_apikey.go`; the loopback-only admin route
`/api/xollama/api-key` is `server/xollama_apikey_admin.go`; the CLI sends it via
`api/xollama_apikey_client.go` and sets it with `xollama tweak server --api-key`
(`cmd/tweak/server.go`); the desktop UI via `app/ui/apikey.go` — see
`.claude/rules/api-key.md` and `docs/xollama/api-key.mdx`.
**Server settings** (`system-settings` hook, plan `plans/system-settings.md`): the
server's `~/.ollama/xollama-settings.json` overrides the environment — `Var`
reads its `envs` first (`envconfig/xollama_settings.go`), engines get it via
`envconfig.Environ()`; written only by `/api/xollama/settings`
(`server/xollama_settings.go`, loopback); CLI `xollama tweak envs` and
`xollama tweak show server|envs|model` (`cmd/tweak/envs.go`, `cmd/tweak/show.go`).
Its `defaults` section is the server's default for every `tweak model` setting
in `xollama.DefaultSections()`, merged under the model's own by
`WithDefaults` (`types/xollama/defaults.go`) in `launchXollama`; set with
`xollama tweak server` (`serverFields`, `cmd/tweak/server.go`).
Its `gpu` section (`types/xollama/gpu.go`) is `xollama tweak server gpu`
(`cmd/tweak/gpu.go`): allowed GPUs, priority (fill order), backend per GPU,
forced link (`OPENCOTI_LINK_GBPS=<pci>=<GB/s>,...`, one entry per forced GPU), split
auto/spread/single and split mode, applied in `server/xollama_gpu.go` via
`server/sched.go` hooks and `llm.DeviceEnvs`. Its menu shows each GPU's link as
the engine measures it: `/api/xollama/link-probe` (`api/xollama_linkprobe.go`,
`LinkProbeHandler` in `server/xollama_linkprobe.go`, loopback only) runs
`engine.LinkProbeCommand` (`llm/engine/linkprobe.go`) once per backend, never
on one with a model generating.
**Discovery** `discover/` · **Transfers** `transfer/` · **GGUF** `fs/gguf/`,
`fs/safetensors/` · **Types** `types/model/`.
**CLI support packages**: Modelfile parsing in `parser/` (`parser.go`,
`expandpath_test.go`), terminal progress bars and spinners in `progress/`
(`bar.go`, `spinner.go`, `progress.go`), human-readable sizes and durations in
`format/` (`bytes.go`, `time.go`), line editing in `readline/`.
**Registry and on-disk store**: `auth/auth.go` signs registry requests with the
local SSH keypair; `manifest/` holds the manifest/blob model (`manifest.go`,
`layer.go`, `paths.go`) that `server/images.go` reads and writes.
**Logging**: `logutil/logutil.go` — the shared `slog` handler and `LevelTrace`.
Every GGUF load is named before the engine starts (`load-log` hook in
`server/sched.go`, `server/load_log.go`): `loading model`, `model file` and
`model placement`, from what the estimator already parsed, on every engine.
**Internal-only packages** under `internal/`: `internal/cloud` (cloud host
policy), `internal/modelref` (model reference parsing), `internal/onboarding`
(first-run app state; on Windows it, the app/server logs, `ollama.pid` and the
settings database live under `%LOCALAPPDATA%\xOllama`, on macOS under `~/Library/Application Support/xOllama`, not upstream's `Ollama`,
and the tray's window class is `xOllamaClass` —
`app-state` hook, also in `app/store/store.go`, `app/wintray/menus.go`,
`app/wintray/tray.go`, `app/cmd/app/app_windows.go`, `app/server/server_windows.go` and `app/server/server_unix.go`),
`internal/orderedmap` (insertion-ordered maps behind the
tool schemas), `internal/fsowner` (hands files a root run creates to the owner
of the model store; `create.go` wrappers, `preflight.go` warning — see
`.claude/rules/store-ownership.md`), plus `internal/testutil` and `internal/proxy`.
**Integration tests**: `integration/` — build-tagged end-to-end suites
(`basic_test.go`, `tools_test.go`, `vision_test.go`, `concurrency_test.go`) with
fixtures in `integration/testdata/`; they need a running server and pulled models.
**Launchers**: `cmd/launch/` (`claude.go`, `opencode.go`, `codex_app_profile.go`…)
with the Bubble Tea menu in `cmd/tui/tui.go`.
**Model settings**: `xollama tweak model` lives in `cmd/tweak/` (`tweak.go`,
`fields.go`, `prompt.go`, `reconcile.go`, `devices.go`, `council.go`), registered
from `cmd/cmd.go` under the `model-config` hook. It reads the model's config
layer through `/api/show` (`api.ShowResponse.Xollama`), validates against
`types/xollama/config.go` (schema v7, engine policies in `engine_policy.go`; the `council` block is
`types/xollama/council.go`; the `media` block — image/stt/tts/video engines, components by digest as
`application/vnd.xollama.media` layers written by `create/xollama_media.go`, template-owned `defaults`
and `fixed` — is `types/xollama/media.go`, its tweak rows and uploads `cmd/tweak/media.go`; it is served by a media-only opencoti
process, `llm/engine_media.go`, scheduled as `media:<digest>` from `server/media.go` (`media` hook; every component goes to the engine as its blob path, each extra voice its own layer passed as `--tts-voice NAME=<blob>`, and an engine lacking a needed `/health` feature is refused at load, `MediaFeatures`); `hf.co` components are resolved by
`internal/mediahub` and fetched by `/api/xollama/media/pull`, `server/media_pull.go`; catalog and discovery are `xollama media`,
`cmd/tweak/mediacmd.go`, with a local mirror (`media fetch`, `create --dir`, `internal/mediahub/mirror.go`); the OpenAI media routes,
per-engine queue and template fill-in are `server/media_routes.go`, an edit without a size keeps its source's (`editSize`, `server/media_edit_size.go`), a speech model's voices `server/media_voices.go`, the operator's default model per kind `server/media_defaults.go` (`XOLLAMA_MEDIA_DEFAULTS`), video jobs (`/v1/videos`, the engine held per job) `server/media_video.go`, see `docs/xollama/media.mdx` and `.claude/rules/media.md`), and replaces only that layer — see
`docs/xollama/tweak.mdx`. `xollama show` lists the stated settings in an
`xOllama` table via `tweak.SettingRows` (same hook, in `showInfo`); unstated ones
are omitted. A council's settings stay out of the launch: `llamaServerConfigForModel`
in `server/routes.go` passes `LaunchConfig()`. The one thing a council adds to the
launch is its PolyKV pool seats (`CouncilPools`, from `councilPoolSeats`), and
only when `polykv` is not `off`. A council tag and its `FROM` base swap the
runner anyway, as any two tags do: upstream's `ManifestDigest` is in the launch
config. A council turn is served by `internal/council/` (the
errgroup runner: route-only decision, researchers and critics in parallel,
synthesizer; `internal/council/build.go`: the builder, on its own `~builder` session (on `council.builder`'s model and host, else the planner's) and reading only the user's messages,
shapes each role's instructions, think budget and check cycles for the work;
`internal/council/tasks.go`: the planner's task list, carried in every plan's JSON;
`internal/council/stuck.go`: tells the next cycle whether a failed check's output moved;
`internal/council/cut.go`: a writer's cap on a tool turn has room for an edit, and a reply cut before its call is asked again;
`internal/council/quote.go`: a change whose quote the member's last read does not bear out is answered in place with the read's text;
`internal/council/directive.go`: the owner's and a client's instructions by slot, and a harness's directive (`ChatRequest.Council`: mode, stated build, evidence, check tool; `council_directive_v1`);
`internal/council/checkcall.go`: a directive's `check_call` is the turn's check, made for a synthesizer that ends with a change unchecked (`council_check_call_v1`), and a synthesizer's cycle lasts while that check's output moves (`cycleSteps`);
`internal/council/loops.go`: Cerebriline's loop guards, a no-op edit refused in place
and a write repeated with the same result struck until the member's steps end) and `server/council.go` (members as in-process chat turns, each
on its own engine session; on opencoti with PolyKV, `server/council_polykv.go`
builds the turn's pool tree — the planner attached to the conversation's root
pool, kept between turns — and `llm/engine_council.go` is its client;
`llm/engine_council_slots.go` launches a council with the engine's parallel ceiling live
(never below its local width; cloud members are counted apart, `council.cloud_parallel`,
`server/council_cloud.go`)
(its `--kv-unified` is never repeated by `appendSlotArgs` in `llm/engine_launch.go`);
`server/council_layers_kept.go` keeps a turn's stage layers across its tool round trips;
a worker's layer ends at its own instruction (`council.OwnPart`), and `server/council_layer_log.go` logs each layer's text;
a member refused for a full owner is answered at once (`llm.ErrOwnerFull`), and the turn compacts and resumes (`server/council_owner_full.go`);
`server/council_retry.go` asks a failed or stalled member call again (`councilRetries`, backoff), never a 4xx refusal or a full owner;
`server/council_room.go` has the owner give back cells a member booked beside it (the builder, a reviewer) needs; a role with
`council.<role>.host` is sent to that server by `server/council_remote.go`, only
when `XOLLAMA_COUNCIL_HOSTS` allows it; `server/council_compaction.go` folds the
conversation before a turn and after its answer, Cerebriline's agentic compaction
ported, prompts in `server/council_compaction_prompts.go`; `server/council_state.go`
seals the `council_chat_state` resume point a client sends back; `server/council_continue.go`
keeps a turn's deliberation so the next message can `continue` the same council,
`internal/council/continue.go`; `server/council_usage.go` adds up what each role
spent, reported on the done chunk as `ChatResponse.CouncilUsage`, `council_usage_v1`; the done chunk's
`prompt_eval_count` is the whole conversation the client sent, measured (`sentTokens`), else the front's or planner's prompt (`carriesConversation`), never the members' sum), reached from one
`councilServes` line in
`ChatHandler` (`council` hook); a `format` bypasses it, and tools without
`council_chat_state` are a generic harness's, the resume point held server side
(`server/council_held.go`; read-only and check inferred, `internal/council/generic.go`; a new task on a derived session starts clean, `server/council_fresh.go`) (`internal/council/tools.go`: read-only tools for
researchers and critics, writes by the synthesizer; `internal/council/evidence.go`:
the server-answered `council_evidence` tool that reads a large result back by ref;
`internal/council/reads.go`: shared reads, a repeated read-only call answered in place;
`internal/council/broadcast.go`: the opt-in `council_post` notes between same-role members;
`internal/council/review.go`: the synthesizer's `council_review` checks, queued for critics
in the background, a per-conversation desk in `server/council_review.go`; a refutation stands
until a change answers it (`refutedUnchanged`, DONE refused up to `maxStanding`);
`internal/council/report.go`: a member's result as a typed tool call (`council_report`, `council_verdict`, `council_done`, `council_retest`), added by `WithReports`;
`internal/council/replay.go`: a member's last step replays its reasoning, and capped reasoning is condensed to a note by the `Condenser`;
`server/council_tools.go`), and a one-shot
`xollama run <council> "…"` goes through chat (`cmd/council_run.go`) — see
`.claude/rules/model-config.md` and `.claude/rules/council.md`.
**Device selection**: a model's device pin (`types/xollama/devices.go`) is
applied by `selectModelDevices` in `server/device_select.go`, hooked from
`server/sched.go` — a missing pinned device refuses the load, never falls back,
and unpinned models are kept off an integrated Vulkan GPU when a discrete GPU
exists. `/api/xollama/devices` (`api/xollama_devices.go`, `XollamaDevicesHandler`
in `server/identity.go`) feeds the `tweak` device menu (`cmd/tweak/devices.go`)
with the server's view. Where opencoti serves a backend, discovery takes the
engine's own device list (`discover/opencoti.go`, `llm/engine/enumerate.go`;
CUDA is also asked of the CUDA 12 engine, `discover/opencoti_cuda12.go`),
and refreshes free memory from it before a load (`discover/refresh_opencoti.go`); a listing that fails or a refresh that finds nothing is said at Warn with the engine's last output (`outputTail`), never Debug.
A model whose own `kv.k` / `kv.v` only opencoti runs (`llm.NeedsOpencoti`,
`llm/engine_placement.go`) is placed only on the GPUs opencoti serves:
`opencotiPlacement` in `server/placement_opencoti.go` (`opencoti-placement`
hook in `server/sched.go`) — see `docs/features/device-selection.md` and
`.claude/rules/engine-kv-cache.md`.
**Desktop UI**: `app/ui/app/src/routes/` (React 19 + TanStack Router + Vite),
sibling to the `app` workspace (`vite.config.ts`, `vitest.config.ts`). A council
model gets a badge and a Deliberation toggle (`hooks/useCouncil.ts`,
`components/CouncilBadge.tsx`, `components/DeliberationButton.tsx`);
`councilThink` in `app/ui/council.go` (the `council` hook in `app/ui/ui.go`)
keeps its explicit `think:false`. The app names itself xOllama (`app-brand`
hook): `AppName` in `app/wintray/brand_xollama.go` for the tray, notifications
and window title, and the build-time Vite plugin `xollamaBrand()`
(`app/ui/app/xollama-brand.ts`) for the UI's strings; its icons in `app/assets/`
are made by `scripts/xollama-icon.py` — see
`.claude/rules/app-brand.md`.
**Desktop updates**: `app/updater/fork.go` reads this fork's GitHub releases
(`XOLLAMA_UPDATE_FEED`, `XOLLAMA_UPDATE_PRERELEASE`) instead of `ollama.com`,
hooked from `app/updater/updater.go` / `app/updater/updater_windows.go`; on macOS the staged update, the backup and the upgrade marker live in
`~/Library/Caches/xOllama`, never upstream's `Caches/ollama` (`app/updater/fork_stage.go`, which also removes our own leftovers from the old folder); its logs are in `~/Library/Logs/xOllama`, not `~/.ollama/logs`;
`app/updater/fork_payload_windows.go` picks the small `xOllamaUpdate.exe`
(no `lib\ollama`) over the full `xOllamaSetup.exe` when the installed
`lib\ollama\PAYLOAD_ID` matches the release's `payload-id.txt` (`payloadId` in
`scripts/build_windows.ps1`). The installer is `app/xollama.iss` (setup pages
for Ollama-found, port, API key and KV cache in `app/xollama-setup-pages.iss`,
full installer only); before an install (`PrepareToInstall`) or uninstall
(`[UninstallRun]`) it runs `app/xollama-stop.ps1`, which stops only xOllama's
own processes and engines, never a stock Ollama's `llama-server.exe`; it always
registers `xollama://` and registers `ollama://` only when nobody already owns
it (`OllamaSchemeUnclaimed`). macOS declares both schemes and
`app/cmd/app/app_darwin.m` handles both, because ollama.com picks the sign-in
redirect scheme; `app/cmd/app/app.go` accepts either — see
`docs/features/windows-installer.md`.
**Container image**: `.github/workflows/docker-release.yaml` ASSEMBLES the image
on `ubuntu-latest` from pinned artifacts — nothing native is compiled:
`scripts/docker-assemble.sh` stages the fork's CPU runtime and upstream's GPU
tarballs (`llama/runtime-pin-linux.txt`, sha256 + a README-excluded inputs
digest), the engine (`llm/engine/pin/`, with its CUDA 13, CUDA 12 and Vulkan
libraries and media sidecars beside it) and a Go-only `xollama`, and
`Dockerfile.xollama` sets `XOLLAMA_HOST=0.0.0.0:22434` and prebuilds the engine's
dlopen helper from `scripts/cosmo-dlopen-helper.c` (cosmo's own compile command,
both files given an old mtime), since the runtime image has
no compiler (upstream's `Dockerfile`
now sets it too and `EXPOSE 22434`, `docker-release` hook, but the published
image still comes from `Dockerfile.xollama`). Publishes to Docker Hub and GHCR,
per architecture (`ARCH=amd64|arm64`, `:<version>-<arch>` joined by the `manifest`
job); the arm64 CPU runtime is the fork's too, pinned in `llama/runtime-pin-linux-arm64.txt`
and moved with the amd64 pin; see `docs/features/docker-release.md`. The channel is the GitHub
pre-release flag of the tag's release: a full release moves `:latest`, a
pre-release or any branch run (`gh workflow run docker-release.yaml --ref dev`)
moves `:dev`, never `:latest`.
Upstream's `.github/workflows/latest.yaml` job is guarded to `ollama/ollama`
(`docker-release` hook), since `docker-release.yaml` already pushes `:latest`;
`release.yaml`'s `darwin-build` job is guarded off on the fork and dropped from
the `release` job's `needs`.
Pinned natives: `LLAMA_CPP_VERSION`, `MLX_VERSION`, `MLX_C_VERSION`, orchestrated by `CMakeLists.txt` / `CMakePresets.json`; the opencoti engine artifact is pinned in `llm/engine/pin/`, opencoti's pin format 2: our own `index.txt` (channel, tag, one line per
component with its pin's sha256) and the component pins it names, vendored byte-identical (`engine`, `cuda`, `cuda12`, `vulkan`, `macos`, `media`: `abi`, `engine-min`, `sass`, `feature` and `file <platform> <kind> <path> <sha256> <bytes>` rows). Every file is
staged beside the engine under its published name: the GPU libraries (CUDA 13 and the legacy CUDA 12 one side by side; a load on cards only CUDA 12 serves starts the engine with `OPENCOTI_CUDA_LEGACY=1`, `engine.LegacyCUDA`), the media libraries oc-codec,
oc-audiocpp and oc-espeak with their licence texts, and on Apple silicon (`macos-aarch64`) the loader the engine is started through (`ape`) and the Metal library, staged and signed by `scripts/build_darwin.sh` with `app/darwin/engine-loader.entitlements`
(`macos-engine` hook). It is read by both `llm/engine/pin.go` (`LoadPin`) and `cmake/opencoti-fetch.cmake`, which refuse a vendored pin that is not the one the index names and a component whose `abi` or `engine-min` the engine does not meet. Moving that pin
retires only the rows in `llm/engine_defects.go` the new bytes are *measured* to fix — a changelog is not a measurement; the measurement is `scripts/phase2-engine-ab.py`, run as the `ollama` user (`.claude/rules/solidpc-testing.md`).

## Tooling and conventions

- MCP servers available: `pgvector` (persistent memory + KG — store fork
  decisions there), `lsp` (gopls/clangd, wired through `cclsp.json`),
  `code-graph`, `gitnexus`, `context7`, `github-mcp`, `filesystem`, `searxng`.
- Compile-aware LSP engine config: `.claude-hooks/lsp-engine.toml`.
- Commit subjects follow `CONTRIBUTING.md`: `<package>: <short description>`,
  lowercase, a continuation of "This changes Ollama to…".
- Docs are Mintlify `.mdx` under `docs/`, indexed by `docs/docs.json`.
  `docs/README.md` and `docs/docker.mdx` stay upstream's except for one
  xollama block at the top (`docs-entry` hook).

## Before Committing

**IMPORTANT:** Before every git commit, you MUST ensure Caliber syncs agent configs with the latest code changes.

First, check if the pre-commit hook is already installed:
```bash
grep -q "caliber" .git/hooks/pre-commit 2>/dev/null && echo "hook-active" || echo "no-hook"
```

- If **hook-active**: the hook handles sync automatically — just commit normally. Tell the user: "Caliber will sync your agent configs automatically via the pre-commit hook."
- If **no-hook**: run Caliber manually before committing:
  1. Tell the user: "Caliber: Syncing agent configs with your latest changes..."
  2. Run: `caliber refresh && git add CALIBER_LEARNINGS.md CLAUDE.md .claude/ 2>/dev/null`
  3. After it completes, briefly tell the user what Caliber updated. Then proceed with the commit.

**Valid `caliber refresh` options:** `--quiet` (suppress output) and `--dry-run` (preview without writing). Do not pass any other flags — options like `--auto-approve`, `--debug`, or `--force` do not exist and will cause errors.

**`caliber config`** takes no flags — it runs an interactive provider setup. Do not pass `--provider`, `--api-key`, or `--endpoint`.

If `caliber` is not found, tell the user: "This project uses Caliber for agent config sync. Run /setup-caliber to get set up."
## Session Learnings

Read `CALIBER_LEARNINGS.md` for patterns and anti-patterns learned from previous sessions.
These are auto-extracted from real tool usage — treat them as project-specific rules.
## Model Configuration

Recommended default: `claude-sonnet-4-6` with high effort (stronger reasoning; higher cost and latency than smaller models).
Smaller/faster models trade quality for speed and cost — pick what fits the task.
Pin your choice (`/model` in Claude Code, or `CALIBER_MODEL` when using Caliber with an API provider) so upstream default changes do not silently change behavior.

## Context Sync

This project uses [Caliber](https://github.com/caliber-ai-org/ai-setup) to keep AI agent configs in sync across Claude Code, Cursor, Copilot, and Codex.
Configs update automatically before each commit via `caliber refresh`.
If the pre-commit hook is not set up, run `/setup-caliber` to configure everything automatically.
