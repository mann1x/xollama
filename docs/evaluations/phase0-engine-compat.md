# Phase 0 — engine compatibility, measured

**Date:** 2026-09-18 · **Host:** solidPC (RTX 3090, 24 GB) · **Verdict: PASS.**

The question Phase 0 had to answer: can opencoti-llamafile stand in for
`llama-server` under ollama's own launch contract, and does ollama's memory
accounting still work when it does? Both answered yes, with one narrow,
fixable drift.

## Method

No reconstruction of the argv by hand. ollama logs the exact command it
builds, so the real one was taken from a real load:

```
model   qwen3:4b  (Q4_K_M, 2.5 GB, ollama blob sha256-3e4cb141…)
engine  /usr/local/lib/ollama/llama-server            (stock baseline)
engine  opencoti-llamafile-0.10.5-c7-x86_64.llamafile (candidate)
```

Both were given **byte-identical argv** — only `--server` prepended for the
llamafile and a different port:

```
--model <blob> --port <p> --host 127.0.0.1 --no-webui --offline
-c 32768 -np 1 --log-verbosity 4 --no-log-prefix --no-log-timestamps
--no-jinja --chat-template chatml --cache-type-k q8_0 --cache-type-v q8_0
--flash-attn on -b 1024 -ub 1024 --context-shift --keep 4
```

## Result 1 — the argv is accepted whole

**Zero unknown-argument errors.** Every flag ollama passes was accepted,
including the ones most likely to be too new: `--no-webui` (accepted as a
deprecated alias for `--no-ui`), `--offline`, `--context-shift`,
`--flash-attn on` (tri-state), `--log-verbosity 4`.

It loaded **the ollama blob directly** from
`$OLLAMA_MODELS/blobs/sha256-…`, with no copy and no conversion. Health was
up in ~3 s.

## Result 2 — all four log scrapers match

This was the identified risk: ollama does not ask the engine how much memory
it used, it parses stderr (`llm/llama_server.go:2752+`). All four regexes hit:

| regex | stock | opencoti |
|---|---|---|
| `deviceFreeRegex` | `CUDA0 23853` | `CUDA0 23823` |
| `offloadedLayersRegex` | `37/37` | `37/37` |
| `fitOverflowingLayersRegex` | none (nothing overflowed) | none |
| `bufferSizeRegex` | 6 matches | 13 matches ⚠ |

opencoti's base carries `common_params_fit_impl` and `-ngl auto`, so the
memory-fit machinery ollama relies on is present and ran.

## Result 3 — VRAM accounting is exact, total memory drifts

The 13-vs-6 match count is **not** a 2× over-count. `memoryParsingWriter`
stores buffers in a map keyed by `{component, backend, kind}`, so a re-logged
allocation **overwrites** rather than accumulates. Reconstructing ollama's
real accounting:

| | stock | opencoti |
|---|---|---|
| `memGPU` | 5068.01 MiB | **5068.01 MiB — identical** |
| `memTotal` | 5456.97 MiB | 7885.92 MiB (**+2428.95**) |

`memGPU` — the number that drives GPU fit and `VRAMByGPU` — is exactly right,
because the converged values overwrite the intermediate ones on the same key.

The `memTotal` drift is **one stale map entry**: `llama_kv_cache / CUDA_Host /
KV = 2428.95 MiB`, written during rolling-KV's first pass and never
overwritten, because the later passes log only `CUDA0 KV`. It is host memory,
not VRAM (`isGPUBuffer` excludes `_Host`), which is why `memGPU` is unaffected.

### Why it re-logs: rolling-KV converging

With ollama's argv, opencoti's auto residency mode engaged and then optimised
itself to the same answer stock reached directly:

```
rolling-kv POSITION_WINDOW mode ON (--kv-residency-mode auto) — window 256 / 32768
  tactic table: POSITION_WINDOW=36  GPU_RESIDENT=0
two-pass — measured GPU compute buffer = 635 MiB; re-sizing (iter 0)
  tactic table: GPU_RESIDENT=36     POSITION_WINDOW=0
two-pass — measured GPU compute buffer = 500 MiB; re-sizing (iter 1)
  tactic table: GPU_RESIDENT=36     POSITION_WINDOW=0      ← converged
```

Final state per `/props`: `fully_resident: true`, `n_cells_resident: 32768`,
`n_layers_spilling: 0` — i.e. **the same placement as stock**. The extra log
lines are the optimiser showing its work, not a different outcome.

Note `--kv-residency-mode` has no `off` value (`{auto,head,window}`), so this
is not disabled by a flag. It must be handled on the xollama side.

### The fix

Two options, neither large:

1. **Reset-on-block (recommended).** When a new `llama_kv_cache` block starts,
   drop prior entries for that component before applying the new ones. A
   re-logged allocation then replaces the whole block rather than leaving
   orphans. Contained entirely in `memoryParsingWriter`.
2. **Reconcile from `/props`.** After load, read `opencoti.kv.effective` and
   trust it over the scraped log. Strictly better data, but it only exists on
   this engine, so it belongs in the adapter, not the shared parser.

Do (1) — it is engine-agnostic and would also protect against upstream
llama.cpp ever re-logging.

## Result 4 — the API surface is complete

| endpoint | result |
|---|---|
| `/health` | `{"status":"ok"}` |
| `/props` | full, **plus an `opencoti` block** (kv, residency, dca, sparse_attn, speculative, elastic_slots) |
| `/slots` | per-slot state **plus `opencoti` telemetry** (draft acceptance, tps_ewma, sampling placement) |
| `/completion` | `" Paris."` — ollama's primary generate path |
| `/v1/chat/completions` | works, `usage` populated |
| `/tokenize` | `{"tokens":[9707,856,965,3029]}` |
| `/detokenize` | round-trips |

`/tokenize` and `/detokenize` being live on the engine matters for the
tokenizer-endpoint work: `llm.LlamaServer.Tokenize` is already wired to them,
so `/api/tokenize` is routes-only on **both** engines.

`/props` and `/slots` carrying structured opencoti state is the Phase 3 answer
to "how do we read the knobs back" — it is introspection we do not have to
build.

## What this changes in the plan

- The engine swap is **confirmed low-risk**. No flag translation layer is
  needed beyond `--server`, the APE launch form, and `--gpu`.
- The scheduler keeps working unmodified: VRAM accounting is exact.
- One small, well-understood patch to `memoryParsingWriter` before Phase 2
  ships, plus a regression test built from the captured logs.
- Not yet measured: throughput, multi-slot (`-np > 1`), a Gemma-4 model with
  its parsers, and a model that actually overflows VRAM (the one case where
  `fitOverflowingLayersRegex` and rolling-KV spill really matter). Those
  belong in Phase 2's A/B, not here.
