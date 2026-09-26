# Council chat

A model whose config layer carries an enabled `council` answers every chat turn
with several calls to itself: a planner that routes, researchers and critics in
parallel, a synthesizer. User documentation: `docs/xollama/council.mdx`. Plan,
measurements and decision log: `plans/agentic-council-chat.md`.

## Where it lives

| piece | file | kind |
|---|---|---|
| schema (`council` block, v4) | `types/xollama/council.go` | additive |
| `tweak` fields, `show` rows | `cmd/tweak/council.go` | additive |
| the runner: `Decide` → `Direct`, or plan → researchers ∥ → critics ∥ → synthesizer, bounded revise loop, on `errgroup` | `internal/council/` | additive |
| serving a turn: members as in-process chat turns, streaming | `server/council.go` | additive |
| the PolyKV tree, windows, pressure, compaction | `server/council_polykv.go` | additive |
| the engine client: `Placement`, pools, sessions, `/kv`, resize | `llm/engine_council.go` | additive |
| entry: `if councilServes(...)` in `ChatHandler` | `server/routes.go` | hook `council` |
| `Placement` on the completion and native-chat requests, `CouncilPools` in the launch config, the placement applied to the engine body | `server/routes.go`, `llm/server.go`, `llm/llama_server.go` | hook `council` |
| the council kept out of the launch (`LaunchConfig`) | `server/routes.go` | hook `model-config` |

The Registry row `council` in `docs/protocols/UPSTREAM-SYNC.md` lists every
hunk in an upstream file.

## A turn

1. `councilServes` is false for a model without an enabled council, a request
   with no messages, with tools or with a `format`, and for a member's own turn
   (the `councilMemberKey` gin key; never a header, so no client can set it).
   Such a request takes upstream's path unchanged.
2. The planner's first call is route-only, under a `json_schema` grammar. A
   malformed decision goes to the council; the question is never lost.
3. Every member is an ordinary chat turn through `ChatHandler`, in process,
   thinking off, with its own seed; researchers and critics draw a temperature
   within `temperature_jitter`. The planner runs on the conversation's session;
   the others on `<session>~researcher-N`, `~critic-N`, `~synthesizer`.
4. The deliberation streams as thinking, one member holding the floor at a
   time; the answer streams as content, through upstream's
   `writeChatResponse`. The OpenAI and Anthropic shims need nothing.

## PolyKV (opencoti)

When the runner implements `llm.PolyKV` (opencoti with `polykv_subpools_v1`
and `kv_status_v1`, `CouncilPools > 0`, affinity on) a turn builds one tree:

- the planner is the **owner**: it books the window on the conversation's
  session (`num_ctx`, `num_ctx_min` = the floor);
- P1 (the conversation), P2r (+ plan), P2f (+ findings), P3s (+ critiques)
  are each built once, forked from the longest prefix already built, pinned
  to the owner. A layer is the rendered prompt cut at `councilSentinel` and
  must be a byte prefix of the member's own prompt, or the member runs
  unpooled;
- workers attach with `pool_id` and no window, and are closed when done; the
  pools are released newest first; the owner is never closed;
- on a recurrent-state engine (`/kv` has an `rs` block) each pool costs a
  state cell, so a finished stage's layer (no worker, no child, not P1) is
  released before the next is built, and the next forks P1; otherwise the
  synthesizer can wait forever for a cell (bug-118, 131k);
- before a turn the owner grows back toward its ask when nothing is refused;
  after a turn, under pressure, it shrinks (deferred) to
  `max(floor, used + reserve)`, only if that frees at least 4096 cells or 10 %;
- past `compact_at` of the grant, the older turns are summarised into the
  system message (the last three stay).

A PolyKV council launches with `councilPoolSeats` = `2 + 2 × rounds` pool
seats, so it is not launch-neutral; `polykv off` is.

Measured on b111 (omnimerge v4 IQ2_M, 16k, `-np 4`): computed prefill per
council turn 3,139 → 525 tokens, peak KV cells about −40 %, wall time at parity.

## Invariants

- **Off means off.** No council, or `XOLLAMA_ENGINE=llamacpp` with no council:
  `Placement` is nil, `CouncilPools` 0, and nothing in the request or the
  launch differs from upstream.
- **A council is a property of the model.** No environment fallback, no
  request field.
- **Pool id 0 is valid.** `Placement.PoolID` is `*int`; never test `> 0`.
- **Members never convene the council.** The gin key, not a header.
- **A council tag has its own runner.** Upstream's `ManifestDigest` is in the
  launch config, so any two tags over one blob swap the runner.

## Tests

- `internal/council/council_test.go` — routing, fan-out, seeds and jitter,
  the revise loop.
- `server/council_test.go` — `TestAModelWithoutACouncilIsUntouched`,
  `TestToolsAndFormatBypassTheCouncil`, `TestEveryParallelMemberHasItsOwnSession`,
  `TestParallelMembersReadOneAtATime`, `TestTheModelsOwnMessagesReachEachMemberOnce`.
- `server/council_polykv_test.go` — a fake engine records the tree:
  `TestACouncilOnPolyKVBuildsItsTreeOnce`, `TestPolyKVOffRunsTheMembersUnpooled`,
  `TestARecurrentModelReleasesEachStageItHasFinished`,
  `TestTheOwnerWindowFollowsThePressure`, `TestCouncilSeatsFollowTheRounds`.
- `llm/engine_council_test.go` — placement gating and pool 0, resize answers,
  session routing, pressure, seats.

Live runs are scripts under `/srv/ml/xollama-phase2/` run as `ollama`
(`council-p3.sh`, `council-p4ab.sh`, `council-cli.sh`, `council-131k.sh`), against the isolated
store, never the service's.
