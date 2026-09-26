# Agentic Council Chat

**Status:** ACTIVE · **Phase:** 3 — the runner, llama.cpp path (Phase 0 re-measure on b111 pending) · **Index:** [MASTER_PLAN](MASTER_PLAN.md)

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
- [x] Phase 1 — the council flow in each candidate library; **in-house errgroup chosen** (2026-09-25; results below)
- [x] Phase 2 — config and tweak (2026-09-26; results below)
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
- **Open for Phase 3: qwen35 is single-sequence in xollama.** Upstream's
  `parallelUnsafeArchitectures` (ollama#4165) includes qwen35, qwen35moe,
  qwen3next and others, so through xollama omnimerge serves one sequence at
  a time, and the council's parallel members would run one after another.
  Phase 1's engine runs bypassed xollama with `--parallel 6` and ran four
  members concurrently. Phase 3 must decide whether opencoti is exempt:
  whether its multi-sequence path for these architectures is correct, which
  has to be measured, not assumed.
- **Deploy note:** a server older than this build refuses a v4 config (by
  design), so writing a council needs the new build serving.

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
  config, so it never gets its own runner.
- 2026-09-26 — Schema v4 for the council, and no environment fallback: a
  council is a property of the model, never of the server.
