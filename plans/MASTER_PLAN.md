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
| [Agentic Council Chat](agentic-council-chat.md) | ACTIVE | 0 — measure on b109 | xollama | A model configured as a council (planner, researchers, critics, synthesizer) answers through the normal chat APIs, sharing KV through PolyKV |
| [Docker image](docker-image.md) | PARKED | — | xollama | Assemble the image on hosted runners from prebuilt artifacts; waits for opencoti b109 on HF |

## How to use this document

- Add a row for every new plan in the same commit that creates it.
- When a phase closes, do four things in one commit: tick the phase in the
  plan, update its row here, add a dated entry to `STATE_SUMMARY.md`, and
  record any decision in the plan's decision log.
- A finished plan stays listed with status DONE. A dropped plan gets
  ABANDONED plus a one-line reason. Rows are never deleted.
