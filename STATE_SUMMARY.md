# xollama — State Summary

Newest entry first. Each entry is dated and says what changed (commit shas,
release tags, measurements) and what is left. The fixed sections below the
entries are rewritten in place so they always describe *now*. Plans are
indexed in [`plans/MASTER_PLAN.md`](plans/MASTER_PLAN.md).

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
finished Phases 0 (on b65), 1 (in-house `errgroup` runner) and 2 (schema v4
and `tweak`). Phase 3, the runner on the llama.cpp path, is next. Phase 0
runs again on b111 once it is on the HF dev repo.

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

- **opencoti:** b111 is on HF; the engine pin moves once it is measured.
  Also waiting on the spent-response port,
  queued behind row K, and the E2B/E4B gate, which needs an HF repo@rev.
- **mann1x/ollama (fork):** the static `llama/compat/README.md` commit. When
  it lands, xollama takes it by sha.

## Known gaps

- Docker image assembled and validated locally, but not published yet: no
  `:dev` run has happened ([`plans/docker-image.md`](plans/docker-image.md)).
  amd64 only.
- Upstream's `Dockerfile` still sets `OLLAMA_HOST`/`EXPOSE 11434`, which
  xollama ignores; only `Dockerfile.xollama` makes a reachable image.
- Open fork-sync items: `docs/protocols/FORK-SYNC.md` § "Open items".
- Carried upstream PRs: `docs/protocols/CARRIED-PATCHES.md`.
- UI lockfile Dependabot alerts are inherited from upstream and not addressed.

## Immediate next steps (in order)

1. Council Chat Phase 3:
   - `server/council.go` and the `ChatHandler` hook;
   - streaming as thinking plus content, and the OpenAI/Anthropic shims;
   - member windows stated per call;
   - decide on qwen35's single-sequence rule on opencoti, by measurement.
2. When b111 is on HF: rerun `plans/council-eval/probe/` and
   `cmd/council-run` on it, then move the pin on a measurement.
3. Push `dev` and run
   `gh workflow run docker-release.yaml --ref dev -f push=true -f channel=dev`
   for the first `:dev` image. The first GHCR push leaves the package private,
   so make it public (`docs/features/docker-release.md`, one-time setup).

## Open decisions

- Whether the desktop UI gets a council toggle (Phase 5).

## Maintenance protocol

Every session that changes code, releases, pins or plans adds a dated entry
at the top of this file, in the same commit as the change. That session also
rewrites any fixed section the change makes stale. When a plan changes status
or phase, update its row in `plans/MASTER_PLAN.md` and the plan itself in the
same commit. Keep entries factual: shas, tags, numbers, and what is left.
