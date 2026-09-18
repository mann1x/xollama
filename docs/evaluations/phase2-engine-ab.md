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
| `gemma4:e4b` | ok | **fails** | `done_getting_tensors: expected 2131, got 720` |
| `gemma3:27b-it-qat` | ok | **fails** | `done_getting_tensors: expected 1247, got 808` |
| `mistral-small3.1:latest` | ok | **fails** | `done_getting_tensors: expected 585, got 363` |
| `qwen3.5:2b` | ok | **fails** | `qwen35.rope.dimension_sections has wrong array length; expected 4, got 3` |
| `llama3.1:70b-instruct-q3_K_S` | ok | **fails** | `ggml_new_object: not enough space in the context's memory pool` → `signal: aborted` |
| | **8 / 8** | **3 / 8** | |

Two causes, not three, and neither is in our argv.

**a. The pinned engine's llama.cpp base is older than these model
architectures.** Four of the five failures are one cause: the engine cannot
parse the model file. `qwen3.5` wants a 4-element `rope.dimension_sections` and
the engine reads 3; the other three abort in `done_getting_tensors` with a
tensor count that does not match the architecture it resolved.

> **A wrong first reading, corrected.** Each of those four failures also logs
> `Failed to load CLIP model from <the model blob>`, because ollama passes
> `--mmproj` pointing at the model file itself — `gemma4:e4b` has no projector
> layer in its manifest at all, and `llm/llama_server.go:778` special-cases
> `projectors[0] == modelPath`, so an embedded projector is a first-class
> upstream concept. It is tempting to read that line as the cause and to call it
> a convention mismatch. It is not. Re-running `gemma4:e4b` against the artifact
> directly **with `--mmproj` removed** fails identically
> (`expected 2131, got 720`), so the CLIP line is noise logged before the real
> failure. Both engines receive byte-identical params — `engine.Command` adds
> only `--server`, `--gpu` and `--log-verbosity` — and stock llama-server loads
> the same argv fine, because it is built from llama.cpp `b10969`
> (`LLAMA_CPP_VERSION`) while the artifact carries an older base.

**b. The overflow path aborts.** A separate cause, see §5.

Nothing here is fixable by changing what xollama passes. It is a pin bump, and
it will recur every time a model architecture lands in llama.cpp before it lands
in a released artifact.

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

> **Provisional — re-measure before acting on it.** During this run opencoti's
> KV lines were in the per-stream shape (`KV buffer (stream 0) size =`) that
> `bufferSizeRegex` did not match, so `memGPU` was short by 3584 MiB of CUDA0 KV
> and the scheduler planned against a wrong figure. No warning fired, because
> the model and compute lines still matched. The parser is fixed (`memory-scrape`
> in the hook Registry); the number is not re-taken.

This is the one real performance difference the A/B found: single-stream parity
does **not** carry over to concurrency. All four opencoti slots reported an
identical 111.34 tok/s, which is consistent with stricter lockstep batching.
Worth understanding before Phase 3 exposes any knob that changes batching.

## 4. Gemma-4 parsers

| engine | tool call | thinking channel |
|---|---|---|
| llama.cpp | 1 call, `get_weather({"city":"Berlin"})`, no markup leaked into content | separated, 781 thinking chars vs 366 content chars, no channel markup leaked |
| opencoti | **not measurable** — cannot parse `gemma4:e4b` (§1a) | — |

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

- **`auto` must fall back to llama.cpp when an opencoti load fails.** This is
  the one fix that is ours, and it is the right shape: `engine.Launch` already
  refuses to fail — a missing artifact or a bad `XOLLAMA_ENGINE_PATH` falls back
  to stock with the reason logged once — but that philosophy stops at the moment
  the runner is spawned. A load that fails *after* spawn is terminal today, so
  on this host `auto` turns a model that works into a model that does not. An
  architecture allow-list in `llm/engine/policy.go` is the wrong answer: it
  would have to be edited for every new architecture and would be wrong the day
  after a pin bump. Retry on stock instead, and log which model forced it.
- **Bump the engine pin, and report the gap.** The four parse failures are the
  artifact's llama.cpp base, not our argv. `llm/engine/pin.txt` is at
  `llamafile-v0.10.5+opencoti.c7`; stock is llama.cpp `b10969`. The four models
  are the reproduction to hand to opencoti.
- **Phase 0's low-risk verdict needs its scope written down**, not withdrawn: it
  was measured on `qwen3:4b`, a plain text model on a supported architecture, and
  it holds there.
- **Phase 3 knobs are premature** while `auto` can route a load into a failure.
  Compatibility gating comes first.
- Concurrency (−27%) is the only performance question worth carrying forward;
  single-stream parity means throughput is not the reason to pick either engine.
