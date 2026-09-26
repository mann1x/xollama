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
| [Agentic Council Chat](agentic-council-chat.md) | ACTIVE | 0–7 closed 2026-09-26; Phase 8 (Cerebriline's agentic council compaction ported: writer, critics on halves, synthesizer; carried forward and incremental, sized on the grant) built, mutation-checked and live-tested on b133 2026-09-26 (five live faults fixed; open: the writer's fit at the trigger, bug-139 owner private cells); Phase 9 in progress (Cerebriline as a client: tools on council turns, one shared-prefix layout, sealed `council_chat_state` for resume, client-driven PolyKV); 9.1 features list + thinking tags built 2026-09-26; the pin moves to a published build with `pool_unowned_v1`, on a measurement | xollama | A model configured as a council (planner, researchers, critics, synthesizer) answers through the normal chat APIs, sharing KV through PolyKV |
| [Council: continue the conversation's pool](council-continue-pool.md) | WAITING | partly realized 2026-09-26 by forking a kept root (no `continue_pool`); the rest waits for an opencoti build with patch 0406 on the HF dev repo | xollama | Keep and continue the conversation's pool across council turns instead of rebuilding it |
| [Docker image](docker-image.md) | ACTIVE | first `:dev` image published (run 36221348282), GHCR public (2026-09-26); user testing open | xollama | Assemble the image on hosted runners from prebuilt artifacts (`scripts/docker-assemble.sh`, `llama/runtime-pin-linux.txt`) |

## How to use this document

- Add a row for every new plan in the same commit that creates it.
- When a phase closes, do four things in one commit: tick the phase in the
  plan, update its row here, add a dated entry to `STATE_SUMMARY.md`, and
  record any decision in the plan's decision log.
- A finished plan stays listed with status DONE. A dropped plan gets
  ABANDONED plus a one-line reason. Rows are never deleted.
