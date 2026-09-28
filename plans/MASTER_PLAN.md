# xollama — Master Plan

## Purpose

This is the index of every plan in `plans/`. Each plan holds its own detail:
scope, phases, measurements and decisions. This page says which plans exist,
where each one stands, and the constraints they all share. The running log of
what happened is [`../STATE_SUMMARY.md`](../STATE_SUMMARY.md).

## Defining constraints (read first)

- **Never rename the Go module path.** The binary is xollama; the import path
  is upstream's.
- **Off means off.** With `XOLLAMA_ENGINE=llamacpp` and no xollama flags,
  behaviour is byte-identical to upstream. Every new feature is opt-in per
  model or per environment variable.
- **Additive files over hooks.** An upstream file is edited only through a
  marked `xollama-hook:` registered in `docs/protocols/UPSTREAM-SYNC.md`.
- **The fork supplies llama.cpp.** `LLAMA_CPP_VERSION`, `llama/server` and
  `llama/compat` change in mann1x/ollama first and arrive here by sha
  (`docs/protocols/FORK-SYNC.md`).
- **Releases only through `docs/protocols/RELEASE.md`.** Never push a `v*`
  tag by hand, and never copy exes onto a host as an update.
- **Engine pins move on measurements**, not changelogs
  (`llm/engine_defects.go`, `scripts/phase2-engine-ab.py`).

## Plans

| Plan | Status | Phase | Owner | Summary |
|---|---|---|---|---|
| [Agentic Council Chat](agentic-council-chat.md) | ACTIVE | 0–7 closed 2026-09-26; Phase 8 (Cerebriline's agentic council compaction ported: writer, critics on halves, synthesizer; carried forward and incremental, sized on the grant) built, mutation-checked and live-tested on b133 2026-09-26 (five live faults fixed; open: the writer's fit at the trigger, bug-139 owner private cells); Phase 9 in progress (Cerebriline as a client: tools on council turns, one shared-prefix layout, sealed `council_chat_state` for resume, client-driven PolyKV); 9.1 features list + thinking tags and 9.2 client placement + chat_render_v1 and 9.3 shared-prefix layout (A/B: same format adherence, +12 s per council turn from fuller research) built and measured 2026-09-26; compact compaction writer measured on b137 and shipped; 9.4 sealed `council_chat_state_v1` built and resumed live on b137 (bug-142, a left turn hanging, fixed); 9.5 tools on council turns (`council_tools_v1`: read-only tools for researchers and critics, writes by the synthesizer, calls forwarded under member ids, suspend/resume in the state) built and run live on b137 — after the 2026-09-27 fixes (evidence in findings, charter, resumed members' PolyKV layer) 6/6 correct on the live tool A/B, as the plain model, at 27–38 s vs 5–7 s; narrated-call guard (10/10 vs plain 8/10) and `council_evidence` (long results by ref; open: a member whose own results outgrow the window) 2026-09-27; the pin moves to a published build with `pool_unowned_v1`, on a measurement; Phase 10 (ab-3 follow-ups) 2026-09-27: 10.1 a slot per parallel member, 10.2 one critic, 10.3 shared reads, 10.4 confirmed verdict, 10.5 council across turns, 10.6 broadcast (off by default, under evaluation) built; 10.7 ab-4 run on eleven2go 2026-09-28 (council unfixed at 1463 s vs plain 233 s); Phase 11 (researchers propose, the synthesizer tests; the builder) 2026-09-28: 11.1 caps and 11.2 repeated-call note built, 11.3 resumed-member cache waits on opencoti #525, 11.4 test loop and re-plan built (preemption open), 11.5 builder and the synthesizer's front turn built; verdict preemption built; 11.3 member sessions live for the council's life (opencoti #526) built; ab-5 run (council unfixed on both hosts, plain fixed), 11.6 fixes A–J built (step budgets, verdict nudge, checks across turns, the front forwards before investigating); 11.7 sources and history built (every council message names its writer, earlier turns attributed per member, working notes dropped); 11.8 a council follows the engine's parallel slots (4 by default) and cloud members run `cloud_parallel` (3) at a time, built; 11.9 async critic reviews of the synthesizer's checks built (queued per conversation, returned as each finishes, DONE waits for the reviews out); 11.10 the planner keeps a task list (schema field, rules enforced by the runtime, carried across turns), 11.11 checks that stop moving change the approach (topic-agnostic), 11.12 a review always ends with its verdict, built; 11.13 usage per role (`council_usage_v1`) built; 11.14 proposals applied together, one check, built; 11.15 loop guards (Cerebriline), front handoff, balanced researchers, built; 11.16 the builder on its own model (`council.builder`) built; rerun on eleven2go with the #530 relief | xollama | A model configured as a council (planner, researchers, critics, synthesizer) answers through the normal chat APIs, sharing KV through PolyKV |
| [Council: continue the conversation's pool](council-continue-pool.md) | WAITING | partly realized 2026-09-26 by forking a kept root (no `continue_pool`); the rest waits for an opencoti build with patch 0406 on the HF dev repo | xollama | Keep and continue the conversation's pool across council turns instead of rebuilding it |
| [Docker image](docker-image.md) | ACTIVE | first `:dev` image published (run 36221348282), GHCR public (2026-09-26); user testing open | xollama | Assemble the image on hosted runners from prebuilt artifacts (`scripts/docker-assemble.sh`, `llama/runtime-pin-linux.txt`) |

## How to use this document

- Add a row for every new plan in the same commit that creates it.
- When a phase closes, do four things in one commit: tick the phase in the
  plan, update its row here, add a dated entry to `STATE_SUMMARY.md`, and
  record any decision in the plan's decision log.
- A finished plan stays listed with status DONE. A dropped plan gets
  ABANDONED plus a one-line reason. Rows are never deleted.
