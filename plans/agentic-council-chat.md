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
