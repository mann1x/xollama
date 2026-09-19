# Gemma-4 assistant drafters

A gemma-4 drafter is not a small standalone model. It is a head: the GGUF holds
`nextn.*` and `masked_embd_*` tensors, roughly 79 MB at Q8_0 for the E4B one,
and it declares

```
gemma4-assistant.requires_target_arch = gemma4
gemma4-assistant.nextn_predict_layers = 4
```

It carries no context of its own and has to be built against the target's. Load
one on its own and llama.cpp fails outright rather than degrading.

The five drafters live in `.opencoti/models/drafters/` and all declare
`gemma4-assistant`:

| Drafter | Target |
|---|---|
| `gemma-4-E2B-it-assistant-Q8_0.gguf` | `gemma-4-E2B-it-qat-UD-Q4_K_XL.gguf` |
| `gemma-4-E4B-it-assistant-Q8_0.gguf` | `gemma-4-E4B-it-qat-UD-Q4_K_XL.gguf` |
| `gemma-4-12B-it-assistant-Q8_0.gguf` | `gemma-4-12B-it-qat-UD-Q4_K_XL.gguf` |
| `gemma-4-26B-A4B-it-assistant-Q8_0.gguf` | `google_gemma-4-26B-A4B-it-Q4_K_M.gguf` |
| `gemma-4-31B-it-assistant-Q8_0.gguf` | `gemma-4-31B-it-Q4_K_M.gguf` |

## The two engines name the same driver differently

This is the whole reason the hook exists. Both engines run the drafter against
the target's context; they disagree only on the `--spec-type` spelling.

| Engine | `--spec-type` | How it gets there |
|---|---|---|
| llama.cpp | `draft-mtp` | `common/speculative.cpp` sets `cparams.ctx_other = ctx_tgt` and the MTP driver flips `is_mem_shared` when `llama_get_ctx_other(ctx_dft) == ctx_tgt` |
| opencoti | `draft-assistant` | its own driver, which logs `MTP assistant dual-context draft created (ctx_other=target, shared KV)` |

`draft-assistant` is not in upstream's enum, and opencoti's `draft-mtp` refuses
an assistant head with `Gemma4Assistant requires ctx_other to be set`. So the
value cannot be picked once and reused — it depends on which engine wins.

## How the fork resolves it

`externalDraftType` reads `requires_target_arch` off the drafter and returns
`draftTypeAssistant`, a semantic marker rather than a wire value. It also fails
early when the target is a different architecture, which is otherwise a
confusing runtime error.

Params are built before the engine hook runs, so `appendDraftArgs` emits the
upstream spelling `draft-mtp`. Once `engine.Launch` reports which engine it
chose, `retargetSpecType` rewrites the value to `draft-assistant` for opencoti
and leaves every other combination byte-identical. An assistant drafter never
gets `--spec-draft-backend-sampling`: that flag belongs to a drafter that
samples on its own.

`draft_n` needs no new plumbing. `api.Options.DraftNumPredict` (default 4,
zeroed in `server/routes.go` when no drafter is attached) already feeds
`--spec-draft-n-max`, exactly as it does for the qwen MTP drafters.

## What llama.cpp cannot load: the E-series drafters

Three of the five drafters work on stock llama.cpp. Two do not, and the reason
is in the GGUF, not in the version.

| Drafter | `masked_embd_*` tensors | `use_ordered_embeddings` | llama.cpp b10969 |
|---|---|---|---|
| `gemma-4-12B-it-assistant` | — | false | loads |
| `gemma-4-26B-A4B-it-assistant` | — | false | loads |
| `gemma-4-31B-it-assistant` | — | false | loads |
| `gemma-4-E2B-it-assistant` | 2 | true | fails |
| `gemma-4-E4B-it-assistant` | 2 | true | fails |

`src/models/gemma4-assistant.cpp` declares those two tensors with an empty
expected shape, which is not "unchecked":

```cpp
create_tensor(tn(LLM_TENSOR_MASKED_EMBD_CENTROIDS, "weight"), {}, TENSOR_NOT_REQUIRED);
create_tensor(tn(LLM_TENSOR_MASKED_EMBD_ORDERING),  {}, TENSOR_NOT_REQUIRED);
```

`check_tensor_dims` reads an empty `ne` as "every dimension must be 1", so a
real `masked_embd_centroids.weight [256 2048]` fails the check. Then the error
path calls `llama_format_tensor_shape(ne)`, whose first statement is
`ne.at(0)` — on the same empty vector. The genuine "wrong shape" message is
never printed and what surfaces instead is

```
llama_model_load: error loading model: vector::_M_range_check: __n (which is 0) >= this->size() (which is 0)
```

Upstream registers the arch but does not implement the centroid / ordered-
embedding variant: `load_arch_hparams` never reads `n_centroids`,
`centroid_top_k` or `use_ordered_embeddings` — all three of which the GGUF
declares. b10969 and master are byte-identical in this file, so bumping
`LLAMA_CPP_VERSION` does not help.

### The carried patch

`llama/compat/005-gemma4-assistant-unchecked-tensor-shape.patch` closes this.
Two hunks, both in upstream's own files:

1. `check_tensor_dims` returns early when `ne` is empty, so an empty expected
   shape finally means what both call sites intend by it. `{}` appears at
   exactly two `create_tensor` call sites in the whole tree, both of them the
   ones above, so nothing else can be affected.
2. `llama_format_tensor_shape` returns `"(unchecked)"` for an empty vector
   instead of throwing out of the error path. Worth sending upstream on its own
   merit: any GGUF carrying these tensors currently gets an `out_of_range`
   instead of a diagnostic.

Hunk 2 is defensive rather than load-bearing, and the build proves it: with hunk
1 in place the error path is only reachable with a non-empty `ne`, so the
compiler inlines `llama_format_tensor_shape` and eliminates the `"(unchecked)"`
branch as dead — the literal is absent from the built `libllama.so`. It stays in
the patch because the next caller to pass `{}` should get a diagnostic, not an
`out_of_range`.

A **third hunk was written and then removed after testing.** It logged a warning
when the centroids were present, to stop the patch turning "fails loudly" into
"loads and quietly drafts worse than trained". The E4B run showed it never fired
*and* was redundant — llama.cpp already prints, from `llama-model-loader.cpp:1196`:

```
model has unused tensor masked_embd_centroids.weight (size = 557056 bytes) -- ignoring
model has unused tensor masked_embd_ordering (size = 1048576 bytes) -- ignoring
```

naming both tensors and their sizes. It never fired because ollama's own compat
hook (patch 001) `should_skip_tensor` hides MTP-class tensors, so `create_tensor`
returns `nullptr` for them. Speculative decoding verifies every drafted token
against the target, so **output stays correct either way** — what degrades is
acceptance, i.e. speed. Implementing the ordered-embedding head is separate work
and is not attempted here.

### Verified

Measured on the 2-hunk patch, built in the pinned-glibc lane (both gates PASS):

```
common_speculative_init_result: loading draft model '…/gemma-4-E4B-it-assistant-Q8_0.gguf'
srv  llama_server: model loaded
srv  llama_server: listening on http://127.0.0.1:11581
```

with zero occurrences of `_M_range_check`. Target
`google_gemma-4-E4B-it-Q4_K_M.gguf` + the E4B assistant head, via
`-md … --spec-type draft-mtp`. Before the patch the same command fails at load.

Measured shapes, identical on both E-series heads, which is what the patch was
written against:

| Tensor | Shape | Type |
|---|---|---|
| `masked_embd_centroids.weight` | `[256 2048]` (`{n_embd, n_centroids}`) | `q8_0` |
| `masked_embd_ordering` | `[262144]` (`{n_vocab}`) | `i32` |

The patch retires on a `LLAMA_CPP_VERSION` bump that contains the fix; see
`llama/compat/README.md` and `docs/protocols/CARRIED-PATCHES.md`.

> Loading a drafter standalone is **not** a test of any of this. These heads
> have no context of their own, so `llama-server -m <assistant>.gguf` fails by
> design with `requires embedding_length_out to carry the target hidden size`.
> Exercise a drafter only through the target plus `--spec-type`.

## Measured — E4B drafter, both engines, through xollama serving

The first A/B that was *possible*: before patch 005 the llama.cpp column could
not be produced at all, because the E4B head failed to load.

Measured through `xollama serve`, not a hand-written `llama-server` argv, so the
`--spec-type` hook is exercised rather than bypassed. Model built with a
`DRAFT` line in the Modelfile; greedy (`temperature 0`, `seed 42`),
`num_predict 128`, `draft_num_predict 4`, 3 rounds per boot.

| Engine | `XOLLAMA_ENGINE` | resolved to | `--spec-type` on the wire |
|---|---|---|---|
| llama.cpp | `llamacpp` | `using stock llama-server` | `draft-mtp` |
| opencoti | `opencoti` | `using opencoti-llamafile` | `draft-assistant` |

That is the hook working end to end: one Modelfile, one `DRAFT` line, and the
wire value follows the engine that actually won.

| Prompt | llama.cpp | opencoti | Δ |
|---|---|---|---|
| merge two sorted lists | 0.5973 (89/149), len 3.34 | 0.6207 (90/145), len 3.43 | **+0.0234** |
| why spec-decoding is lossless | 0.3949 (77/195), len 2.57 | 0.4581 (82/179), len 2.82 | **+0.0632** |
| first ten primes | 0.7222 (26/36), len 3.89 | 0.7222 (26/36), len 3.89 | 0.0000 |
| what a KV cache is | 0.4659 (82/176), len 2.86 | 0.4500 (81/180), len 2.80 | −0.0159 |
| **aggregate** | **0.4946** (823/1664) | **0.5167** (837/1620) | **+0.0221 (+4.5 %)** |

Reading it honestly:

- **opencoti is ahead, but not by much** — +2.2 points, +4.5 % relative. Three
  of four prompts favour it or tie; one favours llama.cpp slightly. On this
  sample that is a real but modest edge, not a different league.
- **The prompts disagree far more than the engines do.** 0.39 to 0.72 across
  prompts, against a 0.02 engine gap. Anyone quoting a single acceptance number
  for "the E4B drafter" is quoting their prompt mix.
- **Identical on "first ten primes"** — byte-identical counts (26/36, len 3.89).
  Highly predictable output is where a weak drafter and a strong one converge.
- **Per-prompt figures repeat exactly across rounds 2 and 3 on both engines**,
  which is the determinism check passing; only llama.cpp's *first* round of the
  primes prompt differed (0.84375, 27/32), a first-boot effect that argues for
  discarding round 1 rather than averaging it in.
- **Neither engine implements the ordered-embedding head**, so both are drafting
  below what the model was trained for. The gap between these numbers and the
  model's potential is not the engine difference — it is the unimplemented
  centroid path, on both sides.

Acceptance is a speed metric, not a correctness one: every drafted token is
verified against the target, so both columns produce the same output.

## Measured

All at `--spec-draft-n-max 4`, greedy, seed 42.

| Pair | Engine | `--spec-type` | Acceptance | Mean len |
|---|---|---|---|---|
| E4B + E4B-assistant | opencoti | `draft-assistant` | 0.364 (75/206) | 2.44 |
| 12B + 12B-assistant | opencoti | `draft-assistant` | 0.517 (31/60) | 2.94 |
| 12B + 12B-assistant | llama.cpp b10969 | `draft-mtp` | 0.640 (16/25) | 3.29 |
| E4B + E4B-assistant | llama.cpp b10969 | `draft-mtp` | fails to load | — |

The llama.cpp numbers come from a stock b10969 build (CPU, `-ngl 0`) rather than
the repo payload, because the payload cannot currently be rebuilt on this host —
see the GCC note below.

## A separate build problem on solidPC

`cmake/local.cmake` passes `-DGGML_CPU_ALL_VARIANTS=ON`, so every microarch
variant is compiled. At b10969 the sapphirerapids variant needs AMX intrinsics
and therefore GCC >= 11; solidPC has GCC 10.2.1, and the build stops at

```
cc: error: unrecognized command-line option '-mamx-tile'
```

That is why `build/lib/ollama` is still dated 2026-09-02 although
`LLAMA_CPP_VERSION` moved to b10969 on 2026-09-15. It is unrelated to the
drafters — it just has to be fixed before any of this can be exercised through
`xollama serve`.
