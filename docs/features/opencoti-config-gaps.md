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

> **Partly closed, 2026-09-19.** `session_id` now ships: derived in
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

## Order I would fix these in

Updated 2026-09-19. G3, G4 and G7 are closed; G5 is half closed.

1. ~~**G6**~~ — done, `GET /api/engine`.
2. **G5, the pool half** — `--polykv-max-pools` at launch and a pool lifecycle
   over `POST /polykv/pools {from_session}`. `session_id` is done and is what
   a pool attaches to.
3. **G1** — the per-model options carrier, now that `xollama.json` carries kv,
   slots, session and dca. What is left is moving the remaining launch settings
   into it rather than adding new ones beside it.
4. **G2** — an argv escape hatch for flags with no home. Demoted: G4 was the
   case that needed it and was closed without it, by writing the specific
   override rather than a general hatch. It is worth doing for flags nobody has
   modelled yet, not as a dependency of anything currently planned.
