# Gap report — reaching opencoti's features from xollama

Written while mapping the user documentation
([`docs/xollama/opencoti-tuning.mdx`](../xollama/opencoti-tuning.mdx)), which is
where the user-facing half of this lives. This file is the engineering list: what
a user cannot configure today, why, and what the fix would be.

**The one-line summary.** The engine adapter passes ollama's argv through and
adds three flags. Everything else opencoti can do is reachable only because the
subprocess inherits the server's environment and every engine flag has a
`LLAMA_ARG_*` twin. That channel is server-global, silently inert wherever
xollama already passes the explicit flag, and does not exist at all for
per-request fields.

## How configuration reaches the engine today

| Plane | Mechanism | Status |
|---|---|---|
| Boot flags | `LLAMA_ARG_*` env twins, inherited via `cmd.Env = os.Environ()` in `SetupLlamaServerCommandEnv` (`llm/llama_server.go`) | Works, server-global |
| Boot flags xollama already sets | explicit flag beats env twin | **Twin is inert, with no warning** |
| Per-model | `xollama.json` — `types/xollama/config.go` | Carries `engine` and `draft.spec_type` only |
| Per-request | none | **No passthrough exists** |

`llm/engine/opencoti.go:152` `Command()` adds exactly `--server`,
`--log-verbosity 5` and `--gpu`, and strips any inherited `--log-verbosity`. It
has no `extraArgs` equivalent; opencoti's own TS adapter does.

## G1 — no per-model engine options

> **Closed, 2026-09-19.** The carrier is `types/xollama/config.go` and it now
> holds `engine`, `flash_attention`, `kv` (both halves plus the sliding-window
> ring), `slots` (dynamic, max, tps_floor, vram_reserve_mib, swa_seq_budget),
> `session` (affinity, pool, max_pools), `dca` and `draft.spec_type`. The
> original example — a model needing `kvarn3` plus a `q4_0` ring — was closed by
> G3; what this entry was really asking for was the rest of the settings that
> could only be said server-wide.
>
> The two added to close it:
>
> - **`flash_attention`**, because `OLLAMA_FLASH_ATTENTION` forces one answer
>   onto every model on the machine, and whether flash attention helps or breaks
>   is a property of the model and its cache. `"auto"` is a request to be
>   decided for rather than to switch on, so it still defers to the devices —
>   which is how a model opts out of a server that forced it on.
> - **`slots.swa_seq_budget`**, which opencoti specifically asked us to expose
>   per model rather than server-wide. It also feeds the memory prediction: a
>   budget the estimate ignored would free memory the scheduler then refused to
>   use, which would make the setting look inert.
>
> Documented as a whole in [`../xollama/model-settings.mdx`](../xollama/model-settings.mdx).
>
> **The generic `engine_options` object proposed below was deliberately NOT
> built, and should not be.** A model is pulled from a registry, often unread,
> and the engine's command line can write files and change where it loads from.
> A model able to add arguments to it is a model able to act on the machine that
> ran it. Every setting is a named field with a checked value for that reason,
> and an argument with no field is a gap to fill with a field. This also
> retires the model-carried half of G2.

`types/xollama/config.go` is the natural carrier and already survives a round
trip through push, pull and `ollama show --modelfile` on stock ollama. It has no
field for engine tuning, so a model that *needs* `kvarn3` plus a `q4_0` ring —
which is a property of the model, not of the server — cannot say so.

**Fix:** an `engine_options` object in the schema, rendered to argv by the
adapter. Unknown keys are already accepted by the reader, so a v1 consumer
tolerates a v2 model. Bump `version` only if the semantics of existing fields
change.

## G2 — no argv passthrough

There is no `XOLLAMA_ENGINE_ARGS` and no per-model equivalent, so any flag
without an env twin is unreachable. The one that matters is `--override-kv`
(`common/arg.cpp`, no `set_env`), which is how DCA is told to serve past the
model's declared context.

**Fix:** an append-only argv escape hatch, applied after ollama's params so
last-wins is on the user's side, with a log line naming what was appended. It
should refuse to override the flags the scheduler's memory accounting depends
on (`-c`, `-np`, `-ngl`, `--log-verbosity`) rather than silently letting a user
break estimation.

## G3 — K and V cache types cannot differ

> **Closed, 2026-09-19.** `llm/engine_launch.go` resolves the pair from the
> model's `xollama.json`, then `XOLLAMA_K_CACHE_TYPE` / `XOLLAMA_V_CACHE_TYPE`,
> then `OLLAMA_KV_CACHE_TYPE`, then unset. The sliding-window ring
> (`--cache-type-k/v-swa`) comes with it, added after the engine hook because
> stock llama.cpp has no such flag, and a load asking for a type or a ring that
> engine cannot serve is refused by name before the process starts. The resolved
> configuration is part of `LlamaServerConfig`, so `needsReload` already treats
> two models that disagree as two runners.

`llm/llama_server.go:425` writes one value into both:

```go
params = append(params, "--cache-type-k", launch.kvCacheType, "--cache-type-v", launch.kvCacheType)
```

`OLLAMA_KV_CACHE_TYPE=kvarn3` therefore works — the value is passed through with
no allowlist, which is the one piece of luck in this whole report — but
asymmetric K/V, which opencoti treats as first class, is not expressible. The
env twins cannot help: the explicit flag wins.

**Fix:** accept a `k,v` pair in the value, or add the pair to G1's
`engine_options`.

## G4 — DCA past the native context is blocked twice

> **Closed, 2026-09-19.** Both blocks are gone and the recipe is reachable.
>
> The clamp in `llm/server.go` now asks `DCAUnlocksContext` first, so it applies
> exactly as upstream wrote it unless dual chunk attention is carrying the load.
> The scheduler's `effectiveModelContext` takes the same answer, so the memory
> prediction sizes the cache for the context actually served rather than the one
> the file declares, and `needsReload` no longer clamps an incoming request back
> to a DCA runner's declared figure.
>
> G2 turned out not to be needed for this. Rather than a general argv escape
> hatch, `appendDCAArgs` writes the specific override the recipe calls for —
> `--dca on`, plus `--rope-scaling yarn` and
> `--override-kv <arch>.context_length=int:N` when and only when the request is
> past native. Four planes as promised: `XOLLAMA_DCA` /
> `XOLLAMA_DCA_CHUNK_SIZE`, a `dca` block in `xollama.json` that overrides them,
> and no request field or CLI switch because DCA is a property of the runner and
> the context length is already the per-request knob.
>
> Two refusals came with it, because the engine accepts `--dca on` on any
> architecture and silently does nothing on most of them: a load asking for DCA
> on stock llama.cpp is refused, and a load asking for more context than the
> model was trained on, on an architecture with no chunked route, is refused
> with the number that would have to change. Within the trained window the same
> case is only a warning. See [`../xollama/dca.mdx`](../xollama/dca.mdx).

1. `llm/server.go:127` clamps `opts.NumCtx` to the GGUF's own `context_length`
   with a `requested context size too large for model` warning, before the
   engine is launched. ollama reads that from its own parse of the GGUF, so an
   engine-side override cannot move it.
2. `--override-kv` has no env twin (G2), so the engine cannot be told a larger
   `context_length` either.

So `LLAMA_ARG_DCA=on` can be set and does nothing useful: the feature exists to
serve 4× the training context and the request can never ask for it. opencoti's
measured recipe — Gemma-4-A4B at 256k native, RULER-VT 0.964 at 256k and 0.916 at
1M — is not reproducible through xollama.

**Fix as delivered:** the clamp became "clamp unless dual chunk attention is
carrying this load", which is the narrow form of "unless the model declares an
extension mechanism" — narrow because the unlock is gated on the engine, on the
setting, *and* on the architecture actually having the route, so it cannot be
switched on by accident. G2 was not needed: the override the recipe wants is
written directly rather than exposed as a general escape hatch.

## G5 — no per-request passthrough, so PolyKV's request half is unreachable

> **Closed, 2026-09-19.** Both halves now ship. The pool half: the launch
> passes `--polykv-max-pools` (four planes — `XOLLAMA_SESSION_POOL` turns it on,
> `XOLLAMA_POLYKV_MAX_POOLS` or `session.max_pools` sizes it, default 2), and
> `llm/engine_pool.go` runs the lifecycle. A prefix is identified by
> `DerivePoolKey` — model digest, system messages and tool names, deliberately
> *not* the first user message, which is what separates a pool key from a
> session id and is the difference between one pool and one pool per
> conversation. The first request for a prefix is served normally and then
> snapshotted with `POST /polykv/pools {from_session, pin}`; later requests
> carry that `pool_id`. Pools are pinned and released on unload rather than left
> to the 60 s idle sweep, because a swept pool whose id we still hold is a
> silent full reprocess. Bounded by the same number the engine was given seats
> for, with LRU eviction. `--kv-unified` is emitted once for both features.
> The estimate was corrected too: `n_seq_max` is `parallel + polykv_max_pools`,
> so a reserved pool costs a sliding window exactly as a slot does.
>
> Not validated end to end against a live engine — the unit surface is covered
> and the HTTP shapes are taken from opencoti's `docs/features/polykv_api.md`,
> but no pooled load has actually been served yet.
>
> The `session_id` half, from earlier the same day: derived in
> `server/routes.go` from the stable head of a conversation, resolved in
> `llm/engine_session.go` against `xollama.json` then `XOLLAMA_SESSION_AFFINITY`,
> and emitted only when the load ran on opencoti. `pool_id` is plumbed and
> gated the same way but has no source yet — see the note at the end of this
> section. `overcommit` and `backend_sampling` are untouched.

opencoti parses `session_id`, `pool_id`, `shared_pool_slot`,
`shared_prefix_n_tokens`, `overcommit` and `backend_sampling` from the
completion body. xollama builds that body itself and has no channel for
engine-specific fields, so:

- **SharedKVPool cannot be used.** Pools can be created at boot
  (`LLAMA_ARG_POLYKV_MAX_POOLS`) and then sit unattached. This is the single
  biggest gap on this list, because the shared prefix is the reason PolyKV
  exists.
- **`session_id` is never sent**, so the engine's session→slot affinity never
  engages. This one is interesting beyond opencoti: it is the engine doing
  something ollama's own prompt-cache reuse would benefit from.
- `overcommit` cannot bypass the admission gate, which is `enforced` by default
  and answers 429 before prefill.

**Fix:** a narrow, named passthrough — not an arbitrary body merge. `session_id`
in particular could be derived by xollama rather than asked for.

**What remains for the pool half**, and it is launch-side rather than
request-side: the engine has to be started with `--kv-unified
--polykv-max-pools N`, which is G7's question, and something has to create a
pool (`POST /polykv/pools` with `from_session`, the documented agent path) and
remember its id per model. Until both exist, `session.pool` in a model config is
recorded and carried but changes nothing, which the user documentation says
plainly.

## G6 — engine introspection is not exposed

> **Closed, 2026-09-19.** `GET /api/engine` reads the engine back.
>
> With no arguments it lists every loaded model and which engine actually served
> it — not which one was asked for, which is the distinction that matters when
> routing declined a device. With `?model=` it proxies one endpoint from that
> model's engine: `props` (the default, and the only authority on the effective
> cache tier), `slots`, `metrics`, or `polykv/pools`.
>
> The engine's body comes back verbatim. Nothing renames or reinterprets its
> fields, because a translation layer over an evolving external schema goes
> stale silently — the exact failure this gap was about. The engine's own status
> is passed through too: a 404 from stock llama.cpp on `polykv/pools` is the
> honest report that the feature is absent, not a failure of ours.
>
> The endpoint name is chosen from a whitelist rather than composed by the
> caller, and is validated before a connection is opened. llama-server trusts
> whoever can reach it — its surface includes `POST /completion`,
> `/apply-template` and, on some builds, slot save and restore to arbitrary
> paths — so an arbitrary-path proxy would expose all of that to anyone who can
> reach the ollama port. Guarded by `TestValidIntrospectEndpointRefusesAnythingElse`
> and `TestEngineReadRefusesToDriveTheEngine`.
>
> It is not gated on opencoti, deliberately: three of the four endpoints are
> upstream's own, so the window works on stock too. It adds no work to the
> inference path.

`/props` (with its `opencoti` block and the **effective** KV state), `/slots`,
`/metrics` and `/polykv/*` live on the engine's private port. Nothing proxies
them, so "did my setting apply?" has no answer through xollama's API — the user
has to find the port in the log.

`/props → .opencoti.kv.effective` is the only authority on what auto-tier
decided, which makes this worse than a convenience gap.

**Fix:** surface the effective engine config on `/api/ps` or a fork endpoint.
Read-only, and only when the load routed to opencoti.

## G7 — `--kv-unified` is untested under xollama's sizing

> **Answered, 2026-09-19.** xollama now sets `--kv-unified` itself whenever
> dynamic slots are on, which is the default, together with `--max-parallel` and
> its two brakes (`llm/engine_launch.go`, `slotPlan`). The cell count is
> deliberately unchanged — ollama's `-c` is still context × parallel and its
> memory estimate still holds — so what moved is only whether one conversation
> may use all of them. ollama's own concurrency semaphore is resized to the
> ceiling after the engine is known, or the feature would be invisible. The
> engine's 429 + `Retry-After` is waited out in `llm/engine_admission.go` so a
> caller still sees queueing rather than an error.
>
> **What this gates:** less than first thought. The rolling-KV restriction was
> retracted — see the correction below. What remains is real but narrower: on
> iSWA models each slot reserves its own sliding window regardless of the shared
> pool, so a high ceiling costs real memory on Gemma-4. That is now priced into
> the memory prediction rather than only documented.

> **Correction, 2026-09-19.** This section claimed dynamic slots and the
> rolling-KV window were mutually exclusive. That is wrong, and the source it
> came from is wrong too: opencoti's `docs/llamafile-usage.md` §3.3 still says
> the window is "inherently incompatible with `--kv-unified`", and their own
> reply to our question repeated it. The restriction was lifted long ago and
> that paragraph was never updated; rolling-KV works with PolyKV pools on both
> the split cache and the shared pool. opencoti is fixing their doc.
>
> Lesson kept rather than buried: a claim read out of one project's prose is not
> evidence about its code, even when the project's own maintainer session
> repeats it. Ask for the flag, the test or the matrix row.

PolyKV pools and elastic slots both require `--kv-unified`, which changes what
`-c` and `--parallel` mean. xollama sizes `-c` as `NumCtx × numParallel`, which
is the same total either way — see `llm/engine_estimate.go` for why the shared
pool costs what the split cost.

**Remaining:** measure the pair on a real load — the sizing is reasoned, not
measured. And `--polykv-max-pools` is still not passed, so the pool half of G5
is still waiting on a pool lifecycle rather than on this.

## Not our bug — a report for opencoti

`docs/llamafile-flags.md` in the opencoti tree declares `common/arg.cpp` the
ground truth and is behind it: it lists the `turbo*` / `*_tcq` tiers as current
when `arg.cpp:345` marks them **deprecated and frozen**, does not mention the
`kvarn2..kvarn8` types that replaced them, and omits `--cache-type-k-swa` /
`--cache-type-v-swa` entirely — the flags their own KLD tables say carry most of
the win on iSWA models. Anyone configuring from that file today would pick a
deprecated tier and miss the ring.

## G5 leftover: `expect_len` — closed, by not needing it

Closed 2026-09-20, and not the way it was framed.

The question was how to cap a `from_session` pool at the shared prefix, since
such a pool holds the prefix *plus the first turn and the first answer* for its
pinned lifetime. opencoti's answer (#106) made the framing itself wrong:

- On a plain attention model an over-long pool is merely wasteful, as assumed.
- On a **sliding-window** model it is not. Once the session that built the pool
  moves on, the pool is sole owner of its cells and the ones outside its own
  window become reclaimable. A later conversation attaching at a shorter prefix
  needs exactly those cells, and the attach guard only checks the pool's maximum
  position. The result is a hole: degraded attention with no error and no WARN.

So `expect_len` was never the fix. An *exactly* right `expect_len` still has the
exposure, because the source slot may already have recycled the tail by the time
the snapshot is taken.

The pool is now built from the shared prefix itself — the materialising
`{"tokens": [...]}` create — which is correct on plain, sliding-window and
recurrent architectures alike, and needs no length declared to anyone.

**How the prefix is obtained**, which is what made this look hard: the boundary
was said to be unknowable because on the native chat path the engine renders the
template. It is knowable without knowing the template at all. Two conversations
that share a prefix are rendered by whoever owns the template — ollama on the
Completion path, the engine via `/apply-template` on the Chat path — tokenised
by the engine, and the longest common **token** prefix of the two is the answer.

Comparing tokens rather than strings disposes of the BPE-merge tail: a run of
whole tokens that two real prompts both begin with is, by construction, a valid
prefix of both. One option does matter — `/tokenize` defaults `add_special` to
**false** while the serving path tokenises with it **true**, so leaving it out
would shift every token by the BOS and match at zero.

It does **not** dispose of opencoti's caveat about cutting back to a template
boundary, which this document previously claimed. Two real conversations share
the template *and* whatever their own first messages happen to have in common:
"What is the capital of France" and "What is two plus two" agree for two tokens
past the point where the template stops. That overshoot is the origin of the
`347` / `346` discrepancy in the measurement below.

So the boundary is measured, not inferred. Before a pool is created, the
conversation's system prompt and tools are rendered against several stand-in
user messages that share no opening, and only the tokens every rendering agrees
on are kept — `templateBoundary` in `llm/engine_pool.go`. The result is then
intersected with what the two real prompts shared, so it is guaranteed to be a
prefix of prompts the engine actually saw even if the last template token merges
with what follows it. The measurement can only come out short, and short is the
safe direction.

Which is the difference between wasteful and broken. On plain attention an
over-long pool costs a few cells. On a **recurrent or hybrid** model
`server-context.cpp` takes the share only when `P == pool_max + 1` and the
prompt is strictly longer — the match must cover the pool entirely — so a pool
two tokens too long can never be matched by anything. That, not the nature of
recurrent state, was what kept these models out of pooling.

## The pool path, measured end to end (2026-09-20)

After the redesign, against the same live c7:

```
polykv: created pool 0 (seq 4, prefix_len 347, source 'tokens/slot')
polykv P7: new session 'xo-...' admitted to pool 0
polykv-pools: pool 0 match P=344 not better than cached n_past=347 — skipping share
polykv-pools: attached pool 0 — shared 346-token prefix (n_past -> 346)
```

Every line of that is the design working:

- `source 'tokens/slot'` — the materialising create, not a session snapshot.
- `prefix_len` equals what two conversations were observed to share, and the
  first conversation alone created nothing.
- `P == 346` on the attach, which is the proof that our tokenisation matches the
  engine's exactly — `add_special: true` was the option that decided it.
- `prefix_len 347` against a share of `346` is **one token of overshoot**, and
  it was reported here as a clean success. opencoti caught it: the two sampled
  conversations agreed one token further than the template did. Harmless on this
  attention model, fatal on a recurrent one, and now fixed by measuring the
  boundary rather than taking the pair's word for it.
- The `skipping share` lines are the documented `P > n_past` rule and are not
  failures: those slots already held more of the prefix than the pool offered.
  The share is taken as soon as a request lands on a slot that does not.

Nothing is wasted: under the old design the same prefix gave a pool of 50 tokens
serving a share of 29.

Finding it required fixing one more thing first. The engine numbers pools from
**zero**, and xollama spelled "attached" as a non-zero int — a `pool > 0` guard
and an `omitempty` int on the wire, either of which alone drops pool 0. The
engine's first pool could never be attached to, silently, because an unattached
pool looks exactly like a pool nobody is using.

## The measurement, taken 2026-09-19

It has now been run. Host: eleven2go, Windows 11, RTX 3090 (CUDA compute 8.6),
artifact `opencoti-llamafile-0.10.5-c7-win-x86_64-gpu.llamafile.exe`, sha
`19ff1d87…`, matching the pin.

| What | Result |
|---|---|
| Engine discovery and policy | chose the artifact unprompted, "tested on windows/amd64 with CUDA compute 8.6" |
| Elastic slots (G7) | engine grew to 4 slots, each reporting the full `n_ctx=32768` — the shared pool, not a split |
| `--kv-unified` | emitted once, for both features |
| DCA beyond native (G4) | `num_ctx=65536` on a 40960-trained qwen3; engine confirms `validate_override … = 65536`; chunk **40960**, derived from the ORIGINAL trained context |
| `GET /api/engine` (G6) | all four endpoints live; `metrics` returned as text; `completion` refused 400 |
| Recurrent gate | fired on qwen35; engine corroborates with `qwen35.attention.recurrent_layers` and `llama_memory_recurrent`. Since the boundary is measured, these models are no longer excluded — they are pooled from the chat path only |
| PolyKV attach (G5) | pool created, `pool_id` sent, engine matched the prefix token-exactly at P=29 |
| Off-path `XOLLAMA_ENGINE=llamacpp` | stock `llama-server.exe`, **zero** xollama flags, `--load-mode` left untranslated |

Three things the measurement found that no amount of reading would have:

1. **`--load-mode` made every opencoti load fail.** Fixed; see the correction
   appended to `phase0-engine-compat.md`.
2. **`--cache-type-k-swa` / `--cache-type-v-swa` do not exist in c7.** Closed,
   bug-034. opencoti confirmed against the patch chain (#108): added by patch
   0288, and the c7 chain ends at 0244 — so they are in no published cut and
   first ship in c8, which has no date. The spelling and the probe were both
   right; their flags doc described the development tree without saying so.
   Such a load is now refused up front naming the pinned tag, gated on the pin's
   cut number so the refusal lifts by itself when we re-pin.
3. **Pool seats were reserved for models that can never use them.** Fixed by
   `effectivePoolCount`.

And one the engine told us in its own words: a pinned `from_session` pool
measured `prefix_len=50` against a share of `P=29` — 21 cells pinned for the
pool's life that nothing can ever match. That is the `expect_len` cost above, in
numbers, and together with opencoti's SWA-residency reading (their bug-3494) it
is why the pool path is moving to the `/apply-template` + `{tokens}` create
rather than gaining an `expect_len` argument.

Everything else in this report is closed.
