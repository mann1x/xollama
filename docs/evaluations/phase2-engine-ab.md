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

**a. Our "stock llama.cpp" is not vanilla llama.cpp — it carries ollama's
compat layer, and the engine did not.** Four of the five failures are one cause,
and it is not architecture age. ollama's registry blobs are monolithic (text
weights and the projector tensors in one file — `gemma4:e4b` has 2131 tensors
while the text model declares 720) and carry converter-specific metadata
(`qwen35.rope.dimension_sections` with 3 elements). Vanilla llama.cpp rejects
them. Our baseline loads them because `llama/compat/compat.cmake` patches the
fetched llama.cpp with `001-llama-cpp-hooks.patch` and links four
`llama-ollama-compat*` sources into the llama targets in-process. Our own
baseline log proves the layer fired on the failing model
(`serve-llamacpp-1789753510.log`):

```
handle_gemma4_clip: detected Ollama-format gemma4 GGUF used as mmproj; translating
handle_gemma4: detected Ollama-format gemma4 GGUF; applying compatibility fixes
compat tensor transform: op=F16->F32 promote tensor=v.patch_embd.weight
```

So "both engines receive byte-identical params" was true and beside the point:
the two binaries were not built from equivalent llama.cpp. opencoti had no
ollama compat layer and behaved like unpatched llama.cpp. opencoti's
cross-check closes it — an unpatched upstream `llama-server` (b10268) fails on
these same four blob digests with the identical counts.

The compat layer is **upstream ollama's**, not this fork's: `llama/compat/` is
byte-identical to `upstream/main` here apart from one carried patch of ours
(`004-reasoning-budget-line-boundary`, PR #18212) and a README line.

> **A second wrong reading, corrected.** Each of those four failures also logs
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

Nothing here is fixable by changing what xollama passes. The engine has to
carry ollama's compat layer, which opencoti has now ported verbatim (their patch
0307), and the fix reaches us as a pin bump.

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
> in the hook Registry).
>
> **Re-taken 2026-09-20**, with that parser fixed, run as the `ollama` user (see
> `.claude/rules/solidpc-testing.md`). Raw:
> `/srv/ml/xollama-phase2/as-ollama/multislot/`.
>
> | engine | wall | aggregate (tok/s) | vs llama.cpp | per slot (tok/s) |
> |---|---|---|---|---|
> | llama.cpp | 3.27 s | 625.6 | — | 159.2, 159.2, 159.2, 157.8 |
> | opencoti c7 **r2** | 5.13 s | 399.2 | **−36.2%** | 106.0, 104.6, 104.6, 104.6 |
> | opencoti **build 18** (c8 line) | 3.25 s | **630.1** | **+0.7%** | 162.7, 162.5, 162.1, 162.3 |
>
> Two things change here. The provisional −27% was **optimistic**: measured
> honestly on the shipped release pin the concurrency deficit is −36.2%, not
> −27%. And it is **gone on the development line** — build 18 carries opencoti's
> patch 0311, and four slots reach parity with stock llama.cpp, marginally ahead
> of it and within noise of it.
>
> So the one real performance difference this A/B found is a property of the c7
> release cut, not of the engine. It is the strongest single argument for the
> move to c8: on the release pin xollama serves four concurrent requests at
> roughly two thirds of stock throughput; on the line we are integrating
> towards, it does not.

This is the one real performance difference the A/B found: single-stream parity
does **not** carry over to concurrency. All four opencoti slots reported an
identical 111.34 tok/s, which is consistent with stricter lockstep batching —
and the re-take above shows the same signature on r2 (104.6 x3) and its
disappearance on build 18, where the four slots sit at 162 each, above stock's
159. The lockstep is still there; what changed is that it no longer costs
anything.

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

> **Identified, 2026-09-19.** opencoti named this: the published c7 cut aborts
> as soon as a rolling window engages, on either cache layout — their bug-3369,
> a 0.10.5-port regression in the streaming-attention fallbacks, fixed by patch
> 0253 two days *after* c7 was published and never announced. Their own repro
> gives `fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT)`;
> ours surfaced earlier, in CPU KV placement, but it is the same unshipped fix.
>
> Mitigated as far as it can be. `llm/engine_defects.go` recognises this
> failure — by artifact name *and* the engine's own dying words, so it cannot
> blame an unrelated crash or a build that has the fix — and reports it as a
> known defect with the workaround attached, instead of leaving a CUDA assertion
> for someone to decode. It is diagnosis only: no retry, no downgrade. That is
> what `XOLLAMA_ENGINE_FALLBACK` is for and it stays opt-in.
>
> `llm/engine/pin.txt` pinned exactly that artifact —
> `3c907bc7511359054dbf55c9d2d69fb49324ef75bdbb45efa092c3facad8951e`, verified
> against the payload at `/usr/local/lib/ollama/` — so spill was a crash for
> our users, and the dev-build row below was the measurement of a fix nobody
> could install.
>
> **Re-pinned, 2026-09-20.** opencoti re-published c7 as r2: the same cut plus
> patch 0253 and nothing else. `main` now pins that commit
> (`3cf95ad25b7cb18c278cc6fb6a7d29ffea703b9e`, artifact
> `4f4102d6d8dd39bf794dee4f4d9000120766fd1fccc42090feff2e710a48104e`), and the
> known-defect row was retired with it on the strength of that changelog.
>
> **NOT resolved — retaken on r2 the same day, and it still aborts.** Raw:
> `/srv/ml/xollama-phase2/c7r2-overflow-clean/`.
>
> | arm | result |
> |---|---|
> | llama.cpp (control) | loads in 28.1 s, **56.8% resident** (23.9 of 42.0 GB), 2.71 tok/s |
> | opencoti c7 **r2** | **aborts in 2.7 s**: `ggml_new_object: not enough space in the context's memory pool (needed 118128, available 117760)`, while placing KV cache layers 30..53 on the CPU |
> | opencoti c7 **r2**, model that fits (`llama3:latest`) | loads, **75.5 tok/s** |
> | opencoti c7 **r2**, `LLAMA_ARG_KV_RESIDENCY_MODE=head` | **loads** in 34.8 s, 56.8% resident, **2.85 tok/s** |
>
> Byte-identical numbers to r1 — the same 368 bytes short, at the same point.
> The control reproduces the 2026-09-18 figure exactly (56.8% resident), so the
> host has not drifted, and r2 serving a fitting model at 75.5 tok/s rules out a
> broken artifact. **The abort is specific to the partial-offload path and patch
> 0253 does not fix it.**
>
> So the row covered *two* defects and only one was fixed. bug-3369's assertion
> is gone from r2; this is not that bug. The row is re-instated in
> `llm/engine_defects.go` against r2's sha256 with the spill signature only,
> because retiring it removed the only diagnosis a user gets for a failure that
> still ships. Reported to opencoti.
>
> **Narrowed, same day**, after opencoti asked (their bug-3515). The failing
> load says which tactic it chose:
>
> ```
> llama_kv_cache: rolling-kv POSITION_WINDOW mode ON (--kv-residency-mode auto)
>   - window 256 / 32768 cells, host tail 32512 (M2 head split forced off)
> ```
>
> and there is **nothing** between `layer 53: dev = CPU` and the abort — it dies
> on the next layer it tries to place. Forcing the other tactic with
> `LLAMA_ARG_KV_RESIDENCY_MODE=head` loads the same model on the same card and
> generates faster than stock llama.cpp does on this arm (2.85 vs 2.71 tok/s);
> its plan reads `GPU_RESIDENT=80 ... POSITION_WINDOW=0` and no KV layer is
> placed on the CPU at all. So the defect is in the POSITION_WINDOW path, not in
> partial offload as such — which is also why opencoti could not reproduce it on
> a roomy card, since `auto` only picks the window under real VRAM pressure.
> Their reading of the arithmetic: the KV metadata pool is budgeted 4 tensors
> per layer (4 x 80 x 368 = 117,760, exactly the `available`), and this path puts
> one more than that into the CPU buffer-type context.
>
> That makes the workaround a real one — it keeps the user on this engine
> instead of off it — and it is what `llm/engine_defects.go` now tells them.
>
> **Already fixed on the development line, measured 2026-09-20.** Build 18 of
> the c7 dev line (`a7a4e7da…` + its side-loaded `bef1ab64…` CUDA payload, both
> verified against `llm/engine/pin.txt` on the `dev` branch) loads the same
> model on the same card. Raw: `/srv/ml/xollama-phase2/as-ollama/b18-overflow/`.
>
> | arm | result |
> |---|---|
> | llama.cpp | loads, 56.8% resident, 2.71 tok/s |
> | opencoti c7 r2 | aborts in 2.7 s |
> | opencoti **build 18** | **loads** in 37.5 s, **76.0% resident**, **4.25 tok/s** |
>
> It is the same path, not an avoided one — the log still says
> `rolling-kv POSITION_WINDOW mode ON (--kv-residency-mode auto)` with the same
> window 256 / 32768 and host tail 32512, and the plan reads
> `POSITION_WINDOW=80`. It places 310 KV layers on the CPU where r2 died on the
> 54th, and aborts zero times. So opencoti's patch 0308 (rolling-KV metadata
> budget) is the fix, and it is already on the line c8 is cut from.
>
> It is also the fastest arm of the three, by a distance: 4.25 tok/s against
> stock's 2.71, because the position window keeps 76% of the model resident
> where stock's split manages 56.8%. This is the feature working as designed,
> on the exact case that was chosen to break it.
>
> One hazard found while measuring, worth knowing before trusting any r1-vs-r2
> comparison: the llamafile self-extraction cache is keyed by version string,
> and an in-place re-cut keeps that string, so r1 and r2 share
> `~/.llamafile/v/opencoti-0.10.5-c7/`. The first r2 run here replaced a
> `ggml-cuda.so` left by r1 *during* the load — the harness's
> `dso_changed_mid_axis` guard caught it and that run was discarded. The numbers
> above are from the re-run with a warm r2 cache and a stable DSO.

## Re-run on a dev build — NOT PINNABLE

> 2026-09-18, after opencoti's patches 0305–0310. Raw:
> `/srv/ml/xollama-phase2/dev-1831001-NOT-PINNABLE/`. Handed over as
> `/shared/dev/handover/2026-09-18-xollama-opencoti-devbuild-rerun.md`.

**This build cannot be pinned and nothing below is a release claim.** It is a
host binary (`/srv/ml/opencoti-dev/opencoti-0.10.5-c7-2609181831001`, sha256
`9c19b0e6…a766f9a`) that side-loads its CUDA backend from
`~/.llamafile/v/opencoti-0.10.5-c7/ggml-cuda.so`. There is no single file to
sha-pin, and the DSO that ran was five minutes newer than the host binary. It
also self-reports `opencoti-0.10.5-c7`, the same string as the release, so the
version is not a thing to gate on. `llm/engine/pin.txt` is unchanged and waits
for a self-contained clean-room artifact.

Both engines were re-measured in the same session with the fixed KV scraper, so
`memGPU` is correct on both sides this time.

| axis | Sep 3 release | dev 1831001 |
|---|---|---|
| compatibility | 3 / 8 | **8 / 8** |
| single stream | parity | parity (3744 vs 3852 prompt, 76.9 vs 78.3 gen tok/s) |
| Gemma-4 parsers | not measurable | **parity byte for byte** — same tool call, thinking split to identical 781/366 chars |
| 70B overflow | abort | **3.66 tok/s at 75.9% resident vs llama.cpp's 2.49 at 56.8%** |
| multi-slot `-np 4` | −27% (void) | **−25.6%**, 588.9 vs 438.3 tok/s aggregate |

The overflow case inverts: opencoti is now **47% faster than stock** on a
39.2 GiB model on a 24 GiB card, with 19 points more of it resident. That is
rolling-KV doing the thing it exists to do, and it is the first measured reason
to prefer the engine rather than merely tolerate it.

**Multi-slot is the one open deficit**, and it is no longer explainable by the
artifact's age — this build carries 0305–0310 and so should include the four
multi-slot host-overhead patches (0274–0277). The per-step timings locate it:

- opencoti's prompt eval **does not scale with token count** — 36, 55, 60 and 60
  tokens all take 114.4 ms, within 0.07 ms of each other, where llama.cpp's same
  four take 10.97–35.65 ms and track the count.
- decode is **perfectly lockstep** — all four slots within 0.03 ms of each other
  (4544.28–4544.31 ms) against llama.cpp's 0.5 ms spread — at 8.88 ms/token vs
  6.69, a uniform **+32.7%** per decode step that is exactly the aggregate gap.

Both point at a fixed per-batch host cost rather than per-token work.

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
- **Bump the engine pin.** Handed to opencoti as
  `/shared/dev/handover/2026-09-18-xollama-opencoti-phase2-findings.md`; they
  reproduced all four load failures and the 70B abort on a same-day `dev` build
  and fixed them (0307 compat-layer port, 0308 rolling-KV metadata budget, 0310
  projector reservation). No artifact to pin yet: their dev builds side-load the
  CUDA DSO and are not self-contained, so `pin.txt` waits for a clean-room
  `.llamafile`.
- **Any engine we route to must carry `llama/compat`.** That is now a
  compatibility requirement of the fork, not an implementation detail of one
  artifact, and it belongs in the pin's acceptance criteria.
- **Phase 0's low-risk verdict needs its scope written down**, not withdrawn: it
  was measured on `qwen3:4b`, a plain text model on a supported architecture, and
  it holds there.
- **Phase 3 knobs are premature** while `auto` can route a load into a failure.
  Compatibility gating comes first.
- Concurrency (−27%) is the only performance question worth carrying forward;
  single-stream parity means throughput is not the reason to pick either engine.
