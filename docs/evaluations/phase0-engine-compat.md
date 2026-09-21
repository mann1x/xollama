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

- The engine swap is **confirmed low-risk for the model measured here** --
  `qwen3:4b`, plain text, on a supported architecture. Phase 2 measured a
  wider set and found opencoti loads 3 of 8: see
  [phase2-engine-ab.md](./phase2-engine-ab.md). No flag translation layer is
  needed beyond `--server`, the APE launch form, and `--gpu`.
- The scheduler keeps working unmodified: VRAM accounting is exact.
- One small, well-understood patch to `memoryParsingWriter` before Phase 2
  ships, plus a regression test built from the captured logs.
- Throughput, multi-slot (`-np > 1`), a Gemma-4 model with its parsers, and a
  model that actually overflows VRAM were left to Phase 2's A/B, which has now
  run them: [phase2-engine-ab.md](./phase2-engine-ab.md). Single-stream is
  parity, `-np 4` is 27% slower on opencoti, and the overflow path aborts.

## Correction, 2026-09-19: "no flag translation layer is needed" has expired

The conclusion above — *"No flag translation layer is needed beyond `--server`,
the APE launch form, and `--gpu`"* — was true when it was measured and is not
true now. It is kept rather than edited, because the way it went stale is the
lesson.

ollama has since gained `--load-mode {none,dio,auto}`, one flag covering how
weights are read off disk, and it disables mmap **by default** for a
llama-server load. So `--load-mode none` is on essentially every argv we build.
`opencoti-llamafile-0.10.5-c7` predates that merge and still spells it
`--no-mmap`, and rejects the whole command line:

```
error: invalid argument: --load-mode
```

The effect was total: on the first live end-to-end run against a real engine
(eleven2go, Win11, RTX 3090, CUDA compute 8.6) the opencoti path could not load
a single model. Discovery, policy and artifact selection were all correct; the
argv was not. There is now a translation layer, in `Command`.

A second flag failed the same way and is still open: `--cache-type-k-swa` /
`--cache-type-v-swa` do not exist in c7 either — see bug-034 and mail #107.

**The rule this establishes.** A compatibility claim has a date, and both sides
move. Before shipping a flag to the engine, probe the pinned artifact rather
than reading anyone's documentation, ours included:

```sh
<artifact> --server <flag> <value> --model C:\nonexistent.gguf
# "no ... file found in zip archive"  -> accepted (parsing got past the flag)
# "error: invalid argument: <flag>"   -> rejected
```

Measured this way against c7, on 2026-09-19: `--gpu`, `--no-mmap`,
`--max-parallel`, `--max-parallel-tps-floor`, `--max-parallel-vram-reserve`,
`--kv-unified` and `--swa-seq-budget` are accepted; `--load-mode`,
`--cache-type-k-swa` and `--cache-type-v-swa` are rejected.

### Follow-up, same day

opencoti confirmed the second finding against their patch chain rather than
their prose: `--cache-type-k-swa` / `--cache-type-v-swa` are added by patch
0288, the c7 chain ends at 0244, and c7-r2 is c7 plus the single window-abort
fix. They are therefore in **no published cut** and first ship in c8. Twenty-one
other development-tree flags are in the same position; their flags document now
carries a generated availability table, which it did not when we read it.

The rule survives the correction unchanged, and is worth stating in its strong
form: **a flag in the vendor's source tree is not a flag in the vendor's
published binary.** Probe the artifact the pin names. And because a flag probe
cannot see values, probe those too — `-ctk kvarn3` against a bogus model path
should fail on the *type*, not on the file.

### Re-measured 2026-09-21 against build 19 (`66408c19`)

The pin moved from build 18 to `opencoti-0.10.5-c7-2609210611001` for patch 0320.
opencoti stated plainly that they ran no engine-compat pass on these bytes, so
the matrix was taken again here rather than inherited — the rule above, applied
to its own author.

Probed as the `ollama` user on solidPC, `--server <flag> <value> --model
/nonexistent.gguf`:

| Flag | build 19 |
|---|---|
| `--gpu nvidia` | accepted |
| `--no-mmap` | accepted |
| `--max-parallel`, `--max-parallel-tps-floor`, `--max-parallel-vram-reserve` | accepted |
| `--kv-unified`, `--swa-seq-budget` | accepted |
| `--cache-type-k-swa`, `--cache-type-v-swa` | accepted |
| `--kv-residency-mode auto` | accepted |
| `--load-mode none` | **rejected** |
| `--banana 1` (control) | rejected |

Unchanged from build 18. `--load-mode` is still the one rejection and is still
not a defect: opencoti spells it `--no-mmap`, and `Command` translates it. The
control being refused is what makes the rest of the column mean anything.

`--sparse-attn` is accepted, which is why it needed an advisory rather than a
defect row — see `llm/engine_args.go`. opencoti's bug-3524 makes it lose
retrievable content over a plain cache, silently, so nothing downstream can
notice and the engine never says a word.

### The 0320 fix, measured rather than inherited (2026-09-21)

The pin moved for one patch, so the patch was checked. Both builds served the
same qwen3 blob on the same 3090, as the `ollama` user, `--server
--reasoning-format deepseek`, and were asked `What is 2+2?` with
`reasoning_effort: "none"`:

| build | `reasoning_content` | `content` |
|---|---|---|
| 18 (`a7a4e7da`) | the entire answer, as thinking | **empty** |
| 19 (`66408c19`) | field absent | `"2 + 2 equals 4."` |

On build 18 thinking-off through the API did not switch thinking off: everything
went to the reasoning channel and a caller reading `content` got an empty
string.

**The inference drawn from that was wrong, and opencoti corrected it
(2026-09-21).** We also saw `reasoning_effort: "low"` come back with an empty
`content` and read it as the defect spanning the effort range. It is not:

- The engine's thinking-off predicate is the exact string
  `reasoning_effort == "none"`, or a request budget of 0
  (`reasoning_budget_tokens` / `thinking_budget_tokens`), or Anthropic
  `thinking: {type: "disabled"}`. **`low`, `medium`, `high` and `minimal` are
  no-ops** — deliberately, since `low` asks for less thinking and not for none.
  opencoti measured all four as byte-identical to each other. Upstream
  llama.cpp never reads the field at all.
- An empty `content` on a thinking-on turn is what **any** such turn returns
  when the token cap lands inside the think block: `finish_reason: "length"`,
  everything in `reasoning_content`. opencoti reproduced it with no effort field
  present at all.

So on build 18, `none` and `low` looked alike because `none` was broken into
being a thinking-on turn and `low` always was one — not because one defect
covered both. On build 19 `none` is fixed; `low` will still answer empty under a
short cap, and that is correct behaviour.

**Not yet re-verified here**: this repro was ad hoc and its `max_tokens` was not
recorded, so the cap explanation is opencoti's measurement rather than ours. The
source-level fact about the predicate is decisive on its own and is what
retires the "broader than reported" claim.

The consequence for anything that translates ollama's think levels: map only
`false` / `none` to `"none"`, and never expect `low` / `medium` / `high` to
differ. To ask for *less* thinking rather than none, send a request budget —
`reasoning_budget_tokens: N` force-closes the block and leaves room for the
answer. Whether xollama should map effort levels onto budgets is a
default-behaviour decision, not something to adopt silently.

One negative result worth keeping, because it nearly became a false conclusion:
the first attempt ran the same comparison through `--cli` instead of `--server`,
and **both** builds emitted a full `<think>` block despite `--reasoning off
--reasoning-budget 0`. That is not a disproof of the fix; `--cli` simply does not
apply the server-side reasoning controls. A repro that cannot reach the defect
proves nothing about it.
