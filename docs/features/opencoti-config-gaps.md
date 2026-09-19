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

**Fix:** G2 gives the override; the clamp then needs to become
"clamp unless the model declares an extension mechanism", which is a real design
question and not a one-liner. Worth an explicit decision rather than a patch.

## G5 — no per-request passthrough, so PolyKV's request half is unreachable

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

## G6 — engine introspection is not exposed

`/props` (with its `opencoti` block and the **effective** KV state), `/slots`,
`/metrics` and `/polykv/*` live on the engine's private port. Nothing proxies
them, so "did my setting apply?" has no answer through xollama's API — the user
has to find the port in the log.

`/props → .opencoti.kv.effective` is the only authority on what auto-tier
decided, which makes this worse than a convenience gap.

**Fix:** surface the effective engine config on `/api/ps` or a fork endpoint.
Read-only, and only when the load routed to opencoti.

## G7 — `--kv-unified` is untested under xollama's sizing

PolyKV pools and elastic slots both require `--kv-unified`, which changes what
`-c` and `--parallel` mean, and which is mutually exclusive with the rolling-KV
window. xollama sizes `-c` as `NumCtx × numParallel` for the split layout. The
env twin exists, so a user can switch it on — into a configuration nobody here
has measured.

**Fix:** measure it, then either support it deliberately or refuse it with a
message.

## Not our bug — a report for opencoti

`docs/llamafile-flags.md` in the opencoti tree declares `common/arg.cpp` the
ground truth and is behind it: it lists the `turbo*` / `*_tcq` tiers as current
when `arg.cpp:345` marks them **deprecated and frozen**, does not mention the
`kvarn2..kvarn8` types that replaced them, and omits `--cache-type-k-swa` /
`--cache-type-v-swa` entirely — the flags their own KLD tables say carry most of
the win on iSWA models. Anyone configuring from that file today would pick a
deprecated tier and miss the ring.

## Order I would fix these in

1. **G5** (`session_id`, then pool attach) — unlocks the feature PolyKV is named
   for, and `session_id` pays off on the stock path too.
2. **G2 + G3** — cheap, and between them make the whole KV stack expressible.
3. **G6** — without it nothing above is verifiable from the outside.
4. **G1** — the right home for all of it once the argv side works.
5. **G4**, then **G7** — both need a decision before a patch.
