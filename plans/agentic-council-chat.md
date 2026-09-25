# Agentic Council Chat

**Status:** ACTIVE · **Phase:** 0 — measure on b65, re-measure on b111 · **Index:** [MASTER_PLAN](MASTER_PLAN.md)

In this chat mode, one model name is a *council*. A client connects to xollama
the usual way: `/api/chat`, the OpenAI or Anthropic API, the CLI, or the
desktop app. The council runs inside the server and streams back one answer.
The model's own configuration defines the council: its roles, prompts and
parameters. `xollama tweak model` sets and configures it. On the opencoti
engine, PolyKV makes a council cheap: every member shares the conversation's
KV cache and prefills only its own role and turn.

## Progress

- [x] Phase 0 on b65 (2026-09-25; results below)
- [ ] Phase 0 re-measured on b111 (when on HF)
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
- **Phase 0 — measure the ground (no feature code).** The target is opencoti
  **b111**, not the pinned b65. It carries keepalive (`0388`/`0393`),
  `kv_pressure_v1` with resize (`0394`/`0396`), the SWA residency fix
  (`0397`) and the E8/E9 fixes. Until b111 is on the HF dev repo,
  **development and smoke tests run on the pinned b65**, and every b65 result
  is marked as a b65 result. When b111 lands, the same probe runs again and
  the pin moves only after that measurement, per the pin rule. On solidPC the
  engine runs directly on a spare port as the `ollama` user; the installed
  `ollama.service` is left alone. Measure: which
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
  stub model and then against b111 (list below). Output: a benchmark table
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
then against b111 on solidPC. Measures:
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

## Phase 0 results — b65 (2026-09-25)

Engine `opencoti-0.10.5-c7-2609242056001` (b65, `b1789787714-c588c4f47`) ran
directly as `ollama` on `127.0.0.1:38311` on the RTX 3090, with the product's
flags: `-c 65536 --parallel 6 --kv-unified --polykv-max-pools 8 --flash-attn on`.
The model was llama3.1:8b. The conversation was
`docs/features/device-selection.md`, about 2.2k tokens, with a nonce. The
probes are in `plans/council-eval/probe/`: `council_tree.py`, `routing.py`
and `direct_cost.py`. The raw JSON is in
`/srv/ml/xollama-phase2/as-ollama/council-p0/`. **All of this is b65 and is
re-measured on b111.**

- **Features:** b65 advertises 24 flags, including the whole pool, admission,
  close and pressure set. It lacks `stream_keepalive_v1`, `boot_id_v1`,
  `pool_unknown_in_response_v1` and `tcp_keepalive_v1`, which b98 and later
  have, and `kv_pressure_v1`/`kv_resize_v1`, which b111 adds.
- **Per-request window, confirmed.** A request with a `session_id` and no
  `num_ctx` booked the full `session_ctx_max` (65,536 cells,
  `per_request: true`) for its duration, even though it used 47 cells. That is
  what every xollama request without a stated `num_ctx` does today. The
  council's owner therefore states `num_ctx` + `num_ctx_min`, and it got
  `X-Context-Window: 32768`.
- **The council tree shares as designed.** Typical run: 2 researchers,
  2 critics, max_tokens 384/256/512.

  | role | prompt | prefilled | n_pool_shared | pool |
  |---|---|---|---|---|
  | planner | 2,286 | 2,286 | — | (owner) |
  | researcher ×2 | 2,496–2,501 | 68–73 | 2,428 | P2r |
  | critic ×2 | 2,851 | 37 | 2,814 | P2f |
  | synthesizer | 3,372 | 29 | 3,343 | P3s |

  Pools: P1 2,214 tokens, P2r +214, P2f +386, P3s +529, with no <64-token
  warning. The owner held **3,343 cells** mid-run for the whole tree, against
  ~16,300 prompt tokens unpooled. The council's wall time was 13–17 s
  (research 2.6–5.5 s, critique 3.7 s, synthesis 3.2–5.6 s) at ~72 tok/s per
  member. Close returned `{found, released, kv_dropped}`, leaving 0
  allocations and 0 pools. P0 (system alone) was folded into P1 here, because
  nothing varies between them in one conversation. It becomes its own layer
  when many conversations share one council system prompt.
- **Routing is the weak link, and the decision must be route-only.** 20
  trials per message at temperature 0.7, with a random seed each time.

  | planner decision | trivial → direct | hard → council | malformed |
  |---|---|---|---|
  | full JSON (route + answer or plan + briefs), 256 tokens | 70 / 100 | 56 / 60 | 4 (hard, truncated) |
  | **route-only** `{"route"}` enum, 16 tokens | **86 / 100** | **60 / 60** | 0 |

  Every miss fails safe: a trivial message sent to the council costs time,
  never the answer. "Thanks, that helps." is the hard trivial case (6/20
  direct). A route-only decision also lets the direct answer stream as plain
  `content`, which an answer embedded in JSON cannot.
- **Direct-path cost with route-only.** Medians of 5 cold trials with a
  2.2k-token prefix: a plain turn's time to first token is 0.579 s. The
  decision followed by the answer on the same session reaches its first token
  at 0.706 s, so the decision adds ~0.13 s. The answer call then prefilled 3
  tokens, because the decision's slot cache carries it. On a warm multi-turn
  conversation the overhead is the decision's ~8 decoded tokens plus one
  round trip.

**What this changes in the design:** the planner makes two calls. The first
is a grammar-constrained route-only decision. Then comes either the direct
answer, streamed as `content` on the same session, or the plan-and-briefs
call, which is the first council step. Routing accuracy per model is a
measured property, recorded when a model is made a council.

## Decision log

- 2026-09-25 — The target is opencoti b111 (the owner moved it from b109).
  Until b111 is on the HF dev repo, development and smoke tests run on the
  pinned b65. The pin moves only when b111 is published and measured.
- 2026-09-25 — Defaults are 2 researchers and 2 critics. Every role gets a
  random seed per request. Researchers and critics also get the temperature
  jittered by ±2 % relative.
- 2026-09-25 — The planner routes: it answers trivial turns directly and
  sends harder ones to the council.
- 2026-09-25 — Phase 1 compares eino, langgraphgo and trpc-agent-go against
  an in-house `errgroup` baseline. It runs in `plans/council-eval/`, which is
  its own Go module.
- 2026-09-25 — The planner's decision is route-only (`{"route":"direct"|"council"}`
  under a JSON-schema grammar, ≤16 tokens). The direct answer and the plan
  are separate calls on the same session. Measured on b65: 86/100 trivial
  messages direct, 60/60 hard messages to the council, no malformed output,
  and +0.13 s to the first token on the direct path. The full-JSON decision
  got 70/100 and could not stream its answer.
