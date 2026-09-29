# Agentic Council Chat

**Status:** ACTIVE · **Phase:** 0–7 closed 2026-09-26; Phase 8 (Cerebriline's council compaction, ported) approved 2026-09-26, being built; the pin moves to a published build with `pool_unowned_v1`, on a measurement · **Index:** [MASTER_PLAN](MASTER_PLAN.md)

In this chat mode, one model name is a *council*. A client connects to xollama
the usual way: `/api/chat`, the OpenAI or Anthropic API, the CLI, or the
desktop app. The council runs inside the server and streams back one answer.
The model's own configuration defines the council: its roles, prompts and
parameters. `xollama tweak model` sets and configures it. On the opencoti
engine, PolyKV makes a council cheap: every member shares the conversation's
KV cache and prefills only its own role and turn.

## Progress

- [x] Phase 0 on b65 (2026-09-25; results below)
- [x] Phase 0 re-measured on b111 (2026-09-26; notes under the Phase 4 results)
- [x] Phase 1 — the council flow in each candidate library; **in-house errgroup chosen** (2026-09-25; results below)
- [x] Phase 2 — config and tweak (2026-09-26; results below)
- [x] Phase 3 — the runner (2026-09-26; live on b111, results below)
- [x] Phase 4 — PolyKV path (2026-09-26; A/B on b111, results below)
- [x] Phase 5 — surfaces and docs (2026-09-26; results below)
- [x] Phase 6 — PolyKV sizing and pressure-driven compaction (2026-09-26; live on b128, including the planner on the conversation's root; below)
- [x] Phase 7 — roles on other models and other ollama instances, cloud models included (2026-09-26; live on cloud models and eleven2go; below)
- [ ] Phase 8 — Cerebriline's council compaction, ported (approved 2026-09-26, being built; below)

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

## Context, pressure and compaction

Every member states its context, and PolyKV reports the pressure, so each
member's window and the conversation's compaction are driven by what the
engine says, not by fixed sizes. Sources:
`/shared/dev/docs/cerebriline-polykv-integration.md` §6.5 (window
negotiation, compaction on raw pressure) and §10 (bookings).

- **Unpooled (llama.cpp path, or PolyKV off):** each member call states
  `num_ctx` sized to its own prompt plus `max_tokens` plus headroom. Phase 1
  measured the alternative: without `num_ctx` each member books the whole
  `session_ctx_max`, and the second parallel member is refused.
- **Pooled (PolyKV):** the council's **owner** session states the window
  once, with `num_ctx` and `num_ctx_min`. A grant is sticky for the life of
  the session (§10.3), so the ask must be right the first time. Members are
  workers: they send `pool_id` and their own `session_id`, and **no**
  `num_ctx`, and are charged `peak − pool_len` against the owner's window.
- **Pressure is read per session.** The owner's raw `pressure` comes from
  `GET /kv` `allocations[]`, by the wire session id (`session_pressure_v1`),
  with the pool's own `pressure` as the fallback. This is the signal
  Cerebriline compacts on (`polykvSaysCompact`, threshold 0.85).
- **What compacts.** Members are single-turn, so their context never grows
  across turns. What grows is the **conversation** in the owner's P1 layer,
  plus, within one turn, the findings (P2f) and critiques (P3s) layers.
  - Before a turn: if the owner's pressure is at or above the threshold,
    compact the conversation (summarise the oldest turns, keep the latest
    verbatim), then re-root the tree on the compacted P1.
  - Within a turn: if forking P2f or P3s would push the owner past the
    threshold, condense the findings or critiques first, rather than let a
    worker hit `session allocation full`.
- **Grant, not configuration.** Triggers, targets and output room are sized
  against the granted window (`X-Context-Window`), never the configured one.
  A response without the header means the grant is unknown, not unchanged.
- **On b111** (`kv_pressure_v1`, `kv_resize_v1`, patches 0394/0396):
  - compact on **global** pressure too: others are being refused
    (`refused_60s`) while this council is not full;
  - **shrink** the owner's booking after a compaction (`POST
    /sessions/{id}/resize`, between requests only). Until then a compacted
    session keeps its full booking (§10.1).
- **Config** (Phase 2): `context.window`, `context.floor` (→ `num_ctx_min`)
  and `context.compact_at` (default 0.85). Per-role `max_tokens` sets each
  member's output room.
- **As built (Phase 4, b111).** The grant is read from `/kv`
  `allocations[]` by the owner's session id, not from `X-Context-Window`.
  Before a turn the conversation is compacted when its rendered tokens pass
  `compact_at` of the grant (or the floor, if larger). The sticky-grant
  premise no longer holds on b111: the owner grows back toward its ask when
  nothing is refused, and after a turn under global pressure (`refused_60s`)
  it shrinks, deferred, to what it uses plus a reserve. **Not built:**
  condensing the findings or critiques within a turn, and compaction driven by
  the per-session `pressure` field. Neither has been needed so far: a turn's
  layers are a few thousand tokens against a 16k grant.

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
  with `n_pool_shared` and `/kv`, and compaction on the owner's pressure
  (section above), with resize and global pressure once b111 is published.
  A/B against Phase 3 (prefill, latency, cells).
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

## Phase 1 results — the library bake-off (2026-09-25)

The harness is `plans/council-eval/`, its own module `councileval`; its
README describes the layout. One shared core (`council/`) holds the prompts,
the draws and the steps, and every runner reaches the model only through it.
One suite (`suite/`) tests and benchmarks every runner. Each library runner
was written idiomatically by its own agent: branch or conditional edge for
the route, the library's parallel construct for the fan-out, and a real
graph cycle for the loop. Each has a `NOTES.md` with file:line evidence.

**Correctness.** All four runners pass the 11-test suite under
`-race -count=3`: routes, call counts, event tagging, researcher order,
hidden deliberation, seeds and jitter, widths 3 and 8, the bounded loop,
cancel with goleak, sibling cancel on failure, and a slow consumer. **No
library does sibling cancellation on its own.** eino, langgraphgo and
trpc-agent-go each wait out every task of a step after one fails. Each needed
a `WithCancelCause` wrapper and its own recorded error, or it returned a
sibling's `context.Canceled` in place of the real failure. The suite was
checked against planted bugs in the baseline (serial researchers, findings
in completion order), and each was caught.

**Orchestration cost over the stub** (`-benchtime 2s`, same host, µs rows ±30 %):

| runner | council (1 KB state) | council (1 MB) | direct path | fan-out ×ideal w3 / w8 | binary (stub prog) | deps |
|---|---|---|---|---|---|---|
| **baseline (errgroup)** | **56 µs, 159 allocs** | **57 µs, 14 KB** | **3.2 µs, 28 allocs** | 1.01 / 1.02 | 2.2 MB | 85 |
| langgraphgo v0.8.5 | 69 µs, 223 allocs | 107 µs | 16.6 µs, 59 allocs | 1.01 / 1.04 | 4.3 MB | 215 |
| eino v0.9.21 | 130–180 µs, 686 allocs | 154 µs | 56–63 µs, 301 allocs | 1.02 / 1.04 | 14.7 MB | 270 |
| trpc-agent-go v1.11.2 | 892 µs, 2,262 allocs | **22.5 ms, 33 MB** | 278 µs, 608 allocs | 1.08 / 1.07 | 13.4 MB | 432 |

**Against a real engine** (b65, omnimerge v4 27B IQ2_M, thinking off,
`-c 65536 --parallel 6 --kv-unified`, no pools; one council and one
"Hello!" per runner):

| runner | council wall | first thinking | first answer token | direct path |
|---|---|---|---|---|
| baseline | 64.9–67.2 s | 3.2–3.4 s | 48.8–51.0 s | 3.4–3.8 s |
| eino / eino-native | 68.0 / 69.7 s | 3.4 s | 51.6 / 53.3 s | 3.4 s |
| langgraphgo | 65.0–66.2 s | 3.4–3.5 s | 48.7–49.9 s | 3.6–3.7 s |
| trpc-agent-go / native | 68.3 / 66.3 s | 3.6 / 3.4 s | 52.0 / 50.0 s | 3.4–3.8 s |

At model speed the orchestration does not show; the spread is sampling
noise. What a library costs is CPU, allocations and dependencies on every
request, plus the guards it needs added.

**Inside xollama** (blank-imported into a scratch worktree, then
`go mod tidy`):
- eino bumps `bytedance/sonic` 1.11.6 → 1.15.0, which is gin's JSON, along
  with its loader, cpuid, base64x and x/arch (+16/−6 go.mod lines).
- langgraphgo bumps protobuf, go-sqlite3, testify and json-iterator (to a
  pseudo-version), and pulls langchaingo into `graph` (+15/−9).
- trpc-agent-go bumps protobuf, go-sqlite3, testify, easyjson and
  goccy/go-json, and links grpc, otel and the OTLP exporters even with
  tracing off (+25/−6).

Every one of these moves modules upstream owns, which is a merge conflict on
every sync (`docs/protocols/UPSTREAM-SYNC.md`).

**Library-specific findings** (details in each `NOTES.md`):
- **langgraphgo:** its native streaming is unusable.
  - `StreamModeMessages` is a stub.
  - Events drop under load (1,120 of 5,000 lost).
  - Listeners on a shared compiled graph leak events between concurrent
    requests.
  - It adds a fixed 10 ms sleep per request.
  - It has a close race, confirmed by `-race` (`graph/streaming.go:101`
    against `:218`).
  - There is no ctx check between steps, and fan-out order is random.
- **eino:**
  - The width has to be faked with pre-declared slots.
  - Every Invoke rebuilds one channel per node.
  - Its merge registries are process-global and unlocked.
  - Native streaming needed two goroutines per member and a drain barrier. The
    suite passed without the barrier, yet under a slow consumer 3,940 thinking
    events arrived after the answer had started.
- **trpc-agent-go:**
  - It JSON-marshals the whole state per node for tracing, with no switch
    (`executor.go:2873`).
  - The deep copy nils funcs and chans and zeroes unexported fields.
  - Errors reach the caller only as strings.

**Engine client findings** (for Phases 3 and 4):
1. **A member must state `num_ctx`.** Without it, every member books
   the full 65,536-cell `session_ctx_max`, and the second parallel researcher
   was refused with a 429. That 429 is also why the client must wait out
   `Retry-After`.
2. **The planner's calls share one session**, closed at the end. With the
   decision in a separate session that closed at once, the direct answer
   re-prefilled all 2.3k tokens: 6.1 s against 3.3 s.
3. **One leak, not reproduced.** One planner session in 18 runs was never
   released and was not in the engine log. It did not reproduce in 8 runs
   instrumented to record every close answer. The 300 s TTL reclaimed it.
   Phase 3's close path must record and check every close answer, as
   `cmd/council-run` now does.

**Pool sharing on a hybrid model (Phase 0 addendum, b65, omnimerge v4
IQ2_M).** qwen35 is GDN-hybrid, so a pool shares only on an exact full-state
match. The sentinel-cut layers match exactly:
- researchers prefilled 76–77 tokens of 2.6k;
- critics 41 of 3.4k;
- the synthesizer 33 of 3.9k;
- the owner held 3,858 cells;
- the engine logged 0 `needs exact full-state share` warnings.

The council took 54 s pooled, against 65–69 s unpooled above. The IQ2_M tag
`omnimerge-v4-mtp_tb:27b-iq2m-128k` has **no MTP head** (851 tensors,
`nextn_predict_layers` unset; the Q4_K_M has 866 and `1`), so it ran without
drafting.

**Decision: the in-house `errgroup` runner.** It is the fastest by 1.2–16×
on the direct path, 5×+ against eino and trpc, and flat in state size. It
adds no dependency (`x/sync` is already in `go.mod`), it is ~70 lines, and
it was correct on its first run. No library gave the council anything it
needed. Each needed the same guards added, and each would move modules
upstream owns. Ideas worth borrowing:
- index-slot reducers (trpc, langgraphgo), which the baseline already does
  with preallocated slices;
- a checkpoint/interrupt model (eino), if human-in-the-loop is ever wanted.

## Phase 2 results — config and tweak (2026-09-26)

- **Schema v4:** `types/xollama/council.go` holds `Council`, `CouncilRole`,
  `CouncilContext`, validation, `Clone`, `Prune` and `LaunchConfig`.
  `Config.Council` raises the schema to v4: an older build would read it as
  an unknown field and serve a plain chat, so it must refuse instead.
  `council.enabled` is the only thing a council needs. Settings stated
  without the switch are refused, except `enabled: false` alone, which says
  "not a council" over a parent that is one. Validation also refuses:
  - a count on the planner or the synthesizer;
  - a width above 8 (Phase 1's widest), more than 4 rounds, or a jitter
    outside [0, 0.5];
  - `polykv: on` on a model that pins llamacpp;
  - a floor above the window, or `compact_at` outside (0, 1).
- **`xollama tweak model`:** 23 rows in `cmd/tweak/council.go`, appended to
  the one field table.
  - `--council` walks the core settings. `--council-charter` walks the
    charter and every role's prompt.
  - A new `kindText` keeps prompts as written, and takes `@path` to read one
    from a file.
  - A new `quiet` row attribute skips council questions without a word in the
    full walk while the council is off. Named by a flag, they say why.
  - A stated `temperature_jitter: 0` means "no spread", and a stated seed
    means "reproducible". Both are pointers, so "unset" stays distinct.
  - Dry-run on the live server: `--council=on --council-researchers=3
    --council-jitter=0` gives the v4 JSON expected.
- **`xollama show`:** the rows come from the same table (`SettingRows`), so
  they need no extra code (tested). **Modelfile:** a council with multi-line,
  quoted prompts survives the whole chain, from `show --modelfile` through
  the parser to the create request (`TestAModelfileCarriesACouncilThroughCreate`).
- **Found and fixed: a council would have forced its own runner.** The
  launch config carried the whole xollama config, and the scheduler compares
  it with `reflect.DeepEqual`. So a council tag `FROM` a plain model would
  have loaded a second copy of the weights, and an edited prompt would have
  reloaded the model. `llamaServerConfigForModel` now uses
  `Config.LaunchConfig()`, which is the config without the council, nil when
  nothing else is stated, and at the rest's own version. This is inside the
  existing `model-config` hook, and the Registry row says so. The new
  `TestSchedNeedsReloadOnXollamaConfig` cases fail without the fix.
  **Corrected in Phase 3 (measured on b111):** this keeps the council out of
  the launch, but it does not make a council tag share its base's runner.
  Upstream's `ManifestDigest` is in the same config, so any two tags over one
  blob swap the runner (the base warmed, the council tag's first turn loaded
  again, 23 s). Not changed: that comparison is upstream's behaviour.
- **Resolved (2026-09-26): qwen35 is no longer single-sequence on
  opencoti.** Upstream's `parallelUnsafeArchitectures` (ollama#4165) now binds
  only stock llama.cpp. Measured on b111 before lifting it:
  `probe/parallel_correctness.py` found omnimerge v4 IQ2_M 12/12 correct and
  identical serial vs concurrent, and no cross-task bleed on qwen3.5:2b.
- **Deploy note:** a server older than this build refuses a v4 config (by
  design), so writing a council needs the new build serving.

## Phase 3 results — the runner (2026-09-26)

- **Where it lives.** `internal/council/` holds the Phase 1 errgroup runner,
  with the model's settings wired in (`FromModel`, `Charter`, per-role prompt,
  model and max_tokens). `server/council.go` serves each member as an ordinary
  chat turn through `ChatHandler`, in process: a gin context over a pipe, marked
  by a context key so a member never convenes the council again. The `council`
  hook is one line in `ChatHandler`, with a Registry row.
- **Sessions.** The planner keeps the conversation's engine session. Every
  other member gets `<session>~researcher-N`, `~critic-N` or `~synthesizer`,
  because the derived id is the same for all of them (system prompt plus first
  user turn), and one session would have serialized them.
- **Streaming.** The deliberation goes out as thinking and the answer as
  content, through upstream's `writeChatResponse`, so non-streamed requests and
  the OpenAI and Anthropic shims need nothing. One member holds the floor at a
  time: the first live run changed heading on every line.
- **Bypassed:** requests with tools or a `format`, and `think:false` (answer
  only).
- **Live on b111** as `ollama`, on an isolated store built from hardlinks: the
  service's store is untouched, because an old server refuses a v4 config.
  Model: omnimerge v4 IQ2_M council tag, `num_ctx` 16384, `-np 4`.

  | request | route | members | wall |
  |---|---|---|---|
  | `/api/chat` "Hello!" (cold, then warm) | direct | 2 | 21 s / 1.4 s |
  | `/api/chat` 10 GB sort question, streamed | council | 7 | 54 s, 102 s |
  | second turn ("200 GB?") | council | 7 | 62 s |
  | `/v1/chat/completions` (binary search) | council | 7 | 69 s, 6.9k chars reasoning |
  | `/v1/messages` "Hi there" | direct | 2 | 1.4 s |

  The answers were on point: in-place sort for 10 GB, external merge sort for
  200 GB. The researchers' text interleaved, which shows they ran
  concurrently.
- **Corrected: a council tag does not share its base's runner.** See the
  Phase 2 note. Any two tags over one blob swap the runner (upstream's
  `ManifestDigest`), so a client should use the council tag for everything.
- **Not in Phase 3:** member windows, pools and pressure. Members state the
  request's own `num_ctx` and run on the engine's ordinary sessions. Phase 4
  adds the PolyKV client: owner session, `/apply-template` pools, and
  `kv_pressure_v1` / `kv_resize_v1`, which b111 advertises.

## Phase 4 results — the PolyKV path (2026-09-26)

- **Where it lives.** `llm/engine_council.go` is the engine client: a
  `Placement` on a completion (a pool to attach to, or a window to book), and
  the `PolyKV` interface on the opencoti runner (pools, fork, release, session
  close, `/kv`, resize). `server/council_polykv.go` is the tree. Both are
  additive; the hunks in upstream files are listed in the `council` Registry
  row.
- **The tree, per turn.** The planner runs on the conversation's session as
  the owner and books the window (`num_ctx`, `num_ctx_min` = the council's
  floor). Each layer is built once, pinned to the owner and forked from the
  longest prefix already built: P1 the conversation, P2r plus the plan for the
  researchers, P2f plus the findings, P3s plus the critiques for the
  synthesizer. A layer is the rendered prompt up to a sentinel message, checked
  as a byte prefix of what the member will send. Workers attach by `pool_id`
  on their own sessions, with no window, and are closed when they finish. The
  pools are released newest first. A member on another model is not pooled.
- **Pressure.** Before a turn the owner's grant is read from `/kv`; if the
  engine had granted less than the ask and nobody is being refused, the owner
  grows back (a 429 retries at `largest_admissible`). After a turn, if others
  are being refused, the owner shrinks, deferred, to
  `max(floor, used + reserve)` rounded to 256, but only when that gives back
  at least 4096 cells or 10 %. The conversation is compacted into the system
  message once it passes `compact_at` (0.85) of the grant: old turns become a
  summary, and the last three stay verbatim.
- **Seats.** A council on PolyKV launches with `2 + 2 × rounds` extra pool
  seats (4 for one round); `polykv off`, or no council, launches exactly as
  before. So a PolyKV council tag and its Phase 3 twin do not share a runner.
- **A/B on b111** as `ollama`, isolated store, omnimerge v4 IQ2_M, `num_ctx`
  16384, `-np 4`. Two tags over one blob, `polykv off` (Phase 3) and `on`,
  in ABAB blocks of two council questions each. Raw data:
  `/srv/ml/xollama-phase2/as-ollama/council-p4ab/` (`rows.tsv`, `/kv` trace
  every 2 s in `kvtrace.tsv`). Every turn went to the full council (7 members).

  | per council turn (n = 4 each) | Phase 3 | Phase 4 (PolyKV) |
  |---|---|---|
  | prompt tokens, all members | 5,906–6,416 | 5,804–6,556 |
  | served from cache | 48–52 % | 89–94 % |
  | **prefilled (computed)** | **2,974–3,253, mean 3,139** | **408–637, mean 525 (−83 %)** |
  | peak KV cells in use | 4,277–4,378 | 2,393–2,823 (≈ −40 %) |
  | pool seats in use during a turn | 0 | 4 of 4 |
  | wall | 53.7–67.6 s, mean 60.2 | 49.2–66.8 s, mean 57.6 |
  | wall per generated token | 26.5–27.6 ms | 26.4–28.4 ms |

  After every Phase 4 turn `/polykv/pools` listed no pools and `/kv` no
  unowned pools. No booking was refused. Only the owners held windows: one
  per conversation, kept for the next turn.
- **What that means.** PolyKV does what it is for: each member prefills only
  what is new to it, and the cache holds one copy of the conversation instead
  of seven. On this model and GPU the turn is decode-bound (about 2,000
  generated tokens), and the 2,600 tokens saved are about 1–2 s of prefill,
  inside the spread of the generation length. So latency is at parity here.
  The savings grow with the conversation: every member re-prefills the whole
  history in Phase 3, and none does in Phase 4. They also grow with the
  number of members.
- **Noise.** The user's `ollama.service` loaded its embedding models on the
  3090 during the run (up to 3 runners, about 2.5 GB). That affects the wall
  times, not the token counts.
- **Phase 0 on b111 (llama3.1).** b111 advertises 31 features, including
  `kv_pressure_v1`, `kv_resize_v1`, `kv_resize_deferred_v1`, `boot_id_v1`
  and `stream_keepalive_v1`. Pools share as designed: researchers prefill
  26–40 tokens, critics 37, the synthesizer 29. A council turn takes
  14–18 s, and closing releases everything. The omnimerge probe runs came
  back with empty findings, a probe artefact: a thinking model's text went to
  `reasoning_content`, which the probe did not read.
- **Found on the way:** the members saw the model's own `MESSAGE` turns twice
  (bug-116, fixed and guarded by `TestTheModelsOwnMessagesReachEachMemberOnce`).
  Separately, every runner swap waits about 2.3 s for a GPU-discovery
  subprocess, then kills it and logs an ERROR. This happened in the Phase 3
  run too (bug-117, open).
- **Also in this change:** `/api/engine` now reaches every opencoti
  management route, read and control, so a pool tree or a window can be
  inspected and driven through xollama (`docs/xollama/introspection.mdx`).

## Phase 5 results — surfaces and docs (2026-09-26)

- **Docs.** `docs/xollama/council.mdx` is the user's page: making a council
  (`cp` + `tweak`, or a Modelfile `XOLLAMA` block), every setting with its flag
  and default, what a client sees, PolyKV with the Phase 4 numbers, and the
  limits. It is linked from the index and the navigation. `tweak.mdx` keeps
  the how-to and points there. `docs/features/council.md` is the maintainers'
  page: where each piece lives, the hooks, the invariants and the tests.
- **CLI, walked live** as `ollama` on b111 with the model's own context
  (131k, `-c 524288` at `-np 4`): `cp`, `tweak --council=on` and `show` work as
  written. The walk found two defects:
  - **One-shot `xollama run <council> "…"` never reached the council.**
    Upstream sends a one-shot prompt to `/api/generate`, and the council is
    chat-only, so the plain model answered with no error. `RunHandler` now sends
    it through `chat()` when `/api/show` says the council is on; `--format`
    keeps generate (`cmd/council_run.go`, one `council` hook line). Live:
    "Hello!" answered on the direct path, `--verbose` prints the council's
    summed counts, and `--hidethinking` prints the answer alone.
  - **A refused critic failed the turn and expired the model (bug-118).** At
    131k the engine's elastic recurrent-state cache stayed at 4 committed cells
    (of 8 reserved) and refused a critic with 429. Council members use the
    native chat path, which did not wait out a 429 as the completion path does.
    The error then carried ggml's routine `failed to allocate graph,
    reserving` line, which upstream's out-of-memory heuristic matches, so the
    scheduler expired every loaded model. Both are fixed: the chat path queues
    on 429, and on opencoti that line is not an error.
  - **With those fixed, the turn deadlocked instead.** Every sequence on
    this hybrid model, pool or slot, holds one recurrent-state cell. The owner
    and the four pinned layers already held more than the four cells the
    engine would commit at 131k, so the synthesizer waited out its whole 2-minute
    admission budget three turns in a row (188 refusals). On a recurrent engine
    (`/kv` reports an `rs` block) the tree now releases a stage's layer once
    its workers are closed and a later stage no longer needs it. Each new layer
    then forks the conversation's own pool: P2r goes when P2f is built, and
    P2f when P3s is. Live, same model and context: three full council turns
    in 115, 66 and 54 s, with 0 refusals, 0 errors, no model expiry, and six
    early releases. The cost is re-reading the plan and the findings
    once per stage. A non-recurrent model keeps the whole tree
    (`TestARecurrentModelReleasesEachStageItHasFinished`,
    `TestACouncilOnPolyKVBuildsItsTreeOnce`).
- **Also fixed on the way:** bug-117. A model swap waited about 9 s on
  free-memory refreshes that could never finish; it now takes about 1 s, and
  the log has no false ERROR.
- **Stale wording corrected:** `compact_at` is the share of the granted window,
  not a "session pressure", in the schema comments, the `tweak` help and the
  validation error. The plan's pressure section gains an as-built note.
- **Desktop: option C, built.** The app served a council tag with no change,
  with the deliberation in the Thinking panel. But its Think button exists
  only for `gpt-oss` and `deepseek-v3.1`, and its backend never sends
  `think:false`, so nothing could hide the deliberation. Now a council gets a
  badge in the model picker and a **Deliberation** toggle in place of the
  think buttons: on by default, kept per browser. Off sends `think:false`,
  which the backend now forwards for a council only (`app/ui/council.go`).
  The UI builds, vitest passes 201 of 201, and the `app/ui` tests pass on
  Linux and compile for Windows. Not yet exercised in the running desktop
  app.

## Phase 6 — PolyKV sizing and pressure-driven compaction (built 2026-09-26)

The owner's direction (2026-09-26), after the MTP IQ2_M failed to load at
131k:

1. On opencoti **with PolyKV**, the engine has one unified KV pool. A council
   needs one session. Its roles run as sub-sessions on pools forked from it.
   Common role instructions belong in the shared prefix. Parallel slots are
   not sized up front: opencoti's elastic slots grow on demand.
2. `num_ctx` reserves that many cells for the session. A request without it
   takes the whole pool (`session_ctx_max`). New behaviour: under PolyKV,
   `num_ctx 0` sends no `num_ctx` and lets the engine size the pool.
   Otherwise xollama keeps upstream's default, 32k on a 24 GB card.
3. KV cache K f16, V q8_0.
4. No `OLLAMA_NUM_PARALLEL` with opencoti. Parallelism, where wanted, comes
   from the model's xollama config.
5. Compaction follows the owner session's context **pressure**: compact at
   0.85 during a turn, and at 0.75 while idle, after the answer and before
   the next message, so the next turn starts short. This is the integration
   guide's model (`/shared/dev/docs/cerebriline-polykv-integration.md` §2.7,
   §6.5).
6. **The split stays.** Stock llama.cpp, and opencoti without PolyKV, keep
   today's behaviour byte for byte. Everything here applies only where the
   runner serves PolyKV and the council's `polykv` is not `off`.

**Measured first (b111, 3090, MTP IQ2_M, `num_ctx 131072`).**

- The 131k load failure came from the test script's `XOLLAMA_NUM_PARALLEL=4`.
  xollama launches `-c = num_ctx × parallel` (`llm/llama_server.go`), so the
  pool was 524,288 cells. The compute buffers alone (main and MTP context)
  were 4.6 GiB each.
- Without the override, with `kv {k: f16, v: q8_0}`, the launch is
  `-c 131072 -np 1 --max-parallel 4 --kv-unified --polykv-max-pools 4`.
  Compute buffers are 0.45–1.5 GiB, 10.4 GB of VRAM is in use, and a full
  council turn finishes in 52 s (5,501 prompt tokens, 4,627 cached, 1,962
  generated, MTP drafting on). There were no refusals.
- The fit estimate omits the MTP context's own recurrent-state cells. This
  was reported to opencoti (#349). It no longer blocks this model.

**What exists already.** Dynamic slots (`slots.max`, `--max-parallel`) and
per-model `kv.k`/`kv.v`. The council's owner session, its pool tree and its
sub-session workers match point 1. `begin` reads the owner's `/kv` row.

**Proposed changes (PolyKV path only):**

- **Trigger.** Compact when the owner's `/kv` `allocations[].pressure` is at or
  above `compact_at` (0.85), in place of the rendered-token budget.
  The token budget stays for the non-PolyKV paths.
- **Idle compaction.** After the answer, if the pressure is at or above a new
  `council.context.idle_compact_at` (default 0.75), summarise in the
  background and cache the summary. Then warm the owner session with the
  compacted prefix, so the next message finds it built. The next turn uses a
  ready summary whenever one exists for exactly its old turns.
- **`num_ctx 0`.** Under PolyKV only: send no `num_ctx` (the owner's
  placement included) and launch `-c 0` (the engine sizes the pool from the
  model). Open: whether the council's owner may then book a held window at
  all. The guide's rule 2 says pools need an owner allocation, and an
  allocation without `num_ctx` is per-request.
- **Parallel.** A per-model `slots.live` (the boot slot count), so no
  environment variable is needed; `slots.max` exists.
- **Docs.** The three paths side by side: what each sends and launches.

**Approved (2026-09-26), with two additions from the owner:** a role's think
default is a 2048-token budget, and the thinking mode and budget stay
configurable per role. `num_ctx 0` builds **unowned pools** (opencoti patch
0406, `pool_unowned_v1`).

**Built (2026-09-26, unit-tested; live test on the next promoted build):**

- **Think.** `council.<role>.think: on` is `DefaultCouncilThinkBudget` (2048)
  tokens; a number is a token budget; a level stays a share of the window.
- **Trigger.** `compactConversation` compacts on the owner's raw pressure at
  `compact_at`, then on a ready idle summary at `idle_compact_at`, then (no
  pressure reading) on the token budget. Guard:
  `TestATurnCompactsOnTheOwnersPressure`.
- **Idle compaction.** `council.context.idle_compact_at` (default 0.75, not
  above `compact_at`; tweak `--council-idle-compact-at`). After the answer,
  `councilIdleCompact` reads `/kv` and summarises the old turns into the
  cache (`singleflight`); the next turn takes it. Guard:
  `TestAnIdleCouncilSummarisesForTheNextMessage`. **Not built:** warming the
  owner session with the compacted prefix. The next turn builds P1 from the
  summary itself; warm it if the live test shows the first token waiting on it.
- **`num_ctx 0`** (hook `polykv-window`, `llm/engine_window.go`). Under PolyKV
  only, a stated 0 becomes a whole-pool mark before upstream clamps it to 4.
  The launch resolves it to the model's **trained context**, not `-c 0`:
  xollama's memory estimate and VRAM fit need a number, and opencoti's
  `-c 0` means the same thing. With `pool_unowned_v1` the council's tree is
  unowned: pools are created with `"unowned": true`, the planner sends no
  `num_ctx`, and the owner is never resized. Without the feature the owner
  books the loaded context, as before. Guards: `llm/engine_window_test.go`,
  `TestNumCtxZeroBuildsUnownedPools`,
  `TestAnUnownedCouncilShrinksNothingUnderPressure`,
  `TestTheOwnerKeepsItsPoolsWithoutUnownedPools`.
- **Parallel.** `slots.live` (hook `slots-live`, `server/slots_live.go`)
  replaces `OLLAMA_NUM_PARALLEL` only when opencoti serves the load. It must
  not exceed `slots.max`. Guard: `TestLiveSlotsBindOnlyOnOpencoti`.
- **Split.** Stock llama.cpp and opencoti without pools: `WantsWholePool` is
  false, `liveSlots` returns the server's count, and the tree is nil, so
  nothing changes.
- opencoti answered #349 (#350): the fit bug is real, but it is the main
  context's rolling-KV two-pass re-size ignoring its recurrent-state cells,
  not the MTP context. The fix is patch 0408 (`rs-window-reserve`), due in
  the next dev build after the one on bs2.

**Live on b128, 2026-09-26** (opencoti dev build `2609261427001`, DSO
`d8e69a48`, which carries the #349 fix and advertises `pool_unowned_v1`; a
local dev build, so the pin stays on b111):

| model | launch | VRAM | turn |
|---|---|---|---|
| `omni-council-wholepool` (`PARAMETER num_ctx 0`) | `-c 262144 -np 1 --max-parallel 4` (the trained context) | 22.9 GB | 74 s |
| `omni-council-live4` (`slots.live: 4`, 131k) | `-c 524288 -np 4`, the launch that failed in #349 | 23.0 GB | 86 s (34 s load) |

The whole-pool turn built its pools with `owner ''`, as the engine logged:
`created pool 0 … owner ''` and `forked pool 1 from 0 … owner ''`. They were
released newest first. Members decoded at 25–38 tok/s on the 4-slot launch.

### The conversation held once (2026-09-26)

Scripting the idle-compaction test showed a design flaw. The planner's
session held the conversation in its own cells, and P1 was a second copy
built from tokens and charged to the same owner. The owner's tree was full at
about 45 % of the conversation's window, so compaction at 0.85 and idle
compaction at 0.75 could never fire. Once pools were refused, unpooled
members queued behind an owner that held everything, and the turn failed with
a 503 after two minutes. The owner chose to fix it now (guide §6.2, arm C):
build the root first and run the planner attached to it.

- **The root.** `buildRoot` (`server/council_polykv.go`) builds the
  conversation's root pool before the planner's first call: the rendered
  conversation up to where a decision, a plan and a direct answer part. The
  planner attaches to it with `pool_id`, so only its own tokens are private.
  Workers' layers fork it as before.
- **First turn.** The owner has no allocation until the planner's first
  request, so the root is **unowned**. That needs `pool_unowned_v1`; without
  it the first turn builds no root, as before. The planner's floor comes down
  to its own part (`max(4096, reserve)`), because the conversation sits
  outside its window. The root goes with the turn. While the council is idle,
  `promoteRoot` builds the owner its own copy. The next message waits for
  that to finish (`councilRoots.wait`): built beside the turn's own, the two
  filled the window and the planner was refused, measured.
- **Later turns.** An owned root is kept (`councilRoots`, by owner session)
  and is adopted only while the owner's allocation lives, and only on the
  runner that built it. The next turn forks it and prefills only what is
  new. The engine never releases a pool with a child, so the old root stays
  as the parent; each pool is charged only its own part. After
  `councilRootChain` (2) forks, or on a recurrent model (every pool holds a
  state cell), the root is rebuilt from tokens, and the old chain is let go
  first, newest first. The launch reserves two more pool seats for the chain
  (`councilPoolSeats`: 6 for one round).
- **The summary is a turn of the conversation.** `summariseOld` asks for it
  as the next turn of the conversation itself (head plus
  `councilSummaryPrompt`), on the owner and attached to the kept root. The
  old prompt pasted the old turns into a new prompt, which was a third copy.
- **"Compact the session".** A root refused because the owner's allocation
  is full is `llm.ErrSessionFull` (the engine's 503 names it). The turn
  compacts and builds the root again, once.
- Guards: `TestThePlannerAttachesTheConversationRoot`,
  `TestTheNextTurnExtendsTheKeptRoot`,
  `TestARecurrentModelRebuildsTheRootEachTurn`,
  `TestAStaleRootIsNeitherUsedNorReleased`,
  `TestARefusedRootCompactsAndRetries`,
  `TestTheIdleCouncilGivesTheOwnerItsRoot`,
  `TestTheNextTurnWaitsForTheOwnersRoot`,
  `TestKeepingARootReleasesTheOneItDisplaces`,
  `TestAFullSessionRefusalIsErrSessionFull`. Mutation-checked with compiling
  mutations. The one equivalent mutant: the window test in `promoteRoot`,
  since the engine lists an allocation only with a window.

**Idle compaction, live on b128** (`omni-council-idle`, 16k, a conversation
of about 7,000 tokens over 22 earlier turns; `council-idle.py`; each arm has
its own session, closed after it):

| arm | turn N | idle summary | next message: first token | next message: turn |
|---|---|---|---|---|
| idle (waits for the summary) | 75.7 s | written 14.1 s after the answer | **2.5 s** | 47.1 s |
| no idle (sends at once) | 61.1 s | — | 18.3 s | 64.5 s |

After turn N the promoted root put the owner at pressure 0.867, so both arms
compacted the next message on it, the idle arm using the summary it already
had. Calibration, before the waiting was added: 4,000 and 7,000 tokens of
history ran in 51–61 s. At 10,000 tokens the first turn's root did not fit
beside the window, and the planner held its own copy, as before (57 s).
The load's pool is one conversation wide at 16k: a second conversation
cannot book while the first holds its window. The script closes each session
through `/api/engine` (`sessions/{id}/close`).

## Phase 7 — roles on other models and other instances (built 2026-09-26)

The owner's direction: a role may run on another model **and on another
ollama instance**, so a council can seat a cloud model in a role, or offload
a role to a different kind of model or to another machine on the network.

**Decided (the owner, 2026-09-26):**

1. A failed remote member does not fail the turn when it is a researcher or
   a critic: the council's own model answers in its place. The planner and
   the synthesizer are one of a kind, and their failure is the turn's.
2. A literal URL in the model is fine. An internal address in a published
   model is little risk, and the likely use is cloud models, served by the
   same xollama.
3. No schema question: v4 exists only on `dev` (`main` and
   `v0.34.2-xollama.1` are v3), so `host` joins v4.

**Built:**

- **Cloud models needed nothing new.** `council.<role>.model: …:cloud` is an
  in-process `ChatHandler` turn, which takes upstream's cloud proxy.
- `council.<role>.host` (schema v4, `types/xollama/council.go`): an http(s)
  URL, which **needs `model`**. The council's own name on another xollama
  could be a council too, and would convene there. Tweak
  `--council-<role>-host`.
- `server/council_remote.go`: the member is `/api/chat` on that server
  through `api.Client`, streaming. It carries the same think budget, reply
  cap, seed and temperature as a local member, but no session, placement or
  pool. Its counts join the turn's metrics.
- **Operator allow-list, `XOLLAMA_COUNCIL_HOSTS`** (host or host:port, comma
  separated, `*`; default none). This is not about leaking an address. A
  council model can be *pulled*, and a host it names would receive every
  conversation the council serves. A host that is not listed is never
  called; a researcher or critic there falls back, and a planner or
  synthesizer fails, naming the variable.
- **Fallback** in `internal/council` `call` (`fallsBack`): researchers and
  critics on another model or host, only while the turn is live. The
  deliberation notes the fallback.
- Guards (each fails under a mutation):
  - `TestAFailedRemoteMemberFallsBackOnlyWhereItIsOneOfSeveral` and
    `TestACanceledTurnDoesNotFallBack`;
  - `TestARoleOnAnAllowedHostIsServedThere`: a stub ollama over HTTP, with
    no session sent;
  - `TestAHostTheOperatorHasNotAllowedIsNeverCalled` and
    `TestCouncilHostAllowed`;
  - the schema cases in `TestValidateCouncil`.

**Live, 2026-09-26 (dev `22434`, b111, `gemma4:31b-cloud`, 3090).** Same
hard question ("10 GB sort on 16 GB: quicksort or mergesort?"):

| model | roles on cloud | turn | first token |
|---|---|---|---|
| `omni-council-cloud-research` | researchers | 46.0 s | 11.1 s |
| `omni-council-cloud-critic` | critics | 48.8 s | 14.1 s |
| `omni-council-cloud-ends` | planner, synthesizer | 39.0 s | 11.2 s |
| `omni-council-cloud-all` | all four | 17.1 s | 10.7 s |
| `omni-council-cloud-think` | researchers, thinking on | 54.1 s → 47.2 s after the fix | 11.1 s |
| `omni-council-cloud-fallback` | researchers on a missing model | 59.0 s, 9 members, fallback noted | 10.9 s |

`omni-council-think` (all roles `think: on`, local) on "Can you give me some
help with math?": **2 min 11 s**. On the build before the 2048 default the
same turn ran each researcher to its 32,768-token cap (32,889 and 32,986
tokens at about 40 tok/s, 13.5 and 14 min); the owner took that for a hang.

**Found live and fixed:** ollama.com refuses a numeric think ("think must be
a boolean or string; supported values: [false,true]"), so a thinking role on
a cloud model failed. The owner's rule: check whether the model is a cloud
one, by what the server reports, as cerebriline does. Then check whether
another host is xollama or ollama, and never send an unsupported field.
`councilTakesBudget` sends a token budget only to this server's own models
and to a model another xollama serves itself (`api.IsXollama` + `/api/show`,
cached 5 min). Otherwise it sends `think: true`, with `num_predict` bounding
it.

**Live on another server, 2026-09-26 (eleven2go, researchers `qwen3:8b`).**
eleven2go runs the mann1x/ollama think-budget fork on `:11434`
(`0.34.2-1-thinkbudget`, no `/api/xollama`, so treated as stock) and
`0.34.2-xollama.1` on `127.0.0.1:22434`, reached through an SSH tunnel. The
host allow-list was `eleven2go,127.0.0.1:22435`. After the fixes below:

| researchers on | sent | turn |
|---|---|---|
| eleven2go ollama `:11434`, think on | `think=true` | 56 s |
| eleven2go xollama, think on | `think=2048` | 66 s |
| eleven2go xollama, `gemma4:31b-cloud`, think on | `think=true` | 53 s |
| eleven2go xollama, tunnel killed mid-reply | one "unexpected EOF", fell back | 58 s, 8 members |

**Three bugs found live, each fixed with a guard that fails under a mutation:**

1. A cloud reference on another xollama was sent a token budget and refused
   (400). Its `/api/show` is answered by ollama.com with no `remote_host`.
   Now a cloud reference is cloud by name everywhere, and `/api/show` decides
   only for pulled tags. Guard: the "cloud reference" case of
   `TestAThinkingMemberGetsABudgetOnlyWhereOneIsUnderstood`.
2. A connection dropped mid-reply ended the stream with no error. The member
   "succeeded" with a fragment, and the turn went on with no research. Now
   `remote()` requires the `done` line. Guard:
   `TestARemoteReplyThatStopsShortFallsBack`.
3. A role on another model had its `think` cleared to nil. Upstream reads nil
   on a thinking model as true, and `qwen3:8b` spent its whole 384-token cap
   reasoning, so its content was empty. Measured directly: 1,564 characters of
   thinking, 0 of content. Now `think: false` is kept. As a safety net, the
   runner treats an empty reply from a member elsewhere as a failure. Guards:
   the `think` assertion in `TestARoleOnAnAllowedHostIsServedThere`, and
   `TestAnEmptyReplyFromElsewhereFallsBack`.

**Not done:** the think-budget fork on `:11434` could take a token budget,
but it cannot be told from stock ollama: it has no `/api/xollama`, and its
version string does not name it. It gets `think: true`.

**Left for the live test:**
- a host taken down mid-turn, to check that the fallback note reads well in
  the CLI and the desktop app.

## Phase 8 — Cerebriline's council compaction, ported (approved and built 2026-09-26)

**Why.** The owner asked whether the council's compaction was based on
Cerebriline's agentic council compaction. It was not: only the 0.85 `/kv`
pressure trigger came from the guide (§6.5). Everything else was invented:
the last three messages kept word for word, and one summary of at most 1024
tokens appended to the **system** message. Reading the code also showed four
faults:

- **It forgets.** The client resends the full history each turn and nothing
  records a compaction, so the council likely flips between compacted and
  full turns. Read from the code; not yet measured.
- **It is not incremental.** Each compaction re-summarises all the old turns
  from the raw history.
- **It is sized on the engine's grant, with a wrong budget.** The token
  budget used max(grant, floor), which came to more than the grant.
- **It breaks the cached prefix.** Writing into the system message changes
  the root's first bytes.

**Source.** Cerebriline, `/shared/dev/cline/sdk/packages/core/src/extensions/context/`
(`compaction.ts`, `compaction-shared.ts`, `agentic-compaction.ts`,
`council-compaction.ts`, `continuation-compaction.ts`, `replay-compaction.ts`,
`full-compaction.ts`, `basic-compaction.ts`, `kv-pressure.ts`) and guide
§2.7, §6.5, §11 e, §11 i, §11 j. Numbers below are Cerebriline's defaults,
cited there. A few names mislead in its own sources:

- `DEFAULT_TARGET_RATIO` (0.7) is unused. The live target is
  `COMPACTION_TARGET_CONTENT_SHARE` = 0.25.
- The critics review halves of the *summary*, not of the transcript, whatever
  the comments say.

**What is ported.**

0. **The window** (`resolveGrantedContextWindow`, guide §6.5). This is the
   window the engine granted the owner when it is smaller than the
   conversation's `num_ctx`, and `num_ctx` otherwise. The owner chose this
   basis on 2026-09-26, over `num_ctx` alone. Every size below reads it.
1. **Trigger** (`resolveCompactionTriggerTokens`). Usable input = 0.9 ×
   window. The trigger is `min(0.9 × usable, max(window − output room,
   0.5 × window))`. The output room is the council turn's reserve
   (`councilReserve`), because a council's replies are known caps rather
   than observed outputs.

   Any one of these signals compacts:
   - the applied conversation's tokens reach the trigger, counted by the
     engine's tokenizer as the members send them;
   - on PolyKV, the owner's raw `/kv` pressure is ≥ 0.85 (`polykvSaysCompact`);
   - a root refused with "compact the session" (`llm.ErrSessionFull`,
     Cerebriline's `contextOverflow`);
   - under refusals on the server, the conversation is ≥ 1.25 × what
     compaction would leave **and** compacting lets the owner's booking
     shrink by ≥ 25 % (`kvPressureCompaction`). This joins the existing
     shrink in `finish`.

   Pressure only adds reasons to compact; it never vetoes the arithmetic.
2. **Target** (`resolveMessageTargetTokens`). The messages may use 0.25 ×
   (usable − overhead) after a fold, where the overhead is the system
   message (the model's and the charter's).
3. **The kept tail** (`findCutPlan`, `resolveRecencyBounds`).
   - Walk back from the newest message. Stop at the target, or at
     `preserveRecent` = round(20,000 × (window/128k)^(2/3)), capped at
     0.6 × target, once at least 25 % of the messages are kept.
   - Cut only at the start of a user turn.
   - If the last turn alone is over 0.66 of the budget, pin its prompt and
     keep half of what follows it (`planPinnedCut`).
   - A cut must fold at least one message newer than the last summary.
4. **What replaces the folded turns** (`buildSummaryMessage`). One
   **user-role** message at the front of the messages, after the system
   message, which is never changed. Its parts:
   - every user request so far, quoted verbatim in `<user_request>` blocks
     (0.15 of the target in characters, 200–2,000 per request, never evicted);
   - the retrospective, when there is one;
   - `Context summary:` followed by the replay.

   The kept tail follows it. A chat council has no tools, so there is no
   tool ledger and no `## Files` section.
5. **The pipeline** (`runAgenticCompaction`, `runCouncilReview`), each call
   a council member:
   - **Writer.** The conversation's next turn, on the owner attached to the
     root (§11 i), with the thinking setting of the council's planner. It
     gets the applied conversation plus the replay instruction
     (`DEFAULT_REPLAY_COMPACTION_PROMPT`, adapted from agent work to a
     conversation, plus `DEFAULT_COUNCIL_WRITER_PROMPT`: one `<<<HALFWAY>>>`
     line). The instruction names the span to replay by its first words
     (`describeReplaySpan`), not by pasting it. Up to 3 attempts; an
     over-budget answer is retried with its measured size.
   - **Retrospective** (`generateThinkingSummary`). It reads the folded
     turns' reasoning, where the client sent `thinking`, plus the previous
     retrospective, and is skipped when there is none. Thinking off. It can
     never fail the compaction.
   - **Two critics**, in parallel. They run on P′, the root forked after the
     writer's turn, as workers of the owner (§11 j). Each returns its own
     half of the replay, revised against the conversation, within ±10 % of
     its length (`DEFAULT_COUNCIL_CRITIC_PROMPT`). The split is at
     `<<<HALFWAY>>>` when it falls within 30–70 %, else the nearest blank
     line; with no split the review is skipped. A failed or empty half is
     kept as written.
   - **Synthesizer.** It joins the halves and revises the retrospective
     (`## Replay`, `## Retrospective`), up to 1.1 × the original length. A
     merge under 0.5 × the original is rejected and the writer's replay
     used instead.
   - **Budgets** (`COMPACTION_BUDGET_LADDER` by generation: 0.33, 0.40,
     0.45, 0.50, then 0.55). Combined = max(4,096, target × share). Summary
     = max(4,096, 0.7 × combined), the rest is the retrospective's; critics
     and synthesizer are capped at the summary's budget.
6. **Carried forward.** The server keeps a record per conversation (by
   owner session; memory only, bounded):
   - how many client messages it replaces, and their hash;
   - the summary message;
   - the generation;
   - the user requests;
   - the retrospective.

   Every turn applies it first. A history the client edited or cut drops
   the record, and the raw history is used. A later compaction folds only
   the messages after the summary: the writer sees the previous summary in
   its context, so it is incremental (`createCompactionStateAwarePrepareTurn`).
   The critics review only the newly folded span.
7. **Fallbacks** (`compaction.ts:1441-1791`).
   - Without PolyKV (stock llama.cpp, opencoti without pools), the same
     calls run as plain turns of the owner's session, one critic at a
     time.
   - A continuation that fails or writes nothing falls back to the text
     path: the folded turns serialized as `[User]:` / `[Bot]:` for a
     stateless call.
   - A result still over the trigger gets a no-tail rescue with
     `DEFAULT_FULL_COMPACTION_PROMPT` (sections Goal … Next), accepted only
     if smaller.
   - A failed agentic compaction becomes a basic one (no model call). It
     keeps every user prompt and each older turn's final answer where it
     fits.

**What xollama adds.**

- **Idle** (the owner's design, from Phase 6). After an answer, at
  `idle_compact_at` (0.75 of the trigger basis), the whole pipeline runs
  while the council waits, and the next message applies the record at once.
  Cerebriline compacts only before a request.
- **Pools.** The writer is on the owner attached to the kept root, and
  P′ is a fork of it; after the fold, the root is rebuilt from the
  compacted conversation. The system message no longer changes, so the
  system part of the root survives a fold.

**Settings.** `council.context` gains `compaction` (`agentic` or `basic`),
`review` (the critics and synthesizer, on) and `retrospective` (on). The
schema stays v4, which is unreleased. `compact_at` and `idle_compact_at` keep
their names.

**Built** (2026-09-26): `server/council_compaction.go`,
`server/council_compaction_prompts.go`; the Phase 6 compaction in
`server/council_polykv.go` is gone. Three faults found while building it,
each now held by a test:

- **An unowned tree's grant is not a window.** With `num_ctx 0` the engine
  books each request, and the grant read back is that request's (2 in the
  fake). Taken as the window, it made the idle fold fire and block on a dead
  scheduler while holding the promotion mark. `window()` reads the grant only
  on an owned tree.
- **"Did it fold" is the record changing.** The refused-root retry compared
  message counts; folding one message and adding the summary keeps the count,
  so the retry never ran.
- **A fold can grow the conversation.** Quoting a short span's requests and
  replaying them outweighed the span (589 → 623 tokens). Cerebriline's rescue
  accepts only a smaller result; the port discards any fold that does not
  shrink.
- The idle fold re-learns the grant, since the first turn's booking is made
  by its planner's first call.

**Tests** (`server/council_compaction_test.go`, and `council_polykv_test.go`
rewritten for the port), all under `-race`:

- the record carried over three turns with one writer;
- an edited history drops the record;
- an incremental second fold, whose record still covers the first;
- the summary leads and the system message stays;
- the sizes table-tested against Cerebriline's numbers at three windows;
- the cut: recency, a quarter of the messages kept, the question kept with
  its answer, the pinned last turn, nothing newer than the summary, the no-tail
  form, the tool boundary;
- the split, the section parser, the replay cleaner, the merge guard;
- each fallback (text path, basic);
- the settings (review off, basic);
- the retrospective reads the folded reasoning and touches no pool;
- the refusal trigger;
- a fold that does not shrink is discarded;
- the grant sizes the compaction when it is below `num_ctx`;
- the idle fold holds the next turn;
- the writer marks the half only for a review;
- a refused root that no fold relieves is not retried.

**Mutation-checked**: 28 compiling mutants, every one caught but one. The
survivor, the idle fold's own `learnGrant`, is equivalent while `promoteRoot`
runs first on the same goroutine, which re-learns it; it stays for the path
where `promoteRoot` returns early.

**Live, first try (b128, 2026-09-26)** — `council-turns.py`, one
conversation over six turns at `num_ctx` 16384, starting at ~9.5k tokens.
Turn 1 answered in 78.7 s (first token at 20 s). The idle fold then failed and
held turn 2:

- The owner's grant was 6,656: what turn 1's unowned root (~9.7k cells) left
  of the 16,384-cell pool.
- The writer, a continuation of the whole conversation, sent 11,908 tokens to
  that window and was refused three times ("exceeds the available context
  size").
- The text path sent the whole transcript in one sessionless request (14,244
  tokens); it could not be admitted beside the owner's booking and waited out
  the 2-minute admission budget while holding the promotion mark.

Fixed (bug-134): the writer is asked only when the measured conversation, its
instruction and its reply fit the window; the text path cuts the transcript
into pieces that fit, each piece's summary carried into the next, and a
summary written in pieces is not reviewed (a reviewer would see only the last
piece). Cerebriline's 4,096-token budget floor is scaled to at most window/8,
so it is unchanged from 32k up; on a small window it left no room for the
text itself. Held by `TestAConversationOverItsGrantCompactsFromTextInPieces`;
the test fixtures grew to realistic proportions (a writer needs the
conversation plus about 1,500 tokens of instruction plus its budget).
Mutation-checked: 6 more mutants, all caught but the `w <= 0` guard in `fits`,
equivalent while `num_ctx` is always known.

**Live, second try (b133)** — turn 1 answered in 75.7 s (first token at
17.6 s); the idle fold took the text path in pieces and folded 10,399 tokens
to 3,181 in 56.6 s, and turn 2 applied the record. Turn 2's fold then stuck:
by then `begin` had grown the owner back to the whole 16,384-cell pool, and
the text path's calls, on sessions of their own, were booked beside it
(bug-135: "largest admissible 0 < num_ctx_min 4608"). Fixed: on an owned
tree every compaction call that does not read the root (the text path, its
reviewers, the retrospective) runs on the owner, inside its window and with
no pool, and they take turns there, the retrospective first and one critic
at a time, as the plan's fallback already said. Held by
`TestTheTextPathTakesTurnsOnTheOwner`, whose fake engine now records calls
that overlap on one session; 4 more mutants, all caught. The run's pasted
numeric rows also tokenized about four times denser than estimated (a
2,500-"token" paste was ~10k), so the next run pastes 700.

**Live, third try (b133)** — turns 1–4 of 6 (stopped for a fixed binary):

| turn | wall | first token | owner window, used after | fold |
|---|---|---|---|---|
| 1 | 67.2 s | 17.5 s | 6,656, — | idle, text in pieces then the no-tail rescue: 10,190 → 1,679 tokens in 2 min 4 s |
| 2 | 153.9 s | 100.4 s | 16,384, 4,432 | before the turn, from text on a 6,656 window it no longer had (bug-136) |
| 3 | 60.7 s | 10.0 s | 16,384, 8,345 | none |
| 4 | 48.3 s | 15.2 s | 16,384, 13,354 (0.82) | idle fold refused (bug-137) |

Two more faults:

- **bug-136, since Phase 6.** A successful `POST /sessions/{id}/resize`
  answers `window` = the window it replaced and `window_new` = the new one
  (opencoti `oc_alloc_resize_apply`); xollama read `window`, so every grow
  looked like a no-op and the council kept sizing itself on the old grant.
  Turn 2 compacted for a 6,656 window it had just grown to 16,384, and waited
  100 s for its first token. Fixed in `parseResize`; a refusal now also names
  its `error_kind`. `TestResizeAnswers` holds opencoti's real 200 and 409
  bodies, read from its source.
- **bug-137.** The text path on the owner ran beside the kept root, which
  holds the very conversation it replaces: "its pools and workers hold 12197,
  the prompt needs 9962 private". `dropKept` releases the kept root before a
  fold from text; the idle fold rebuilds it from the compacted conversation.
  `TestAFoldFromTextReleasesTheKeptRootFirst`.

**Live, fourth try (b133, bug-134…137 fixed)** — six turns, 9.5k tokens of
history to start, ~2.8k pasted per turn, `num_ctx` 16384:

| turn | wall | first token | owner used after (pressure) | fold |
|---|---|---|---|---|
| 1 | 80.7 s | 17.5 s | — (window 6,656) | idle, 43.6 s |
| 2 | 67.7 s | 7.6 s | 5,738 (0.35) | none — started on the idle summary |
| 3 | 70.8 s | 20.5 s | 11,613 (0.71) | none |
| 4 | 348.9 s | 304.3 s | 4,837 (0.30) | root refused → fold before the turn, 4 min 26 s (bug-138) |
| 5 | 52.8 s | 14.9 s | 8,534 (0.52) | none |
| 6 | 54.7 s | 20.6 s | 12,187 (0.74) | idle, 59.8 s |

Against the third try, turn 2 fell from 153.9 s (first token 100.4 s) to
67.7 s (7.6 s): the idle summary was applied and nothing else ran.

- **bug-138.** Turn 4's root was refused ("compact the session"), and the
  fold's review could not fork the conversation beside the full owner. Its
  critics and synthesizer fell back to bookings of their own, each reading
  the whole conversation, which the engine never admits while the owner
  holds the pool; each waited out the 2-minute admission budget. Fixed:
  `reviewLayer` reports whether it forked, and on an owned tree an
  unforkable review is skipped and the writer's replay stands, as
  Cerebriline keeps a half as written when its review fails.
  `TestAReviewWithNoRoomToForkIsSkipped`; 2 mutants.
- **Open**: the refusal itself. After turn 3 the owner used 11,613 of 16,384
  while the conversation was ~8.3k tokens: the kept root's chain (up to
  `councilRootChain` older roots) holds cells beside the root it feeds. This
  is the first question of the prefix assessment.

**Live, fifth try (b133, bug-134…138 fixed)** — same conversation shape:

| turn | wall | first token | owner used after (pressure) | fold |
|---|---|---|---|---|
| 1 | 72.4 s | 17.2 s | — (window 6,656) | idle, from text in pieces: 10,155 → 1,705 in 2 min 18 s |
| 2 | 57.7 s | 11.2 s | 4,854 (0.30) | none; the window grew back 6,656 → 16,384, read right (bug-136) |
| 3 | 51.0 s | 10.8 s | 8,874 (0.54) | none |
| 4 | 45.0 s | 19.7 s | 12,518 (0.76) | idle, from text: 12,699 → 6,411 in 3 min 8 s |
| 5 | 76.9 s | 34.4 s | 0 | root refused → fold before the turn, review skipped (bug-138), 26 s |
| 6 | 49.5 s | 10.1 s | 8,433 (0.51) | none |

No turn waited out admission. What is left, both open:

- **The writer rarely fits at the trigger on a small window.** At 16,384 the
  trigger is 12,544, and a continuation needs the conversation plus ~2.5–3k
  of instruction plus a 2,048 budget, so every fold here came from text, at
  2–3 minutes while idle. Cerebriline's trigger reserves the output room but
  not the compaction's own instruction; the fix is to compact early enough
  for the writer to fit, measured before it changes.
- **bug-139 (open): text calls on the owner leave their prompts as the
  owner's private cells.** Right after the idle fold after turn 4 the owner
  had 1 of 16,384 cells free and no pools, so the fold's own root rebuild was
  refused and turn 5 folded again before it started. Asked opencoti (mail
  #376) for a way to free a session's private cells without releasing its
  window; closing and re-booking would lose the grant.

**Live.** `council-idle.py` over four and more turns on b128: tokens sent
per turn, prefill, time to first token and the summary's size by
generation.

**Compact writer instruction (2026-09-26, measured on b137).** The owner:
adapt Cerebriline's limits where the situation differs, and measure. The writer's
instruction was 2.5–3k tokens: the replay prompt (565), the halfway marker
(156), and a requests block that quoted every request at 200 characters or
more, so it grew each turn. At the 16k trigger that left the writer no room, and
every fold went from text. Now the replay prompt is 247 tokens (same rules and
shapes), the marker 63, and the requests block is replaced in the writer's
instruction by a 35-token note; the summary still opens with the requests,
verbatim. The trigger is capped (`compactionWriterTrigger`) so that the
conversation, the longest writer instruction and its largest budget fit the
window, never below half the window. Tests: `TestTheTriggerLeavesTheWriterRoom`
and `TestTheWriterIsNotSentTheRequests`, each killing its mutants.

A/B on b137 (0412 in), six turns plus a recall question over facts planted in
turns 1–3, HEAD against HEAD with the compact writer:

| | HEAD | compact writer |
|---|---|---|
| Turns / recall | 6/6, 6/6 | 6/6, 6/6 |
| Wall, six turns | 407.6 s | 408.5 s |
| Mean first token | 24.1 s | 19.2 s |
| Fold at 16k | the writer needs 18,261 (instruction 2,822), so text, 41 s; then the root is refused and a second fold follows (turn 5 first token 64.8 s) | the writer fits, pooled, 18.5 s and 26.7 s; no refused root |
| First fold (6,912 grant) | text pieces, 35 s | text pieces twice: the tail replay ended at or over the 3,456 trigger, so it was redone without the tail (2m27, idle) |

The redo in the compact arm is the no-tail rescue, triggered by the replay's
length: the budget (864) and the trigger were the same in both arms. Runs on b133
had both died on the engine's 500 (opencoti 0412), so this is the first
comparison that reached recall.

## Phase 9 — Cerebriline as a client: tools, shared prefix, carried state (proposed 2026-09-26)

**Why.** Cerebriline (`/shared/dev/cline`) is adding xollama as a provider
(mails #366, #367 from the Cerebriline session; answered in #370–#372). It
sends tools on every turn, so today it never reaches the council, and it
drives PolyKV itself against a bare opencoti.

**The owner's decisions (2026-09-26).**

- **Council state travels in-band, to resume.** `council_chat_state`, on
  the response and on the request alike: a protobuf message sealed with a
  key the server keeps, sent as an opaque base64 blob. Its only purpose is
  to resume the council after a reconnection (the owner, mail #379), so it
  is emitted at every checkpoint where the council could resume (after the
  route, the plan, each research round, the critics, and at a tool-call
  suspend), each on its own chunk, not only on the done chunk. It holds the
  compaction record and the deliberation in flight, bound to a hash of the
  history and the user turn it was made for. Same history and the same
  retried user turn: skip the spans it records as complete and continue.
  A different last user turn: drop the deliberation, keep the folded
  history. No blob, a bad seal, an unknown version or a history it does
  not prefix: a fresh start (or the server's memory), never an error. No
  deliberation replay: the client never sends the council's thinking back.
  No snapshot endpoint.
- **The council compacts.** Cerebriline turns its own compaction off for a
  council model and sends the history as the user sees it.
- **PolyKV driven by the client** on plain turns, as against a bare
  opencoti: the `/api/engine` pool proxy, plus a client `placement`
  (`pool_id`, `num_ctx`, `num_ctx_min`) on `/api/chat`. The client's root is
  P0, and a council turn that names it forks from it and never releases it.
- **Tools on council turns.** Every role sees the tools and MCP schemas.
  Researchers and critics get read-only tools; only the synthesizer may call
  a writing tool (MCP `readOnlyHint`, and a per-tool mark from the client
  for its built-ins). A member's tool call is forwarded to the client: the
  response ends with `done_reason` `tool_calls`, the deliberation in flight
  is suspended into the blob, and the next request (the results plus the
  blob) resumes it. Parallel members' calls are gathered at a checkpoint and
  sent as one message; each call id names its member. The `len(req.Tools)`
  bypass goes once this works; `format` stays a bypass.
- **One prompt layout, on every council turn.** Every member's system
  message is empty; the tools follow; then the conversation. That is the
  shared P every member attaches to. Each role's instruction is a user
  message after P, with the charter folded into it. The synthesizer alone
  also gets the client's system prompt, when one was sent, as a user message
  right after its own role prompt, so it knows both its role and what the
  user expects. This also answers the owner's request to use the PolyKV
  prefix better, and it is re-measured against the Phase 0 layout on b133
  before it replaces it.
- **Deliberation tags.** `council: {role, index, round}` on each thinking
  chunk and on a forwarded tool call. Content is the final answer (the
  synthesizer's, or the planner's on a direct turn) and carries no tag.
- **Detection.** `/api/xollama` gains `features: [...]`, named per feature
  as it ships; the state is `council_chat_state_v1`.

**Order.** Features list and tags; client placement; the shared-P layout,
measured; the sealed state; tools with suspend and resume.

**9.1 built (2026-09-26): features list and tags.** `/api/xollama` answers
`features: ["council", "council_compaction_v1", "council_tags_v1"]`
(`api/xollama_identity.go`, `xollamaFeatures` in `server/identity.go`); a
name never changes meaning, a changed contract is a new name. Every thinking
chunk of a council turn carries `council: {role, index, round}` (counted from
0; `api.CouncilTag`, the `council` hook in `api/types.go`), and holds one
member only: `thinkingTags` releases per-member segments. The headings stay in
the text for clients that do not read the tag. Content and the done chunk carry
none. Agreed with the Cerebriline session in mails #379–#384. Guarded by
`TestEveryThinkingChunkNamesItsMember`, `TestParallelMembersReadOneAtATime`
and `TestTheIdentityNamesTheFeatures`; two compiling mutants (no tag, the
speaker's tag instead of the floor's) each fail a test.

**9.2 built (2026-09-26): client placement.** `placement {pool_id, num_ctx,
num_ctx_min}` on `/api/chat` (`api.Placement`, `client_placement_v1`). A plain
turn hands it to the engine (`clientPlacement`, through the member's key, so
`placementFields` still drops it off opencoti). A council turn reads only
`pool_id`: `createRoot` forks the conversation root from the client's pool,
and falls back to a root of its own when the engine refuses the fork. The
council never releases the client's pool, and releases rather than extends a
kept root built under a different named pool, so the client can release its
old one. The fake engine now enforces the contiguous-prefix contract on every
fork. Tests: `TestAPlainTurnCarriesTheClientsPlacement`,
`TestACouncilRootStandsOnTheClientsPool`,
`TestAClientPoolThatDoesNotMatchIsLeftAlone` and
`TestANewClientPoolLetsTheOldRootGo`. Three compiling mutants (no fork, no
release on a new pool, placement dropped) each fail a test.
Live on b133:
- **Plain turn.** A pool of the system prompt (285 tokens, via `/api/engine`
  apply-template + `polykv/pools`), then a chat naming it on a fresh session:
  285 of 316 prompt tokens came from the pool. Needs the model's engine to
  have pools (`XOLLAMA_SESSION_POOL=1`; without them the pool create answers
  400 "pools are disabled").
- **The render must match exactly.** A trailing space in the system prompt,
  which the chat path trims and apply-template keeps, left 283 of 286 tokens
  matching, and a hybrid model shares only the whole pool, so nothing was
  shared.
- **Council turn** naming a pool its prompt does not start with: the engine
  answered 400 "child prefix shorter than branch_pos", the council built its
  own root and answered, and the client's pool was untouched.
- **On a council model** the client's pool must belong to the conversation's
  session. An unowned one found only 256 unbooked cells.

**Render (`chat_render_v1`).** Cerebriline cuts P0 from the server's
rendering (mail #390), and opencoti's `/apply-template` is not xollama's
renderer when a model renders through its `TEMPLATE`. Upstream's
`_debug_render_only` on `/api/chat` already answers the exact prompt on both
paths. The one change: `councilServes` lets a render-only request through as a
plain turn, so a council model renders what its members send and convenes
nobody. Guarded by `TestARenderIsWhatTheEngineGets` (render == the prompt the
engine receives; a council render makes no call), and the mutant without the
condition fails it. Live on b133: `[{system:""},{user:SENTINEL}]` renders
identically on the plain and the council model, and no council turn ran.

Sharing on council turns depends on the layout. Today the charter is inside
the system message, so a client's P0 of the system prompt cannot be a prefix.
It becomes possible with 9.3 (empty system, then tools), and the question of
what P0 holds went to Cerebriline in mail #389.

**9.3 built (2026-09-26): one shared prefix.** Every member sends an empty
system message, then the conversation (`councilConversation`). The charter
opens the planner's route and plan requests, so every later member reads it
through the plan layer. The client's system prompt (else the model's) goes to
the synthesizer after its role prompt and to a direct answer after the
conversation (`Config.System`). `conversationEnd` uses
`council.IsPlannerRequest`. Tests: `TestEveryMemberSharesOnePrefix`,
`TestTheRouteDecisionReadsTheCharter` and `TestEveryMemberStartsFromAnEmptySystem`.
Four compiling mutants each fail one (synthesizer or direct answer without the
system prompt, charter after the role, system kept in the conversation).
Three compaction fixtures were re-sized, since the system message no longer
holds the charter's ~200 words.

A/B on b133 (`council-layout-ab.py`): six storage questions on fresh sessions,
with a fixed history and a client system prompt carrying a checkable format
rule; three follow-ups; each session closed after its question.

| Layout | Council-routed | Council turn wall / first token | Eval | Uncached prompt | Format rule kept |
|---|---|---|---|---|---|
| A (charter + system in the system message) | 6/6 | 26.4 s / 23.0 s | 917 | 363 | 9/9 |
| B (9.3, route request without the charter) | 4/6 | 40.4 s / 36.0 s | 1,575 | 648 | 9/9 |
| B2 (9.3, charter on the route request too; shipped) | 6/6 | 38.6 s / 34.6 s | 1,509 | 846 | 9/9 |

The cost is generation, not prefill. In A the client's "three bullets" rule
sat in every member's system message, so researchers and critics obeyed it
and wrote short: 709/748 chars per researcher, against 1,511/1,628 in B on the
same question. In 9.3 they write to their caps, which is what the owner's
layout intends. B's two direct answers were the route decision losing the
charter's definition of trivial; B2 restores it. The uncached rise (about 480
tokens) is the charter read by both planner requests: on this hybrid model the
slot cannot reuse a partial prefix. The caps (`council.<role>.max_tokens`) set
the time if it matters.

**bug-141 fixed (2026-09-26): unowned roots leaked pool seats.** Found by
the 9.3 A/B. Its single-turn questions closed their sessions while the idle
council was promoting the owner's root. The engine created those roots unowned
(`owner: null`, "holds no live allocation"), xollama kept them for a
conversation that never came back, and the engine never releases an unowned
pool on session close. The result was three pinned orphans with
`orphaned_pin: true`, half of `pools_max` 6. Now `PoolInfo.OwnedBy` reads the
engine's answer: `buildRoot` keeps a root only if owned, and `promoteRoot`
releases one that came back unowned. Test: `TestAnUnownedRootIsNeverKept`
(promotion and turn); each guard's mutant fails it.

**9.4 built (2026-09-26): the sealed state, to resume.** A request carrying
`council_chat_state` (even `""`) turns the state chunks on
(`council_chat_state_v1`). `council.RunFrom` takes a `Progress` (route, plan,
per-round findings and critiques) and calls a checkpoint after the route,
the plan and each member; the server seals that into a chunk of its own, and
the done chunk carries empty progress plus the compaction record
(`server/council_state.go`: protobuf by `protowire`, AES-256-GCM, envelope
version byte + nonce, AAD naming the feature, key 32 bytes at
`<models>/council-state.key` written through `fsowner`). `councilResume` opens
a client's blob: bound to sha256 of the history before the last user message
and of that message; the same turn skips the members it records, a different
last user turn drops the progress; the record is restored when the server has
none or an older one. Anything unreadable is a fresh start. Tests:
`TestACouncilStateSealsAndOpens`, `TestACouncilStateSkipsWhatItDoesNotKnow`,
`TestTheCouncilStateKeyIsKept`, `TestACouncilTurnSendsItsState`,
`TestABrokenOffTurnResumes`, `TestTheStateRestoresALostRecord`,
`TestATurnResumesFromItsProgress`; five compiling mutants each fail one.

The live check found **bug-142**. A turn whose client left hung forever,
and the resumed request waited on it. The scheduler's `processPending` skips a
cancelled pending request without answering it, so a member's in-process
`ChatHandler` never returned, and `councilMembers.Stream` read its pipe until
it did. The turn never ended, its root was never promoted, and the next turn
on the conversation waited in `rootRegistry.wait`. Now the read ends with the
context (`context.AfterFunc` closes the pipe), and the turn's last two sends
are guarded. Upstream's scheduler is untouched: the member's handler goroutine
still waits there, as upstream's own handler does for a client that left.
Guarded by `TestALeftTurnEndsWhileAMemberIsUnanswered`; the mutant without the
`AfterFunc` fails it.

Live on b137 (`council-resume.py 4`): the first request was dropped after 4
states (route, plan, both researchers; 26.8 s). The same request sent back with
the newest state logged "resuming the turn from the client's state". Only the
two critics and the synthesizer ran (members 3). It answered in 28.3 s, first
token at 12.9 s, and sent 3 states.

**9.5 built (2026-09-26): tools on council turns (`council_tools_v1`).**
Tools reach the council only with `council_chat_state`: a member that calls one
is suspended into the state, so for a client without it tools stay the
plain-chat bypass they were. Every member's request carries the client's tools,
and so does the renderer the PolyKV root is cut from, so P holds them.
- **Policy** (`internal/council/tools.go`). Researchers and critics call only
  the tools marked read-only: `x_read_only` or MCP `annotations.readOnlyHint`
  in the function object. They are read into `api.ToolFunction.ReadOnly`,
  which is `json:"-"`, because a marshalled mark would change every rendered
  tool prompt. The synthesizer and a direct answer call any tool. The route and
  plan follow a schema and call nothing. A refused call is answered in place
  and never forwarded; after two refusals the member's text stands.
- **Suspend and resume.** `council.ToolModel.StreamTools` returns a member's
  calls. `RunFrom` runs each step to its end, and a step with calls ends the
  turn there. `Result.Calls` holds every waiting member's calls under
  `MemberKey:id` (`r2:`, `c1:`, `s:`, `d:`, `.2` for later rounds).
  `Progress.Suspended` holds each member's own turns (state field 4, turns as
  JSON). The server sends the calls on one chunk and the state on the done
  chunk (`done_reason` `stop`, as upstream's). A resumed request's traffic after
  the last user message leaves the conversation (`councilToolTurn`); results go
  back by `tool_call_id`, or in order when a result names no call.
- **Prompts on a turn with tools.** A tools paragraph joins the charter
  (`toolCharter`). Researchers and critics are told which tools only read and
  to reply in prose. The synthesizer is told to make the changes, and that the
  plan is not a format to follow.
- **Tests.** Council: `TestParallelMembersToolCallsGoOutTogetherAndComeBack`,
  `TestOnlyTheMemberThatCalledWaits`, `TestOnlyTheSynthesizerWrites`,
  `TestADirectAnswerCallsTools`, `TestResearchersAreToldWhichToolsOnlyRead`,
  `TestAModelWithoutToolsCallsNothing`,
  `TestTheCharterNamesTheToolsOnlyWhenThereAreSome`. Server:
  `TestACouncilTurnCallsToolsAndResumes`,
  `TestACouncilSynthesizerWritesThroughTheClient`,
  `TestToolsWithoutStateStayAPlainChat`,
  `TestAResumedTurnsToolTrafficLeavesTheConversation`,
  `TestAToolTurnsRootHoldsTheTools`, `TestASuspendedMemberTravelsInTheState`.
  API: `TestAToolCarriesItsReadOnlyMark`, `TestTheReadOnlyMarkIsNeverRendered`.
  Twelve compiling mutants each fail a test (four policy, eight server).
  **bug-143**, a data race on `Progress.Suspended` between a step's launch loop
  and a finishing member, was caught by `-race`.

Live on b137 (`council-tools.py`), as a client. Tools: `list_files` and
`read_files`, marked read-only, and `write_file`, over a two-file fake
repository. The question needs both files and one write.
- **The mechanism works.** Parallel members' calls go out together (`r1`+`r2`,
  `c1`+`c2`). Each round trip resumes only the waiting members, in 1.2–5 s.
  Only the synthesizer writes.
- **First prompts.** The charter still said researchers work "using only the
  conversation and their own knowledge". They called nothing and described
  calls they had not made, naming files that do not exist. Critics answered in
  the plan's JSON, and the synthesizer asked leave to write.
- **With the tools paragraph and the role notes**, six runs (5–9 round trips,
  28–46 s): 4/6 had both facts and the write. In the other two, one fact was
  lost in the findings and the synthesizer said it had written without calling
  `write_file`.
- **Baseline: the same model as a plain chat** (no state, thinking off), six
  runs: 6/6 had both facts and the write, in 5–6 s over 5 round trips.

So on a small, easy tool task the council costs about 6× the time and is less
reliable than the plain model. The members' failures are the model's (a
synthesizer claiming a call it did not make). The mechanism shows no fault.
Whether a council should take such tasks at all (route them direct, or bypass
when every step needs tools) is the owner's call.

**9.5 follow-up (2026-09-27): the unreliability was ours.** The owner rejected
"less reliable" as a finding. A per-member debug trace (`council member` at
`OLLAMA_DEBUG=1`: each member's full messages, reply and calls) showed:
- **Compaction never fired**, and both arms ran with thinking off.
- **Information loss.** Each tool result stayed with the member that called
  it. Critics and the synthesizer got only the researchers' prose. So critics
  re-read every file to verify, and the synthesizer, the only writer, never saw
  the file it had to edit. It re-read it, or wrote "I have added the line"
  without calling anything. Now a researcher's or critic's reply carries its
  evidence (each call and result, 4,000 characters per result at most;
  `TestEvidenceIsCappedAndNeverInTheAnswer`). A reply is the member's last
  text, not the narration before each call, and critics call a tool only for
  what the evidence lacks.
- **A contradicting charter.** On a tool turn the built-in charter still said
  researchers use "only the conversation and their own knowledge". That
  sentence now names the tools that only read
  (`TestTheCharterLetsResearchersReadOnlyWithTools`).
- **A resumed member got the wrong PolyKV layer.** `workerPlacement` cut the
  layer before a member's last message, and on a resumed member that is a
  tool result. Each round trip built a private pool per member. Two results in
  a row, which qwen's template renders as one block, gave no prefix at all:
  the member booked its own 2,304 cells beside an owner holding all 16,384,
  and waited out admission (`kv-reservation: REFUSED … base 0/16384 free`).
  The layer is now everything before the member's own instruction
  (`TestAResumedMemberReattachesToItsStage`; the mutant fails it).

The same live A/B after the fixes, 6 council and 6 plain runs interleaved:
council 6/6 fully right in 27–38 s over 4–6 round trips; plain 6/6 in 5–7 s.
Critics made no tool calls, and no member ran unpooled or waited on
admission.

Closed the same night: a worker with no layer on an owned tree now runs on the
owner's session, inside its window, one at a time (`councilTree.onOwner`), as
compaction calls already did. It never books beside an owner holding the whole
pool. An unowned tree keeps its per-request booking, since there is no window
to collide with. Guarded by `TestAWorkerWithNoLayerRunsInsideTheOwner` (every
fork refused; the mutant without the branch fails it). Live: 3 runs, 0
unpooled members, 0 admission waits.

**9.5 follow-up 2 (2026-09-27): narrated calls, and evidence by ref.**
- **Narrated calls.** About 8% of researchers' first replies (6 of 72) described
  a call they had not made, with an invented result. The rendered path was
  checked and is not the cause (qwen3.5 renderer and parser, as the official v4
  manifests; 0 of 467 replies with leftover tool syntax). The researcher note
  now says to call first and report once the results are in. A deterministic
  guard (`narrated`) drops a researcher's first reply that names a callable tool
  without calling one, and asks once more; critics are exempt, since they name
  the tools the findings used (`TestANarratedCallIsNeverAFinding`; both
  mutants fail it). Live 10+10 interleaved (notes scenario): council 10/10 in
  26–44 s, plain 8/10 in 4–7 s (both misses claimed an edit never made). The
  nudge fired 0 times: the note alone removed the narration.
- **`council_evidence`** (the owner's design: "so we don't bloat the
  context"). A result over 1,500 characters travels in the findings as its ref
  (the forwarded id, already a key of the client's results), size and first ten
  lines; any member reads a range or a pattern back with `council_evidence`,
  which the server appends to the client's tools once (`WithEvidence`, same list
  for every member and the root) and answers itself, never forwarding it. A
  member's own older results fold to refs past 12,000 characters
  (`folded`); its newest stay whole. `internal/council/evidence.go`; five tests,
  five compiling mutants each failing one.
- **Live, evidence scenario** (`council-evidence.py`: a 20 KB `build.log` with
  two facts deep inside, an 8 KB `config.py` where one value changes and
  nothing else): every completed run was fully right on both sides, `config.py`
  byte-exact (council 9/9 completed, plain 12/12, 25–43 s plain vs 69–106 s
  council). Critics used `council_evidence` 33 times over the two batches.
  **Three council runs did not complete** (2 before `folded`, 1 after): a member
  whose own results outgrew the owner's window (15,264 cells needed of at most
  15,235) was refused, and admission waited it out for 2 minutes before a 503.
  Open: a member whose newest turn alone fills the window (the last case: three
  files read in one turn), and the admission wait on a request that can never
  fit.
- **Closed 2026-09-27 (owner's go):** a member's own results fold within
  `ResultBudget` (one and a half times the window, in characters; three
  quarters folded a synthesizer's own 16 KB file at 16k, which it then paged
  50 lines at a time): older turns
  first, earlier look-ups dropped, then the last turn's largest results, which
  the member searches by ref (`TestAMemberFoldsItsOwnResults`, three mutants).
  An engine refusal that needs more cells than the whole window returns
  `llm.ErrNeverFits` at once instead of a 2-minute wait
  (`TestARequestThatCanNeverFitIsNotWaitedOut`). The desktop app's Expose now
  sets `XOLLAMA_HOST` (it set only `OLLAMA_HOST`, which xollama ignores).
- **Cerebriline's own pools on plain models (mail #411, answered #414):**
  `session.client_pools` / `XOLLAMA_POLYKV_CLIENT_POOLS` gives a plain model
  engine pool seats for the client (none existed unless xollama pooled or a
  council loaded); a placed native chat feeds no automatic capture.

## Phase 10 — one council across turns, working in parallel (ab-3, 2026-09-27)

ab-3 (harness v2, simple Manic Miner fix, `omni-council-idle`, b145) was the
first valid council/plain pair: both fixed it, council 1058 s / 33 trips vs
plain 170 s / 26. Tool mechanics were clean (0 errors). What cost the time:

- **Nothing ran in parallel.** The model launched `-c 16384 -np 1
  --max-parallel 4`. Workers are charged to the owner's window and were
  admitted (`base need 0`), but waited for a SLOT: `no slot is available,
  defer task` until researcher 1 released slot 0 (opencoti #501). The elastic
  controller never grew live 1→2: it needs 3 s of saturation and 512 MiB free
  VRAM, which a fitted load rarely has.
- **The council ran three times.** Twice the synthesizer ended the turn
  without the change the task needed; the harness (which nudges only when a
  turn made no tool call and the game still fails) sent "still not working,
  continue", and each such user message started a new deliberation from
  scratch: route, plan, researchers, critic.
- One of two critics did the work; identical reads by two members in one
  step; a located error (R17) edited only at R21.

The owner's rulings (2026-09-27): one critic by default; parallel members
really parallel; share a read only with a member that asks for it; the
synthesizer starts as soon as a critic confirms a located error; a broadcast
channel between parallel members, to be measured and dropped if it does not
pay; and **the council behind a council chat stays alive**: the next user
message is feedback to the same council (HITL), and the planner decides
whether the work is done, goes on from where it is, or needs the council
again. A council is a chat model for any task, not a coding agent: the
request's system and user messages drive it.

**Built (2026-09-27):**
- 10.1 A slot per parallel member: `-np` = the widest local step, `-c`
  unchanged, `--kv-unified` forced (`llm/engine_council_slots.go`).
- 10.2 Default 2 researchers, 1 critic.
- 10.3 Shared reads (`internal/council/reads.go`).
- 10.4 `VERDICT: CONFIRMED path:line` from a critic.

- 10.5 The council carried across turns (built 2026-09-27): the planner
  routes a follow-up `direct` / `continue` / `council`; continue runs the
  synthesizer on the kept plan and last round; kept per session and in the
  sealed state (fields 6-8), bound to the conversation it answered.

- 10.6 The broadcast channel (built 2026-09-27, behind `council.broadcast`,
  default off): a server-answered `council_post` tool (read-only, so it never
  suspends a turn) for members with a same-role mate; 200 chars, 4 notes per
  member per turn; unread notes are inserted before each of a mate's model
  calls; the board (`Progress.Notes`, `Progress.Seen`) is in the sealed
  state (Progress fields 5-6). Risks named: chatter instead of work (capped),
  agreeing before starting (the note says never wait), a note that is wrong
  steering the mate (it is marked "for your information"), and the shared
  prefix (every member carries the tool so the prefix stays one list).
  To be A/B'd on and off in ab-4, and dropped if it does not pay.

**Next:** 10.7 ab-4 on eleven2go (v0.34.4-xollama.1 + a dev build): simple, then
medium and hard, 3 pairs each.

## Phase 11 — a council that works like its members can (ab-4, 2026-09-28)

ab-4 simple on eleven2go (b177, kvarn3, `-c 393216`, two live slots, broadcast
on): the council had not fixed Manic Miner after 1463 s, where plain fixed it in
233 s. What went wrong:

- The researchers made one call each. They cannot edit or run anything, their
  instruction was "report", and their reply cap was 384 tokens.
- The diagnosis was wrong (template literals; the real bug was `})};` → `});}`),
  and nothing tested it before the synthesizer acted on it.
- The synthesizer sent the same failing edit 15 times.
- Every synthesizer resume re-prefilled about 18k of 20.9k tokens (~20 s a step):
  its worker session is closed after each call, and the pool layer it attaches
  on resume ends before its own turns.

The owner's direction (2026-09-28): researchers propose, the synthesizer tests,
work is split with broadcast on, and every test result goes back to the
researchers. The council must not be starved. Beyond that, a council is not set
up in advance for a goal it does not know yet: it adapts to the request.

### 11.1 Caps (built 2026-09-28)
- Reply caps: planner 2048, researcher 2048, critic 1024, synthesizer 2048 (were
  512/384/256/1024). The researcher and critic prompts ask for terse replies.
- Evidence: a result is carried whole up to 4000 characters (was 1500); the
  preview is 30 lines / 2000 characters (was 10 / 800); one `council_evidence`
  answer is 16000 characters (was 8000); an evidence entry is capped at 16000
  (was 4000).
- A broadcast note is 600 characters (was 200).
- Left as they are: the 2048 default think budget, `maxLookups` 6,
  `maxRefusals` 2, and the `ResultBudget` of 1.5 windows. `max_rounds` 1 becomes
  the test-loop bound in 11.4.

### 11.2 A repeated call is pointed out (built 2026-09-28)
When a result matches, word for word, the one an earlier call with the same
tool and arguments got in the member's own transcript, a generic note follows
it. The note says that repeating the call will not change the outcome, and to
re-read what it acts on or try something different. The note works for any
tool. Guard: `TestARepeatedCallWithTheSameResultIsPointedOut` (checked by
removal).

### 11.3 A resumed member keeps its cache (built 2026-09-28)
opencoti #526 traced it in code: an open session prefix-matches its own
continuation and rebases onto its pool when one is sent again. The owner's
ruling: the council is a living thing until its client leaves. Member
sessions are no longer closed after a call. They live across calls, trips and
turns, and `closeSessions` closes them only when the request context ends,
i.e. the client left. Guards: `TestAResumedMemberReattachesToItsStage`,
`TestAClientThatLeavesClosesItsCouncilsSessions`. The live check is ab-5's
next run (measure the prefill of a resumed synthesizer step).

Original note:
The plan is to keep a suspended member's worker session, and its layer, alive
across the client's tool round trip, and resume on the same session. This waits
on opencoti's answer: does a resume without placement reuse the session's own
cached tokens, and must a pool outlive its attached sessions? Measured goal: a
resumed synthesizer step prefills only its new tokens.

### 11.4 Researchers propose, the synthesizer tests (loop built 2026-09-28; split and preemption open)
- Built: a synthesizer ends a failed check with `VERDICT: RETEST`. The report,
  with its calls' evidence, becomes `Progress.Tests` (state field 7) and
  starts the next cycle. `base` gives every member of that cycle all failed
  checks after the plan. Researchers are asked to propose (what, where, how to
  check) when a tool that changes something exists. `MaxTests` defaults to 6.
  The continue route does not loop. Guards:
  `TestAFailedCheckGoesBackToTheResearchers`,
  `TestATurnResumesPastAFailedCheck` and
  `TestTheFailedChecksTravelInTheState` (loop and prefix checked by removal).
- Owner's answers (2026-09-28): the planner re-plans each cycle from the
  failed checks, splitting what is left into workloads. The user gets a brief
  status and the council goes on in the same response. With thinking on, the
  user sees the deliberation restart. Built: `Replan` (`Progress.Replans`,
  state field 8), and `holdBack`, which streams the synthesizer up to the
  verdict only (`TestTheReportAfterTheVerdictIsHeldBack`). The retry bound
  becomes the builder's to set (11.5); 6 is the default until then.
- Watch live: each cycle builds its own layers, since the failed checks
  change the prefix. A cycle that runs within one trip holds more pools than
  `councilPoolSeats` counts. A pool that can't be built runs on the owner, so
  this is slower, not wrong.
- Design:
- A researcher's reply is a proposal: the change, where it goes, and how to tell
  whether it worked. Researchers and critics keep read-only tools, and the
  synthesizer stays the only writer.
- The synthesizer applies and tests the proposals. Every result, failed or
  successful, goes back to the researchers as the next round's input, with the
  proposal it tested. Up to 6 test cycles run per user turn.
- With broadcast on, researchers split the work (one part each) and post what
  has been refuted or confirmed. A running generation cannot take tokens, so a
  test result or verdict preempts: the member's stream is cancelled at a safe
  point, its partial output is kept, the result is appended, and it resumes
  (cheap once 11.3 holds). Only test results and verdicts preempt.

### 11.5 The builder: a council shaped by the request (design)
Owner's answers (2026-09-28):
- The council record, the target summary included, is kept by xollama in
  `council_chat_state`, like everything else.
- The synthesizer replaces the planner's first-step routing, which saves a
  hop. The planner schedules the researchers' work, splits it into
  workloads, and re-plans when a request comes back to the council.
- The builder sets instructions, think budgets and the retry bound, with some
  freedom: a prose fix needs few retries, coding or science more. Roles and
  counts stay as the user defined them; the builder gets that architecture
  and builds on it. Tool rules are fixed: one writer.
- Preemption only for test results and verdicts. The examples are built into
  xollama.

Built 2026-09-28: the builder (`internal/council/build.go`). It runs on the
owner before the first plan and on a `rebuild` route. Its instructions,
unset-role think budgets and `MaxTests` (0..12) are applied over the user's
council. `Progress.Build` is state field 9, kept across turns. The route
decision reads the target and offers `rebuild`. Also built the same day: the
synthesizer's front turn on tool turns (`internal/council/front.go`). It
replaces the route decision and the direct answer there. It answers on the
conversation's session, or calls `council_forward` (the builder first when
there is no build) or `council_rebuild` (it is told the new setup, then
forwards). Turns without tools keep the planner's route decision, with
`rebuild`. Preemption (11.4) is built too. A verdict note (`kind`
confirmed/refuted) cancels the mates' calls in flight; they keep their partial
text, read the verdict and go on, at most twice
(`TestAVerdictInterruptsTheMateGenerating`). The pipeline has no member
generating while the synthesizer checks, so test results reach the next cycle
through 11.4's loop. Open: 11.3, then the live A/B.

The earlier draft, where these differ:
- A council starts with only the synthesizer. The first request it forwards to
  the council summons the **builder** on the planner's slot. The builder reads
  the system prompt, the user's requests so far, and the tool and MCP surface.
  It judges what kind of council the task needs and writes each role's
  instructions, plus a short target summary.
- The builder decides instructions, member counts and think budgets, within the
  model's configured maximums. Tool policy is fixed: researchers and critics
  read, the synthesizer writes.
- The target summary is kept by the council runtime, in the session and in the
  sealed `council_chat_state`.
- On every later user turn, the synthesizer gets the summary and a nudge: is
  this a continuation or new work, and does the target still fit? Two tools
  replace the planner's routing: `council_forward` (send the request to the
  council as it is) and `council_rebuild` (summon the builder; the synthesizer
  is told when the new instructions are ready, then forwards). Only the
  synthesizer is given them.
- The builder carries built-in worked examples, one detailed for coding. They
  can be replaced with `council.builder.prompt`.

### 11.6 ab-5 and the fixes it asked for (built 2026-09-28)
ab-5, simple: plain fixed it on solidPC in 339 s and 30 trips. On eleven2go
plain took 162 s and 20 trips, while the council declared done without a fix
(659 s, 24 trips); on solidPC the council ran 3405 s and 60 trips unfixed.
Read from the transcripts, the members are served exactly as a plain turn is
(same ChatHandler, template and options; only 2 of 140 replies hit a cap).
What differs is what they read:
- At trip 28, the synthesizer's request is 65 messages and 144k characters.
  The client's history shows every member's calls as one assistant, and holds
  five copies of the 30 KB file and the front's wrong claims.
- The plan, findings and critiques arrive as user messages and are obeyed as
  instructions.
- The builder copied the front's template-literal theory into every role.
- The first cycle's synthesizer investigated on its own for 16 trips instead
  of testing proposals.
- Failed checks were lost between user turns, and the loop depended on the
  model writing `VERDICT: RETEST`.
- Critics tried `edit_file`.

The fixes (owner: "do A–H"; I and J from the same reading):
- A: the builder never names a cause, place or fix, only the kind of work
  and how to do it well.
- B: failed checks outlive the turn while its work stands (`Kept` →
  `Progress.Prior`, state field 10, at most 6 of 6000 characters). A rebuild
  drops them, keeping this turn's front attempts. Every member reads them
  ahead of the plan (`withPrior`).
- C: a testing synthesizer that ends without a verdict is asked for one once.
  The nudged reply is unseen, and only its verdict joins the reply the user
  already read.
- D: a cycle's tool steps are bounded (`MaxSteps`, default 6, the builder's
  `max_steps` 2..16, Build field 5). At the bound the synthesizer is told; two
  steps later its report is taken as a failed check.
- E: the synthesizer's test note says to apply the council's proposals one at
  a time, never to investigate on its own, and to end with `VERDICT: DONE` or
  `VERDICT: RETEST`.
- F: the front's own attempts before it forwards reach the council as a
  failed check (`frontReport`).
- G: the coding example asks researchers to localize before theorizing and
  the planner to locate first.
- H: critics are told they cannot make or test a change.
- I: findings and critiques are introduced as claims, not facts or
  instructions.
- J: the front answers only when the answer or change is already in view,
  and calls `council_forward` before investigating. It is told to forward
  after 4 tool steps, and forwarded two later.

Guards: `TestASynthesizerWithoutAVerdictIsAskedForOne`,
`TestASynthesizerThatKeepsInvestigatingRunsOutOfSteps`,
`TestAFrontThatInvestigatesIsForwardedWithItsAttempts`,
`TestFailedChecksCarryToTheNextTurn` and `TestTheFailedChecksTravelInTheState`.
Each guard was checked by removing its fix. Next: rerun simple on eleven2go,
then medium and hard once each to look for a cliff.

### 11.7 Who said what (built 2026-09-28)
The simple rerun on eleven2go (b96e3c96) stayed unfixed at 667 s and 24
trips. The engine asserted in cycle 3 (opencoti #530, position-window
scatter with two sequences prefilling), after cycle 2 had found the right
line and both missing functions. The bounds held. The first cycle still
chased the front's first guess, which F had carried as "what it concluded
last". The owner's points on the delta, and what was built for each:
- **The client's history should show the member.** `council.History` rewrites
  the earlier turns once, the same for every member:
  - each forwarded call is split out under its member (`[COUNCIL · RESEARCHER
    1, ROUND 2 · TOOL CALLS]`), with that member's results;
  - a long result identical to an earlier one becomes a pointer.
  It runs in `server/council.go` before compaction, after the session id and
  `councilToolTurn`.
- **Wrong claims must not stay in view.** The member's own text beside its
  calls is dropped from the history. The front's report (F) carries only its
  calls and what they returned.
- **Every council message names the role that wrote it; the user's stay
  plain.** A template has only system, user, assistant and tool turns, so
  another member's work can only arrive as a user turn. That is why the
  findings read as orders. Each council message now opens with its source
  (`sources.go`: instructions for you, the planner's plan, the researchers'
  findings, the critics' reviews, the synthesizer's failed checks, checks
  from before, notes from mates, the user's system prompt). `sourcesNote`
  tells every member that only unheaded messages are the user's. The
  re-plan's failed checks are their own message, apart from the planner's
  instruction.
- **The builder's anchoring** is covered by A (it never names a cause) and by
  F above: the builder reads the conversation only, and the front's prose no
  longer reaches anyone.

Guards: `TestEarlierTurnsShowWhichMemberCalled`,
`TestEveryCouncilMessageNamesItsSource`,
`TestEarlierTurnsReachTheMembersAttributed` (server), and the front-attempts
test, which now refuses the front's conclusion. Each was checked by removal.
Relief for #530 on eleven2go: `XOLLAMA_ENGINE_ARGS=--kv-residency-mode head`,
an operator setting on the test host, until opencoti's fix.

### 11.8 A council follows the engine's parallel slots; cloud members apart (built 2026-09-28)
The owner, on hearing the council ran with 2 live slots of the engine's 4:
"the council should follow the parallel or max parallel slots, which are 4
default with opencoti engine; the cloud models should not count the same
slots; default 3 slots in parallel, configurable in the council setup".
- `councilLive` now starts a council with the engine's parallel ceiling live
  (`slots.max`, `XOLLAMA_MAX_PARALLEL` or 4). Its local width is the floor,
  not the count: max(researchers, critics + 1, since the critics' reviews run
  beside the synthesizer, 11.9), at least 1.
- A role on a cloud model or another host takes no engine slot, so it is left
  out of the width. Cloud members run `council.cloud_parallel` at a time
  (default 3, at most 16; `xollama tweak model --council-cloud-parallel`),
  one count per council model (`server/council_cloud.go`).

Guards: `TestACouncilStartsWithTheEnginesParallelSlots`,
`TestACouncilsWidthCountsOnlyLocalMembers`,
`TestCloudMembersRunCloudParallelAtATime` and
`TestOnlyCloudMembersTakeACloudSlot`. Each was checked by removal.

### 11.9 The critics review the synthesizer's checks, asynchronously (built 2026-09-28)
The owner, on the synthesizer doing nearly every call, checks included: "why
not telling him to ask the critics to review its checks?", then: "they should
be async, the synthesizer sends to the critic something to review and they
got queued, every time a critic completes a review, the synthesizer gets back
those processed."

The third simple run on eleven2go (311010f9, `head` relief) shows why. It
stayed unfixed at 60 trips and 3466 s with no engine fault, and the
synthesizer made 27 edits against 11 checks.

Built:
- `council_review` (`internal/council/review.go`, synthesizer only, answered
  in place) queues a job: the change it states, and what its calls actually
  returned since the check it sent before.
- A `Desk` per conversation (`server/council_review.go`, `councilDesks`) runs
  one reviewer per critic, on the critic's model and think settings, each on
  its own session (`~reviewer-N`). The reviewers carry no tools and state
  their own window on opencoti (`reviewPlacement`).
- Before each synthesizer call, the reviews finished since its last call
  arrive under `[COUNCIL · REVIEWS OF YOUR CHECKS BY THE CRITICS]`.
- Nothing waits on a review except a DONE verdict. A DONE sends its last
  check itself when it wasn't sent, and waits up to 3 minutes for the reviews
  still out. When there are reviews, the synthesizer reads them and either
  goes back to work or repeats DONE; a DONE that stands keeps the answer the
  user already read.
- Reviews are scoped to the turn (`Config.Turn`, the turn's hash), so an
  earlier turn's review never reaches a later synthesizer. The desk ends when
  the client leaves, or after 15 minutes with nothing sent.

Guards: `TestTheCriticsReviewAChecksWhileTheSynthesizerWorks`. Its reviewer
holds the review until the synthesizer's next call has started, so a blocking
review would hang it. The others are `TestADoneSendsItsLastCheckForReview`,
`TestTheDeskWorksItsQueue`, `TestOnlyTheSynthesizerSendsChecksForReview`,
`TestAReviewerStatesAWindowOfItsOwn` and `TestAToolTurnKeepsAReviewDesk`.
Each was checked by removal.

Followed the same day, on the fourth simple run (5b331d1b):
- The synthesizer never called `council_review`, so a check it moves on from
  is now sent for it. That happens when its next call makes a change; another
  read is still part of checking. The DONE gate sends its last check under
  that same id, so no check is reviewed twice, and a change never checked is
  sent with the DONE.
- Reviews stream as thinking when they reach the synthesizer, under
  "Reviewer N" (the owner: "stream the reviews as thinking too"). A review
  that finishes while the client runs a tool has no response open, so
  delivery is the moment it can always be shown.

Guards: `TestACheckLeftUnsentIsSentForTheSynthesizer`,
`TestACheckIsReviewedOnce` (found a double review first) and
`TestAReviewHasItsReviewersHeading`. Each was checked by removal.

The builder anchored again on that run, with a place taken from an earlier
turn's tool results ("likely in the Level class methods on lines 115-126").
So the builder now reads only the system prompt and the user's own messages
(`builderConversation`), as the 11.5 design has it. It runs on its own
session (`~builder`) with a window of its own (`ownWindow`), so it no longer
disturbs the owner's cache. Guarded in
`TestEarlierTurnsReachTheMembersAttributed` (checked by removal) and
`TestACouncilOnPolyKVBuildsItsTreeOnce`.

### 11.10 The planner keeps the council's task list (built 2026-09-28)
The owner: "is the planner keeping history? he should act as a coordinator
and program/task manager … consider if we need to give him a task manager as
a tool so he can only plan and schedule work on the researchers with it".

Before this, the planner did not keep history. A re-plan saw its first plan
and the failed checks, but not its own re-plans or what the researchers had
settled. Nothing recorded which hypothesis a check had refuted, so the same
fix was tried again.

Built (`internal/council/tasks.go`):
- The plan's JSON schema requires `"tasks"`: id, task, status (open,
  assigned, done, refuted), the researcher it is assigned to, and the outcome.
  Every member reads the list under
  `[COUNCIL · THE COUNCIL'S TASK LIST, KEPT BY THE PLANNER]`, and each re-plan
  reads it and updates it (`replanRequest`, `ledgerRules`).
- The runtime keeps the rules, whatever the planner writes (`mergeTasks`):
  - no task is deleted;
  - done or refuted needs an outcome, or the task is open again;
  - assigned needs a researcher that exists;
  - refuted stays refuted;
  - new tasks are numbered after the last.
  There are at most 24 tasks of 400 characters.
- The list travels in `Kept` and in `council_chat_state` (Progress fields
  12 tasks and 13 carried, Plan field 3), so the next turn's planner starts
  from it. A rebuild (the front's or `RouteRebuild`) drops it with the
  earlier checks.
- The builder must write the planner's instruction as a coordinator's. It
  says what one task is for this kind of work and what evidence closes one,
  and that the planner reads the list and every failed check before
  planning again (`builderPrompt`).

The choice: a field in the plan, not a tool. The planner answers in one
structured reply and has no tool loop. A task-manager tool would give it one,
a round trip per update, for the same operations. The field lets it do
nothing but plan and schedule, and the runtime enforces the manager's rules
either way.

Guards: `TestTheTaskListKeepsItsRules`, `TestThePlannerKeepsTheTaskList`
(carried, shown at the re-plan, dropped on both rebuilds) and
`TestTheBuilderMakesThePlannerTheCoordinator`. Each was checked by removal.

The sixth simple run (34924cbe) fixed the task: 40 trips, 1401 s, against
plain's 27. Two cycles chased the wrong theory against one unchanged output.
Then the stuck note fired, and the third cycle replaced the faulty method
whole. It followed each moved error (`sX`, then `collide`) to the fix.

Fixed the same day, from the same run: the planner numbered
its re-plan's list from 0 again (0, 1, 2 against the list's #1, #2, #3), so
each update landed on another task. It had read two numberings: the list's,
and the `id 0`s of its own first plan, which the re-plan request repeats as
it wrote them. Now:
- every plan and re-plan is kept with the list's ids, so no member and no
  re-plan reads the planner's 0s;
- `mergeTasks` matches a task by its words first (`updates`), and an id stands
  only for a task whose text is left out or shares at least half its words;
- the rule says to keep the id the list shows (#3 is id 3), never renumbered.

Guard: `TestARenumberedListUpdatesTheTasksItNames` (the run's shape), checked
by removal of each of the three.

### 11.13 What each role spends (built 2026-09-28)
The owner's aim: find the role worth a bigger or a cloud model. That is
"best" meaning both helpful and cheap: a role that spends little and helps a
small synthesizer much is the one to upgrade first.

That needs each role's cost, so every council turn now reports it
(`council_usage_v1`). The done chunk carries one entry per role (and per
model and host): calls, prompt tokens sent (with the cached part), tokens
written, and engine and wall durations. The manic harness records it per
trip and sums it per role in the run's summary.

First reading, from the eleven2go debug log (characters, 032db6c2):

| role | medium: calls, prompt share, output share | hard (so far) |
|---|---|---|
| researcher | 30, 45 %, 39 % | 29, 32 %, 24 % |
| synthesizer | 30, 43 %, 28 % | 29, 45 %, 40 % |
| critic | 7, 7 %, 11 % | 12, 14 %, 11 % |
| planner | 3, 2 %, 12 % | 5, 4 %, 11 % |
| front, builder, reviewer | 8, 3 %, 10 % | 8, 4 %, 13 % |

The prompts are about 68× the output. On a cloud model the cost is the
prompts sent again on every call; locally PolyKV serves most of them from
the cache.

Guards: `TestACouncilTurnReportsWhatEachRoleSpent` and
`TestAUsageBookCountsTheCachedPromptAsSent`, each checked by removal.

Helpfulness is the other half. Measuring it takes swapping one role at a time
to a bigger model on the same tasks (a role-upgrade matrix), which is
proposed, not run.

### 11.14 The synthesizer applies the proposals together (built 2026-09-28)
Medium on 032db6c2: the council fixed it in 1101 s and 27 trips, plain in
91 s and 11. Plain read the file once, made four edits in a row and checked
once. The council paid one whole cycle (re-plan, research, critique,
synthesizer) per fault. The consultants council (csl-2026-09-28-1441-4bbf)
found the cause in our own prompt. `testNote` said "apply the council's
proposals and check them, one at a time … After each change, run its
check", and the builder's coding example repeated it. The researchers were
asked to "propose it", one change.

Now:
- The synthesizer makes every proposed change that does not conflict in one
  reply (several tool calls travel in one trip), then checks once.
- A new failure whose place and fix the check's output and the material
  already show, the synthesizer fixes itself and checks again. RETEST is left
  for a fault that needs investigating.
- Researchers propose every fault they find in their part, not only the first.
- The builder's example says the same.

Guard: `TestTheSynthesizerAppliesTheProposalsTogether` (and the researcher
wording in `TestResearchersAreToldWhichToolsOnlyRead`), checked by removal.
Next: medium on this build, against plain's 91 s.

### 11.15 Less waiting: the consultants' #3–#6 (built 2026-09-28)
The owner took the consultants' proposals #3–#6, with #3 and #5 as Cerebriline
has them ("we are paying exceptional latency so anything that helps is
welcome"). #2, overlapping the stages, waits for the follow-up on flow modes.

- **#3, a change that changes nothing** (`internal/council/loops.go`, ported
  from Cerebriline's editor): a call to a writing tool whose old and new text
  are the same (`old_text`/`new_text` and the usual pairs) is refused in place
  with "No change: …". It is never forwarded, so it costs no trip.
- **Anti-loop** (Cerebriline's `loop-detection.ts`): a writing call that a
  member sends again with the same arguments, and that gets the same result
  back, carries Cerebriline's steering ladder with a strike count (look at the
  target with a read tool; change what produces it; leave it and take the
  next thing). At `loopStrikes` (4) the member's steps end, with a report (a
  RETEST in a testing cycle). A different result starts the count again.
- **#5, a refused change** (Cerebriline's first steering step): the
  synthesizer is told to read the target as it is now and make the change
  again from that text, never the same call unchanged. The builder's example
  no longer says "never resend an edit that failed". On hard, the one edit to
  `dDec` (where the fault was) was refused and never tried again.
- **#4, the front's handoff**: `frontSteps` goes from 4 to 3. The front's
  reads that nothing changed since reach every member as research
  (`[COUNCIL · WHAT THE SYNTHESIZER ALREADY READ]`, read back by ref with
  `council_evidence`; state Progress field 14, this turn only). Its failed
  attempt is only its changes and what followed them; a front that only read
  made no attempt.
- **#6, no idle researchers**: the planner's rules say every researcher gets a
  workload of about the same size, all at once, and none is left without a
  task while tasks are open. Work stealing in the runtime (a researcher that
  finishes early takes the next open task) goes with #2.

Guards: `TestANoOpChangeIsRefusedInPlace`,
`TestTheSameChangeSentAgainEndsTheSteps`,
`TestARepeatedCallWithTheSameResultIsPointedOut`,
`TestTheCouncilIsToldToRedoARefusedChangeAndKeepResearchersBusy`,
`TestAFrontsReadsAreResearchAndItsChangesAnAttempt` and
`TestAFrontThatInvestigatesIsForwardedWithItsAttempts`, each checked by
removal. The test driver now accumulates results as `councilToolTurn` does.

### 11.16 The builder on its own model (built 2026-09-28)

The owner's cloud plan runs the builder on `glm-5.3-turbo:cloud` while one
other role at a time moves there. The builder ran on the planner's model and
host (`build.go`), so the setting did not exist.

- `council.builder` (`types/xollama/council.go`): `model`, `host`, `think`,
  `max_tokens`. No `count` (there is one) and no `prompt`: the builder's reply
  is JSON the runtime parses, so its prompt is a contract, not a persona.
- `builderOn` (`internal/council/build.go`): the builder's own model/host,
  else the planner's; its own think, else the planner's. Unstated, nothing
  changes. `tweak` gains the `--council-builder-*` rows but `prompt`.
- Tests: `TestTheBuilderRunsOnItsOwnModel` (mutation-checked: pointing it back
  at the planner fails "its own"), validation, clone, prune and lookup cases.

### 11.17 A writer has room for its edit, and a cut reply is asked again (built 2026-09-29)

The medium council on 837a1fce + b208 (eleven2go 3090) ended unfixed after
57 trips. Plain fixed the same task in 12 trips / 114 s: it read the file and
rewrote the whole broken class in one `edit_file`. The council's front and
synthesizer set out to make the same rewrite, the one stuck detection (11.11)
recommends. The front's last three calls each stopped at exactly 3,072 output
tokens, the cap. Every call was cut inside the tool call it was writing, so
only the prose before it survived ("I'll rewrite the … class"). The class is
~10.6 k characters, about 3.3 k tokens, and an edit carries the old text and
the new, about 6.6 k. The cap made the fix impossible.

Built (`internal/council/cut.go`, additive):
- `writeTok`: a member that writes (`writes`: synthesizer, planner, front) gets
  a reply cap of at least `writeMaxTokens` = 16384 on a tool turn (in
  `callTools` and the front's call). Room for an edit of a part twice the size
  of the one that was cut. Replies without tools keep their caps.
- `Reply.Cut`: `server/council.go` carries the engine's `done_reason
  "length"` into the member's reply. A writer's reply that was cut and holds
  no call is asked again once (`maxCuts`), with `cutNote`: the call was not
  made and nothing changed, so make the change in smaller steps. The note
  names no topic. Cut again right after the note, the reply stands, so the
  council does not loop.
- Tests: `TestACutWriterIsAskedAgainForASmallerChange` (front and
  synthesizer), `TestACutWriterIsAskedAgainOnlyOnce`,
  `TestOnlyAWriterIsAskedAgain`, and `TestACutReplyReachesTheCouncil` (server:
  done_reason → re-ask, and the cap on the wire). Each was checked by removal:
  without the retry, without the raised cap, and without reading
  `DoneReason`.

### 11.18 The owner makes room for a member booked beside it (built 2026-09-29)

The builder and the reviewers are booked on sessions of their own, beside the
conversation's owner. The owner books the whole window by default and grows
back to it before every turn (`begin`). On eleven2go, with the context cut to
one slot's size by the placement fault, the builder was refused for its whole
admission budget ("base 0/196608 free need 5632"), and the turn failed after
2 minutes.

Built (`server/council_room.go`, additive):
- `roomFor`: before a member with its own window is booked, `/kv`'s
  `largest_admissible` (now read, `KVStatus.LargestAdmissible`) is compared
  with the member's window.
- Short of it, the owner gives back the difference. It never goes below its
  used cells plus the turn's reserve. The resize is applied at once where the
  engine allows it, else deferred to the owner's next idle moment, and the
  member's admission wait seats it.
- A shrink already queued counts as room. One member at a time asks
  (`roomMu`). The next turn's `begin` grows the owner back when nobody is
  refused.
- On an engine that does not report `largest_admissible`, nothing changes.
- Tests: `TestTheOwnerMakesRoomForAMemberBookedBesideIt` (full, room already,
  nothing to give, not reported) and `TestTheBuilderIsGivenRoomBesideAFullOwner`.
  The latter was checked by removing the call.

### 11.19 The check is the read after the change (built 2026-09-29)

The transcripts of the medium council on 96edc4ae show the plan held on to an
explanation its checks had refuted, one cycle after another. It converted one
kind of syntax, then more of it, then another kind. Every check returned the
same error.

The runtime told it to. Each cycle's "check" was taken as the last read-only
call, and the synthesizer searched the file after running the check. The
search answered something new each time, so the planner read "Its check's
output changed from the one before: progress, and the new output is the lead
to follow". The stuck note (11.11) never fired.

Built:
- `lastCheck` (`internal/council/stuck.go`) takes the first read-only call
  after the cycle's last writing call: the check of the change. A later read
  is investigation. With no change in the cycle, the last read stands.
- `stuckNote` also says the explanation behind the unmoved changes is
  refuted: mark its tasks refuted and assign no more changes of the same kind.
  It still names no topic.
- Guard: `TestTheCheckIsTheReadAfterTheLastChange`. With the old `lastCheck`
  it fails exactly as live ("more search results").

### 11.20 A misquoted change is answered with the text the read shows (built 2026-09-29)

On e75c7c3e run 2, 6 of 21 edits named text the file did not have. `dIt` is
one 564-character minified line; to change its last characters the
synthesizer quoted the whole line, copied 560 characters exactly, then wrote
the tail as code usually looks (`}}})`, `});}}`, ...) instead of the file's
`} })};`. Seven attempts, a trip each, on text that was not even a fault. The
"read the target and quote from it" note (11.15) did not help. The
consultants (`csl-2026-09-29-0846-320d`) ranked this second, as deterministic.

Built (`internal/council/quote.go`):
- Before a writer's change is forwarded, the member's own reads of the same
  target (the `path`-like argument), latest first, back to its last change to
  that target the tool did not refuse, are searched for the change's quote
  (the `old_text`-like argument, `noOpPairs`). Line-number gutters are dropped
  first (`N: `, tab, `|`).
- A read with the whole quote lets it go. One with at least `minQuoted` (32)
  characters of its start, a mismatch inside a line and text after it answers
  the call in place: where the match stops, what the read has there and what
  the quote has, verbatim, and "quote only the smallest span around the
  change that occurs once". No trip.
- A mismatch at a line's end, a read that ends there, or too short a match
  lets the call go: that read showed only part, or the quote is of
  something else (an insert written as a replace).
- At most `maxMisquotes` (4) per member are answered in place; past that the
  client's own answer stands. An answer in place costs a step, not a
  refusal.
- Guards (`quote_test.go`), each failing with the guard off:
  `TestAMisquotedChangeIsAnsweredWithTheActualText`,
  `TestARefusedChangeKeepsTheRead`,
  `TestMisquotesAreAnsweredInPlaceOnlySoOften`; and
  `TestAChangeTheReadBearsOutGoesOut` (exact across lines with gutters, too
  little matched, a change in between).

Measured (eleven2go, medium): FIXED in 26 trips on `96ff1f5d`, then
UNFIXED at 60 on `cf223635`, with 13 of 32 edits missing their text. The
misses followed the member's own successful edits, after which its read
counts as stale. Built the same day: the member's own changes since its read
are replayed onto it (`replay` in `quote.go`; a whole write is the text
itself), so the read stays current across them. Guard:
`TestTheReadFollowsTheMembersOwnChanges`, which fails with the replay off.

### 11.25 A member's layer ends at its own instruction (built 2026-09-30)

On solidPC's hard run (569747788), pools 13 and 14 were built 7 s apart on one
parent, both 6724 long. They were round 2's two researchers, one layer each.
Their messages, from the member log, are equal up to each one's
`ROLE: RESEARCHER n` instruction. After it comes
`[COUNCIL · NOTES FROM YOUR MATES]` (`broadcast` is on in `omni-council-ab5`).
The layer was cut before the last user message, the notes, so it held the
member's own instruction and no two researchers ever shared their stage. The
same cut failed wherever a user message follows the instruction: the user's
system prompt after the synthesizer's role, and the council's nudges in a tool
loop (budget, narrated call, verdict, reviews, a cut reply). Those put the
member's own tool turns into its layer, which is then a private pool on every
round trip.

Built:
- `council.OwnPart` (`internal/council/sources.go`): a member's own part starts
  at its first instruction after the last plan. `workerPlacement` cuts the
  layer there. The last-user rule remains only for messages that carry no plan.
- `server/council_layer_log.go` logs each built layer's text past its parent
  (`council: pool text`). For a sibling on the same parent, it logs where the
  two diverge (`council: pool shares a prefix with a sibling`), so a sharing
  miss can be read off the log.
- The build line logs the parent's id (it printed a pointer) and the layer's
  key.
- Guards: `TestOwnPartIsTheMembersInstruction` and
  `TestResearchersWithNotesShareTheirStage`. The latter fails on the old cut.
  bug-184.

### 11.24 A turn's layers are kept across its round trips (built 2026-09-29)

Hard on eleven2go (5ce5f7e7): the pool builds prefilled 582,509 tokens, and
265,334 of them were a layer the previous request had just released. Each
harness tool round trip is its own HTTP request. Each request built its tree
and released everything at its end except the conversation's root (pool 1,
kept on all 47 requests). So after every resume the members' stage layer was
built again: one 6,636-token layer five times in 78 s, 4.7 s each. The
reviewers, the first suspect, were 15.6k tokens (1.1 % of the prefill).

Built (`server/council_layers_kept.go`, part of the `council` hook):
- A request that ends with the members' calls (`suspend`) puts its layers
  aside for the owner (`stashLocked`, from `release`). It keeps only the layers
  this request used and the ones they stand on (`used`). An adopted layer that
  is not asked for again is a stage the turn has moved past, and it is released.
- The next request takes them in `begin` under the kept root's rules: only on
  the runner that made them, only while the owner's allocation lives, and only
  on the same kept root. It adopts them once the turn is known (`adopt`, same
  turn hash). The members then attach to them, as `layer` finds them by text.
- Anything else releases them, newest first. That covers another turn, another
  runner, another root, a rebuilt root (`dropAdopted` runs before the old root
  goes, since the engine keeps a pool with a child), and a client that does not
  come back within `councilStashIdle` (10 min).
- Guards, each mutation-checked (the rule removed, the test fails):
  `TestARoundTripKeepsItsStageLayers` (the fake engine, two requests of one
  turn), `TestAnotherTurnReleasesTheKeptLayers`,
  `TestKeptLayersGoWhenTheClientDoesNotComeBack`,
  `TestAStashKeepsOnlyTheLayersInUse`, `TestAStashIsAdoptedOnlyWhereItStands`
  and `TestARebuiltRootDropsTheAdoptedLayersFirst`.
- Measured on solidPC's 3090 (569747788, `omni-council-ab5`, hard, 40 trips,
  2026-09-29):
  - 37 requests kept their layers and 36 were adopted; the last stash waited
    for the idle release.
  - Resumes built nothing.
  - 275,292 pool tokens in 34 builds, 6.9k a trip, against 12.1k a trip on
    eleven2go's run (a different host and model).
  - No release failed.
  - Open: pools 13 and 14 have the same parent and the same length, built
    7 s apart in one request.

### 11.23 A member sized to its request is sized in tokens (built 2026-09-29)

Hard on eleven2go (5ce5f7e7): four requests ran in a 12800-token engine
window, and one, a critic's background review of 13196 tokens, was refused
(`exceeds the available context size`), so that review was lost. These are
the members that run on a session of their own with a window fitted to their
request (`ownWindow`: a background reviewer, the builder); the researchers,
critics and synthesizer worked in 196608-token windows on the pool tree (116
requests). The fitted window counted a third of the request's characters as
its tokens; code and JSON run near two characters a token (the refused review:
about 1.9).

Built (`ownWindow`, `server/council_review.go`): the request is counted as the
member sends it (rendered and tokenized, `councilTree.tokens`, the count the
unpooled path already used); the background reviewer's member set carries the
tree's counter (`councilMembers.count`). Half the characters is the estimate
only when counting fails. Guards: `TestAReviewersWindowHoldsItsRequestInTokens`
(fails when the count is ignored) and `TestAReviewerStatesAWindowOfItsOwn`.
Open: a reviewer still prefills its whole request on its own session, sharing
nothing with the tree; attaching it to a pool layer is the efficiency step.

### 11.22 A change of approach has to show in the task list (built 2026-09-29)

Hard on eleven2go (5ce5f7e7, 120 trips): plain fixed it in 24 trips by writing
the whole file at trip 22 (it passes `run_gamefull.js` as `reference.html`
does); the council made 16 local changes and never wrote a whole part. The
stuck note fired (58 times in the log). The planner's round-2 plan said
"instead of piecemeal fixes, I'll replace the entire JavaScript section", and
its list kept all four tasks `open`, assigned the refuted template-literal
task again and gave the replacement to nobody. The synthesizer applies what
the members propose, so the cycle made local changes again. Round 5 narrowed
to the right place two minutes before the engine died (host out of virtual
memory, not the council).

Built (`approachKept`, `keptNote` in `internal/council/stuck.go`,
`ReplanAgain` in `steps.go`, the replan in `run.go`):
- When the stuck note is in force for a re-plan, the list's update must mark
  at least one task refuted and add at least one task. Otherwise the planner is
  asked once more, with its plan and a note that says which of the two it left
  undone, and that a whole-part replacement is a task: a researcher writes it
  out in full and the synthesizer applies it in one write. The second answer
  stands, whatever it is. One extra planner call, only on a stuck cycle.
- Structural, no topic words (the stuck test's word ban covers `keptNote`).
- Guards: `TestAReplanThatKeepsTheRefutedApproachIsNamed` (the real round-2
  list) and `TestAStuckReplanIsAskedAgainOnce` (asked again once when stuck,
  never while checks move; fails when the rule is disabled).
- Not yet measured live: eleven2go is lent to opencoti.

### 11.21 The check is the call the member checks with (built 2026-09-29)

On 4770e33b run 2 the council never found the stray `}` ending `dGrid`. All 7
checks returned the same `SyntaxError: Unexpected token '{'`, yet the planner
was told "changed from the one before: progress" every cycle (149 such notes
in that log, none "same"), so the stuck note never fired. The first theory
(template literals, read into the `{` of the error) was never refuted, and
every cycle rewrote more template literals.

11.19's rule, "the first read after the cycle's last change", took the
wrong call again. The synthesizer ran the check, edited again, then read the
file it had edited, so the recorded "check" was the file's own text, which
differs after every edit. The e75c7c3e logs show the same thing: `moved`
140 times, `same` never.

Built (`lastCheck`, `internal/council/stuck.go`):
- Among the read-only calls after the member's first change, the check is
  the one (tool and arguments) it called most, the earliest on a tie. One
  whose latest output is the previous cycle's check is taken first. The
  harness's named check tool (`council.check`) still restricts the choice.
- Replayed over run 2's trips, it records the same `run_game` error every
  cycle, so the stuck note fires from cycle 3.
- Guard: `TestTheCheckIsTheCallTheMemberChecksWith`. On the old rule it
  returns the edited file's text, exactly as live.
  `TestTheCheckIsTheReadAfterTheLastChange` (11.19) still passes.

### 11.11 Checks that stop moving change the approach (built 2026-09-28)
The fifth simple run (b336b144) returned the same "missing ) after argument
list" from every check for 60 trips. The plain arm fixed the task in 27 trips:
it rewrote the file whole at trip 20, then followed each new error. The
council's own rules had forbidden that larger change.

Built (`internal/council/stuck.go`):
- The last read-only call's result of each failed cycle is recorded
  (`Progress.Checks`, state Progress field 11; 2000 characters each).
- The failed-check list marks each check as the same output as the one
  before it or a changed one (`sameNote`, `movedNote`). A changed output is
  progress, and the lead to follow.
- After two checks in a row with the same output (whitespace ignored), every
  member of the next cycle reads `stuckNote`: change approach, find where the
  fault is by narrowing what the check exercises, or replace the failing part
  whole. The note names no topic (the owner: "make sure the nudge is agnostic
  of the topic"); a test holds it free of domain words.
- Researchers may propose a whole-part replacement (`wholeNote`), and the
  synthesizer's check wording says a changed error is progress (`checkNote`).

Guard: `TestACouncilThatDoesNotMoveTheCheckChangesApproach` (same outputs →
stuck; changing outputs → moved, never stuck), checked by removal.

### 11.12 A review always ends with its verdict (built 2026-09-28)
On the fifth run, one of the six reviews carried the `REVIEW:` line. The
owner: "That's what a critic does, always … reject the output of the critic
and tell him to output his answer with the format that was requested, giving
him the structure to follow."

Built: the reviewer is given the structure (`CHANGE:`, `CHECK:`, then one
`REVIEW:` line). A reply without its verdict (`verdictOf`) is sent back once,
with `reviewFormatNudge` repeating the structure. A second miss is delivered
marked `REVIEW: UNCLEAR (the critic gave no verdict)`, so it is never read as
a confirmation.

Guard: `TestAReviewWithoutAVerdictIsSentBack`, checked by removal.

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
- 2026-09-25 — Phase 1 chose the in-house `errgroup` runner. It is the
  cheapest (56 µs per council and 3.2 µs per direct turn), adds no
  dependency, and is correct as written. eino, langgraphgo and trpc-agent-go
  all pass the suite, but only after sibling cancellation and error ordering
  were added by hand. They cost 1.2–16× more per request and would move
  modules upstream owns (sonic, protobuf, go-sqlite3, testify). At model
  speed on b65 all four are within noise of each other (65–70 s per council).
- 2026-09-25 — Every council member states its own `num_ctx`. The planner's
  decision, direct answer and plan share one engine session, closed when the
  run ends.
- 2026-09-26 — Every member states its context, and compaction follows the
  engine's pressure: the owner's raw `pressure` from `/kv` at 0.85, plus
  global pressure and resize on b111 (see "Context, pressure and
  compaction"). The library implementations were removed from the repo
  (notes kept in `plans/council-eval/notes/`), so no `go.mod` in the tree
  links them.
- 2026-09-26 — A council is request-side only. It is left out of the launch
  config, so the launch never sees it (it still has its own runner, as every
  tag does; corrected in Phase 3). Phase 4 adds one exception: on PolyKV the
  launch reserves the council's pool seats.
- 2026-09-26 — Schema v4 for the council, and no environment fallback: a
  council is a property of the model, never of the server.
- 2026-09-26 — Phase 4 moves the shrink to after a turn and makes it
  deferred. The owner gives back cells only when others are being refused,
  and only when that frees at least 4096 cells or 10 %, so it does not shrink
  on every turn and then grow straight back.
- 2026-09-26 — Phase 6 approved as proposed. `think: on` is 2048 tokens per
  role, configurable per role. `num_ctx 0` under PolyKV builds unowned pools;
  the launch takes the trained context. `slots.live` replaces the parallel
  environment variable on opencoti only. Stock llama.cpp and opencoti without
  pools are untouched.
- 2026-09-26 — Phase 7 added: roles on other models and other ollama
  instances (cloud models, another machine), at the owner's request.
- 2026-09-26 — Phase 7 decided: a researcher or critic that fails elsewhere
  falls back to the council's model; a planner or synthesizer failure fails
  the turn. A literal host URL is fine, and `host` stays in v4 (unreleased).
  Hosts are gated by the operator's `XOLLAMA_COUNCIL_HOSTS`, because a pulled
  model could otherwise send conversations anywhere.
- 2026-09-26 — `/api/engine` exposes every opencoti management route, reads
  and controls, always (the owner's call; the risk on a non-localhost bind is
  stated in the doc). Inference, `/cors-proxy` and `/tools` stay out.
- 2026-09-26 — A council answers chat only. `/api/generate` and
  `/v1/completions` are prompt completion and serve the plain model. The CLI's
  one-shot `run` is moved onto chat for a council, rather than teaching
  generate about councils.
- 2026-09-26 — Desktop: option C (the owner's call). A council badge and a
  Deliberation toggle that sends `think`; no per-chat council switch (A), and
  no write of the model's config from the UI (B).
- 2026-09-26 — Follow-up: opencoti b124 adds `continue_pool` and unowned
  pools (patch 0406, #343). A council's next turn could continue the
  conversation's pool instead of rebuilding P1. Its own plan,
  [council-continue-pool.md](council-continue-pool.md), waits for a build
  with 0406 on the HF dev repo (the owner's call).
- 2026-09-26 — Phase 8: the council's compaction becomes a port of
  Cerebriline's agentic council compaction (the owner's question; the
  earlier design was not based on it). A compaction is carried forward per
  conversation and folded incrementally, and is sized on the granted window
  when it is smaller than `num_ctx`, as Cerebriline does. Approved.
- 2026-09-26 — The conversation is held once (the owner's call, "attach the
  planner to P1 now"): a root pool first, the planner attached, the root kept
  and forked between turns. On "compact the session" the turn compacts and
  retries, instead of running its members unpooled.
- 2026-09-26 — Members may think, per role (`council.<role>.think`), with
  their reasoning hidden. The default is `medium`, it is configurable, and the
  cap message is the model's own. The council sends an explicit token
  budget, not a level, because a level is a share of `num_predict`, and for a
  member that is its reply cap. Live, `medium` at 131k (32k tokens per
  member) let a researcher loop past 29k tokens. The member default is open.
- 2026-09-27 — After ab-3: one critic by default; a council gets a slot per
  parallel member; reads shared only with who asks; a critic's confirmed
  error goes straight to the synthesizer; a broadcast channel is tried and
  dropped if it does not pay; the council stays alive across turns and the
  planner decides done / continue / again (the owner's calls).
