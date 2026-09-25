# xollama — State Summary

Newest entry first. Each entry is dated and says what changed (commit shas,
release tags, measurements) and what is left. The fixed sections below the
entries are rewritten in place so they always describe *now*. Plans are
indexed in [`plans/MASTER_PLAN.md`](plans/MASTER_PLAN.md).

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

`v0.34.2-xollama.1` is the latest release. No product code is in flight.
The Agentic Council Chat has Phase 0 measured on the pinned b65, and the same
probes run again on b111 once it is on the HF dev repo. Phase 1, the library
bake-off, is next.

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

- **opencoti:** publishing b111 to HF. This unparks the Docker image and moves
  the pin once b111 is measured. Also waiting on the spent-response port,
  queued behind row K, and the E2B/E4B gate, which needs an HF repo@rev.
- **mann1x/ollama (fork):** the static `llama/compat/README.md` commit. When
  it lands, xollama takes it by sha.

## Known gaps

- Docker image not published yet (PARKED, [`plans/docker-image.md`](plans/docker-image.md)).
- Open fork-sync items: `docs/protocols/FORK-SYNC.md` § "Open items".
- Carried upstream PRs: `docs/protocols/CARRIED-PATCHES.md`.
- UI lockfile Dependabot alerts are inherited from upstream and not addressed.

## Immediate next steps (in order)

1. Council Chat Phase 0 on b111 when it is on HF (b65 done 2026-09-25): rerun `plans/council-eval/probe/`, measuring `/props.features`,
   the per-request window, the pool tree probe and the direct-path latency.
2. Council Chat Phase 1: build the council in eino, langgraphgo,
   trpc-agent-go and the `errgroup` baseline in `plans/council-eval/`
   (its own module), then benchmark them and choose one.
3. When b111 is on HF: unpark the Docker image and pin the fork's existing
   runtime tgz (`33ac42c1…`).

## Open decisions

- Which orchestration library the council uses. Phase 1 decides.
- Whether the desktop UI gets a council toggle (Phase 5).

## Maintenance protocol

Every session that changes code, releases, pins or plans adds a dated entry
at the top of this file, in the same commit as the change. That session also
rewrites any fixed section the change makes stale. When a plan changes status
or phase, update its row in `plans/MASTER_PLAN.md` and the plan itself in the
same commit. Keep entries factual: shas, tags, numbers, and what is left.
