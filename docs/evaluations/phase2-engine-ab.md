# Phase 2 — engine A/B, measured

> Measured 2026-09-18 on solidPC. Harness: [`scripts/phase2-engine-ab.py`](../../scripts/phase2-engine-ab.py).
> Raw results: `/srv/ml/xollama-phase2/results-{llamacpp,opencoti}.json`.

[Phase 0](./phase0-engine-compat.md) established that the opencoti-llamafile
engine loads, routes and accounts for memory the same way stock llama.cpp does,
and deliberately left four things unmeasured: throughput, multi-slot, a Gemma-4
model with its parsers, and a model that overflows VRAM. This measures those.

**The headline is not a throughput number.** On single-stream work the two
engines are within 2%. What the A/B actually found is that **opencoti could not
load five of the eight models tested, all five of which stock llama.cpp loads**
— and that on this host, `auto` routes to opencoti. Phase 0's "engine swap
confirmed low-risk" was true of the one plain text model it tested and is not
true in general.

## Bench

| | |
|---|---|
| GPU | NVIDIA RTX 3090, compute 8.6, 24576 MiB, driver 580.126.09 |
| CPU / RAM | 12 threads, 125 GiB |
| payload | `build/lib/ollama/cuda_v13` |
| engine artifact | `opencoti-llamafile-0.10.5-c7-x86_64.llamafile` (pin tag `llamafile-v0.10.5+opencoti.c7`) |
| selector | `XOLLAMA_ENGINE=llamacpp` vs `XOLLAMA_ENGINE=opencoti`, one server process each |

Each axis starts and stops its own server, so the two engines never share a
process or a loaded runner, and VRAM is allowed to drain between runs.

## 1. Compatibility — the finding that matters

One 1-token generation per model. `loaded` means the runner started and answered.

| model | llama.cpp | opencoti | opencoti's failure |
|---|---|---|---|
| `llama3:latest` | ok | ok | |
| `qwen2.5:1.5b` | ok | ok | |
| `tinyllama:latest` | ok | ok | |
| `gemma4:e4b` | ok | **fails** | CLIP load, `expected 2131 tensors, got 720` |
| `gemma3:27b-it-qat` | ok | **fails** | CLIP load, `expected 1247 tensors, got 808` |
| `mistral-small3.1:latest` | ok | **fails** | CLIP load, `expected 585 tensors, got 363` |
| `qwen3.5:2b` | ok | **fails** | `qwen35.rope.dimension_sections has wrong array length; expected 4, got 3` |
| `llama3.1:70b-instruct-q3_K_S` | ok | **fails** | `ggml_new_object: not enough space in the context's memory pool` → `signal: aborted` |
| | **8 / 8** | **3 / 8** | |

Three distinct causes, and they need different answers:

**a. The projector convention.** ollama passes `--mmproj` pointing at *the same
blob* as `--model` — its own llama-server reads "the projector is inside this
GGUF". opencoti's `mtmd` takes `--mmproj` as a standalone CLIP file, opens the
model as one, and fails on tensor count. This hits **every multimodal model**,
which is four of the five failures. It is the cheapest to fix and the one most
likely to be ours rather than the engine's.

**b. Architecture lag.** `qwen3.5` needs a 4-element
`rope.dimension_sections`; the pinned artifact's llama.cpp base reads 3. Nothing
to fix on our side — it is a pin bump, and it will recur every time a model
architecture lands upstream before it lands in the engine.

**c. The overflow path aborts.** See §5.

## 2. Throughput — single stream

`llama3:latest`, five iterations, each with a unique ~5000-token prompt so that
prompt eval is real work and not a prefix-cache hit.

| engine | prompt eval (tok/s) | generation (tok/s) |
|---|---|---|
| llama.cpp | 3878.5 | 78.38 |
| opencoti | 3700.4 | 77.05 |
| | **opencoti −4.6%** | **opencoti −1.7%** |

Median of five. Spread was tight on both (prompt 3830–4045 and 3636–3805;
generation within 0.7 tok/s). **Single-stream parity**, with opencoti a
consistent hair behind.

> An earlier run of this axis reported 344,000 tok/s prompt eval by sending the
> same prompt five times. That measured the prefix cache. The harness now
> prefixes a nonce per iteration.

## 3. Multi-slot — `-np 4`

`qwen2.5:1.5b`, `OLLAMA_NUM_PARALLEL=4`, four concurrent requests, each running
the full 512 tokens.

| engine | wall | aggregate (tok/s) | per slot (tok/s) |
|---|---|---|---|
| llama.cpp | 3.47 s | 589.7 | 149.3, 149.3, 150.1, 150.1 |
| opencoti | 4.73 s | 433.3 | 111.3 × 4 |
| | **+36% wall** | **opencoti −27%** | **opencoti −26%** |

This is the one real performance difference the A/B found: single-stream parity
does **not** carry over to concurrency. All four opencoti slots reported an
identical 111.34 tok/s, which is consistent with stricter lockstep batching.
Worth understanding before Phase 3 exposes any knob that changes batching.

## 4. Gemma-4 parsers

| engine | tool call | thinking channel |
|---|---|---|
| llama.cpp | 1 call, `get_weather({"city":"Berlin"})`, no markup leaked into content | separated, 781 thinking chars vs 366 content chars, no channel markup leaked |
| opencoti | **not measurable** — cannot load `gemma4:e4b` (§1a) | — |

The parsers are ours and run above the engine, so nothing suggests they would
behave differently. But this axis cannot be closed until §1a is fixed, and
recording it as "passed" would be a lie about what was run.

## 5. VRAM overflow

`llama3.1:70b-instruct-q3_K_S` — 39.2 GiB of weights on a 24 GiB card, so a
partial offload is forced.

| engine | result |
|---|---|
| llama.cpp | loads in 39.2 s, **56.8% resident** (23.9 of 42.0 GB), generates at 2.54 tok/s |
| opencoti | **aborts in 2.1 s**: `ggml_new_object: not enough space in the context's memory pool (needed 118128, available 117760)` while placing KV cache layers on CPU |

368 bytes short of a context pool, during CPU KV placement. This is precisely
the path Phase 0 named as unmeasured — where `fitOverflowingLayersRegex` and
rolling-KV spill actually do something — and it does not survive contact.

## A bug this campaign found in our own code

The engine hook prepended `--server --log-verbosity 5` to ollama's argv, but
`llm/llama_server.go:603` appends `--log-verbosity 4` of its own, and llama.cpp
takes the **last** occurrence. The engine had been running at verbosity 4 the
whole time, which is the level that filters out the buffer-size lines the
scheduler scrapes — bug-007 returning by a side door. The existing test asserted
that our flag must *precede* ollama's argv, encoding the bug as the expectation.

Fixed in `llm/engine/opencoti.go`: strip any `--log-verbosity` out of the stock
params and append ours after them, so the argv carries exactly one. Covered by
`TestCommandLogVerbosityWinsOverOllamas` and `TestCommandHandlesJoinedLogVerbosity`;
logged as bug-008.

## What this changes in the plan

- **The routing policy needs a model axis.** `llm/engine/policy.go` decides on
  platform and compute capability alone. On this host — linux/amd64, compute
  8.6, a "tested" platform — `auto` routes a multimodal model to an engine that
  cannot load it. A failed load is a worse outcome than a slow one, and the
  policy currently cannot express "not this model".
- **Fix the `--mmproj` convention first.** It is four of the five failures and
  the only cause plausibly on our side. Dropping `--mmproj` when it equals
  `--model` would let those models load text-only, but that silently disables
  vision; routing them to llama.cpp is the honest default until the engine reads
  an in-GGUF projector.
- **Phase 0's low-risk verdict needs its scope written down**, not withdrawn: it
  was measured on `qwen3:4b`, a plain text model on a supported architecture, and
  it holds there.
- **Phase 3 knobs are premature** while `auto` can route a load into a failure.
  Compatibility gating comes first.
- Concurrency (−27%) is the only performance question worth carrying forward;
  single-stream parity means throughput is not the reason to pick either engine.
