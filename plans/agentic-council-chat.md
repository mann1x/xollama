# Agentic Council Chat

**Status:** ACTIVE · **Phase:** 0 — measure on b109 · **Index:** [MASTER_PLAN](MASTER_PLAN.md)

In this chat mode, one model name is a *council*. A client connects to xollama
the usual way: `/api/chat`, the OpenAI or Anthropic API, the CLI, or the
desktop app. The council runs inside the server and streams back one answer.
The model's own configuration defines the council: its roles, prompts and
parameters. `xollama tweak model` sets and configures it. On the opencoti
engine, PolyKV makes a council cheap: every member shares the conversation's
KV cache and prefills only its own role and turn.

## Progress

- [ ] Phase 0 — measure the ground on b109
- [ ] Phase 1 — the council flow in each candidate library; choose one
- [ ] Phase 2 — config and tweak
- [ ] Phase 3 — the runner, llama.cpp path
- [ ] Phase 4 — PolyKV path
- [ ] Phase 5 — surfaces and docs

## What the user sees
- `xollama create my-council -f Modelfile` (or `xollama tweak model my-council`)
  makes a model whose config layer carries a `council` section.
- `xollama run my-council`, or any OpenAI/Anthropic client asking for
  `my-council`, gets one streamed answer. Member deliberation is visible as
  `thinking` (opt-out), so existing clients render it with no change.
- Off means off: a model without `council` behaves byte-identically to upstream.

## The council: roles and flow (owner's specification)
```
user message
   │
   ▼
PLANNER ── decides: answer directly, or convene the council?
   │  direct (e.g. "Hello!", small talk, a trivial fact)
   ├─────────────► planner's answer streams straight to the user (1 call)
   │  council (a harder task)
   ▼
plan: what to find out / what each researcher should cover
   │
   ├─► RESEARCHER 1 ┐   in parallel (default 2, configurable)
   └─► RESEARCHER 2 ┘
   │
   ├─► CRITIC 1 ┐       in parallel (default 2, configurable), each reviews
   └─► CRITIC 2 ┘       the plan + findings: errors, gaps, disagreements
   │
   ▼
SYNTHESIZER ── writes the one answer the user gets
```
- **Routing is the planner's first job.** It returns a structured decision
  (`direct` + the answer, or `council` + a plan with one brief per
  researcher). The direct path must stay as fast as a plain chat turn: no
  pools beyond the shared root, no extra calls. The decision format is
  parsed strictly; a malformed decision falls back to the council (never
  loses the question).
- **Defaults:** 1 planner, **2 researchers, 2 critics**, 1 synthesizer.
  Counts, prompts and per-role model are configurable.
- **Sampling diversity:** every role gets its **own random seed** per
  request. **Researchers and critics** additionally get the model's
  temperature randomized within **±2 %** (relative: 0.70 → uniform in
  [0.686, 0.714]); the range is configurable (`temperature_jitter`, default
  0.02). Planner and synthesizer keep the model's temperature. Seeds and the
  drawn temperatures are logged per request so a run can be reproduced.
- **Streaming:** planner (when it routes to the council), researchers and
  critics stream as `thinking`, tagged by role; the synthesizer streams as
  `content`. `show_deliberation: false` hides the thinking; the answer is
  unchanged.
- **Optional bounded loop** (later, off by default): critics may send one
  round back to the researchers (`max_rounds`, default 1).

## PolyKV layout for this flow
```
owner session "<chat>~council"  (num_ctx = window, num_ctx_min = floor)
  P0  council system prompt + tools          (every role)
   └ P1  + the conversation so far            (every role; the planner runs here)
       ├ P2r + researcher role + plan         → researcher workers 1..n
       └ P2f + plan + all findings            (forked after research)
            ├ P3c + critic role               → critic workers 1..n
            └ P3s + critiques + synth role    → synthesizer
```
Each role prefills only what is new to it; the conversation is prefilled
once per turn. Closing the owner releases the whole tree.

## Where it lives (researched)
- **Definition**: new `Council` sub-struct in `types/xollama/config.go`
  (beside `Engine`, `KV`, `Session`, `Devices`; `Validate`/`Parse`/`Marshal`
  already there) — the `xollama.json` config layer, read back by
  `server/images.go` and exposed as `ShowResponse.Xollama`.
  Fields (draft): `planner`, `researchers`, `critics`, `synthesizer`, each
  {`count` (researchers/critics only; default 2), `model` (default: the
  council's base model), `prompt`, `max_tokens`}; `temperature_jitter`
  (default 0.02, applied to researchers and critics); `seed` (`random`
  default, or a fixed base for reproducible runs, offset per role/index);
  `max_rounds` (default 1); `show_deliberation` (default true); `polykv`
  (`auto|on|off`, default auto = on when the engine advertises it). Default
  prompts ship built in, so `tweak` only needs to switch the mode on.
- **Configuration**: new fields in `cmd/tweak/fields.go` (pattern:
  `session()`, `dca()` helpers) so `xollama tweak model` edits them and
  `xollama show` lists them via `tweak.SettingRows`.
- **Request path**: one surgical hook in `server/routes.go` `ChatHandler`
  (`:2758`): a model whose config has `council` is handed to an additive
  `server/council.go` runner. The shims (`middleware/openai.go`,
  `middleware/anthropic.go`) already sit in front of `ChatHandler`, so they need
  nothing. Precedent: `engine-introspect` (one route line + additive handler)
  and the web-search loop (`middleware/web_search.go`) that re-enters chat.
- **Engine side**: new `llm/engine_council.go` (or `llm/polykv/`) client for the
  calls xollama does not make today — owned pools, `fork`, `num_ctx` /
  `num_ctx_min` windows, `/kv`, `POST /sessions/{id}/close`, `/props.features`
  branching. Today `llm/engine_pool.go` only creates automatic prefix pools.

## How PolyKV carries a council (source: `/shared/dev/docs/cerebriline-polykv-integration.md`, opencoti `docs/features/polykv_api.md`, `polykv_fanout.md`)
The tree is the one in "PolyKV layout for this flow" above; the synthesizer
forking from the shared findings is opencoti's "three-role gather contract"
(`polykv_fanout.md`). Rules taken as requirements (guide §0): prefixes via `/apply-template` + a
sentinel, byte-prefix check before attach; workers send `pool_id` + own
`session_id`, no `num_ctx`; a 429 is a queue (wait `Retry-After`); close every
session at the end; pool id 0 is valid; ids never contain `/`; per-session
values out of the system turn; read `opencoti{n_pool_shared,pool_match,pool_len}`
to prove sharing. On llama.cpp (no PolyKV) the council still works, members
just prefill their own copies.

## Phases
- **Phase 0 — measure the ground on b109 (no feature code).** Development
  targets opencoti **b109** (owner's decision), not the pinned b65: it
  carries keepalive (`0388`/`0393`), `kv_pressure_v1` + resize (`0394`) and
  the E8/E9 fixes. Run b109 on solidPC for development via
  `XOLLAMA_ENGINE_ARGS`/a local engine path (the pin moves only when b109 is
  published and measured, per the pin rule). Measure: which
  `/props.features` it advertises; whether a request without `num_ctx`
  books a per-request `session_ctx_max` window (the unverified finding
  about today's xollama requests); a Python probe of the council tree above
  (planner → 2 researchers → 2 critics → synthesizer) with prefill saved,
  owner cells used, `n_pool_shared` per role, wall time; and the direct path
  ("Hello!") latency vs a plain chat turn.
- **Phase 1 — the council flow, built in each candidate library.** The
  planner → 2 researchers → 2 critics → synthesizer council with routing,
  seeds and jitter, implemented once per shortlisted library (eino,
  langgraphgo, trpc-agent-go) plus the in-house `errgroup` baseline
  (`golang.org/x/sync` is already a dependency), in a throwaway
  `plans/council-eval/` harness that is its **own Go module** (own `go.mod`),
  so no candidate's dependencies touch xollama's `go.mod` until one is chosen. Measured over a
  stub model and then against b109 (list below). Output: a benchmark table
  and the chosen library, recorded in the plan and MASTER_PLAN before any
  product code.
- **Phase 2 — config and tweak.** `Council` in `types/xollama`, validation,
  tweak fields, `show` rows, Modelfile round-trip; unit tests.
- **Phase 3 — the runner, llama.cpp path.** `server/council.go`, the
  ChatHandler hook + Registry row, streaming as `thinking` + `content`, the
  OpenAI/Anthropic shims verified; integration test.
- **Phase 4 — PolyKV path.** The engine client, the pool tree, sharing proven
  with `n_pool_shared` and `/kv`; A/B against Phase 3 (prefill, latency,
  cells).
- **Phase 5 — surfaces and docs.** CLI niceties, desktop UI toggle if wanted,
  `docs/xollama/council.mdx`, feature doc, STATE_SUMMARY/MASTER_PLAN update.

## Library research (read from source/go.mod 2026-09-25; nothing run yet)
Named candidates:
- **LangGOAP** — Python only (A* planner over Python LangGraph). **Out.**
- **lango** (lango.rpcx.io) — the website of `smallnest/langgraphgo`, same
  library, evaluated once.
- **smallnest/langgraphgo** — MIT, 305★, v0.8.5 (Jan 2026; July commits
  unreleased). `StateGraph[S]`, plain-func nodes, supervisor/swarm/map-reduce
  prebuilts; `graph` package near dependency-free. But: no token streaming
  (`StreamModeMessages` is a stub), events dropped on backpressure, a
  goroutine per listener per event, no `ctx.Done()` between supersteps,
  nondeterministic fan-out order.
- **dshills/langgraph-go** — MIT, 8★, dormant 10 months, no streaming, JSON
  deep copy per branch, and it *requires `github.com/ollama/ollama`* (our own
  module path). **Out.**
Found by the research, stronger:
- **cloudwego/eino** (compose + adk) — Apache-2.0, 13.2k★, monthly releases,
  84% coverage. The only one with token-level streams flowing through the
  graph (`schema.StreamReader`); parallel/sequential/loop agents,
  plan-execute, interrupts + checkpoints; core has no LLM SDKs, we implement
  `BaseChatModel{Generate,Stream}` over our engine. Costs: `any`+`reflect` at
  node boundaries; bumps `bytedance/sonic` 1.11→1.15 (gin uses it); v0.10
  alphas signal churn.
- **trpc-group/trpc-agent-go** — Apache-2.0, 1.8k★, v1 semver, very active,
  richest council tooling (team coordinator + swarm, parallel agent, Pregel
  graph with reducers, HITL). Costs: `map[string]any` state deep-copied per
  node, pulls grpc + 13 otel modules.
- google/adk-go v2 — requires `go 1.26.6` (above upstream's `go 1.26.0`) and
  genai-typed models. **Out.** langchaingo, tmc/langgraphgo, genkit, golc:
  out (no graph, dormant, or cloud-stack deps).

**Phase 1 shortlist (ranked):** 1. eino · 2. smallnest/langgraphgo (graph
package only) · 3. trpc-agent-go · baseline: an in-house fan-out/join runner
on `errgroup` + channels (~300 lines) that every library must beat.

**Phase 1 builds THIS council in each candidate** (the owner's flow above,
not a generic example): planner routing (`direct` vs `council`) →
2 researchers in parallel → 2 critics in parallel → synthesizer, with a
per-role random seed and ±2 % temperature jitter on researchers and
critics, plus the optional bounded critic→researcher loop. Both paths are
exercised: "Hello!" must take the direct path in one call, a hard task the
full council. First over a stub model emitting N tokens at a fixed rate,
then against b109 on solidPC. Measures:
1. per-step ns and allocs/op (`-benchmem`) at 1 KB / 64 KB / 1 MB state;
2. 3- and 8-wide fan-out wall time vs the ideal, goroutines, peak RSS;
3. streaming: TTFT at the HTTP edge, per-role tagged interleaving, ordering,
   drops under a slow consumer;
4. cancellation: client disconnect → all goroutines gone and the engine
   request aborted (goleak);
5. reducer correctness under `-race`, deterministic merges;
6. checkpoint/interrupt round-trip and its cost;
7. integration cost: `go mod tidy` diff (sonic, grpc, otel), binary size,
   still builds with `go 1.26.0`, no `github.com/ollama/ollama` self-require;
8. ergonomics: council LOC, adaptation needed for our message types.
Then the winner runs the same council against the real engine on solidPC
(llama.cpp and opencoti/PolyKV). Decision recorded in the plan and
MASTER_PLAN.

## Verification
resolve; `scripts/check-hooks.sh` unaffected.
- Part B (per phase, recorded in the plan): Phase 0 probe outputs; Phase 1
  benchmark table; Phase 2 `go test ./types/xollama/... ./cmd/tweak/...`;
  Phase 3 a live `/api/chat` + `/v1/chat/completions` against a council model
  on solidPC as the `ollama` user, plus `XOLLAMA_ENGINE=llamacpp` with no
  council configured is unchanged; Phase 4 `n_pool_shared > 0` on every member
  turn and all sessions/pools gone after the answer (`/kv`, `/polykv/pools`).

## Decision log

- 2026-09-25 — Development targets opencoti b109, not the pinned b65. The
  pin moves only when b109 is published and measured.
- 2026-09-25 — Defaults are 2 researchers and 2 critics. Every role gets a
  random seed per request. Researchers and critics also get the temperature
  jittered by ±2 % relative.
- 2026-09-25 — The planner routes: it answers trivial turns directly and
  sends harder ones to the council.
- 2026-09-25 — Phase 1 compares eino, langgraphgo and trpc-agent-go against
  an in-house `errgroup` baseline. It runs in `plans/council-eval/`, which is
  its own Go module.
