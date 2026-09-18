# Feature — the opencoti-llamafile engine

> Status: **Phase 1 shipped** (routing, adapter, hook) — 2026-09-18.
> Not yet: the artifact downloader, and Phase 2's knobs. An artifact has to be
> placed by hand for now; see "Getting the binary".

## Why this is cheap

ollama 0.34 does not embed llama.cpp any more. `llm/server.go` says it
outright:

```go
// NewLlamaServer creates a new llama-server runner for the given model.
// All GGML models are served via the upstream llama-server subprocess.
```

So the engine is already a **subprocess behind an HTTP API**, selected by one
function and launched by one other:

| Seam | Location | Role |
|---|---|---|
| `FindLlamaServer()` | `llm/llama_server.go:340` | resolves the binary path |
| `startLlamaServer()` | `llm/llama_server.go:349` | builds argv, spawns it |
| `findLlamaCppBinary()` | `llm/llama_binary.go:39` | the candidate-path search |
| `SetupLlamaServerCommandEnv()` | `llm/llama_server.go:446` | library paths for GPU backends |

Everything above those — scheduling, memory estimation, the `/api/*` surface,
parsers, templates — is engine-agnostic. **Replacing the engine is replacing
a binary path and an argv, not rewriting ollama.**

macOS/MLX is a separate path entirely (`server/sched.go:588` →
`x/mlxrunner.NewClient`) and is not touched.

## Compatibility — measured, not assumed

opencoti-llamafile `0.10.5-c7` is upstream llamafile plus an additive patch
series, and llamafile embeds llama.cpp as a submodule. The two bases are
close:

| | base llama.cpp | date |
|---|---|---|
| ollama v0.34.2 | `b10760` (`7e4c0a96`) | 2026-08-15 |
| opencoti-llamafile 0.10.5-c7 | `c588c4f47` | 2026-07-22 |

**24 days apart.** Every flag `startLlamaServer` passes exists in llamafile's
`common/arg.cpp`: `--model --port --host --no-webui --offline -c -np --jinja
--mmproj --cache-type-k --cache-type-v --flash-attn --lora --model-draft -ngl
-t --no-context-shift --chat-template-kwargs`. The server API ollama talks to
(`/completion`, `/v1/chat/completions`, `/props`, `/slots`, `/tokenize`) is
upstream llamafile's, unchanged.

Verified locally: `sh opencoti-llamafile-0.10.5-c7-x86_64.llamafile --version`
→ `llamafile v0.10.5 (opencoti-0.10.5-c7)`.

Three deltas need handling, not redesign:

1. **`--server` is required.** A llamafile with no `--server` is a chat CLI.
   The adapter prepends it.
2. **APE launch.** On Linux without binfmt_misc registration the artifact is
   launched as `sh <file>`, not `exec <file>`.
3. **GPU selection differs.** llamafile takes
   `--gpu {auto,nvidia,vulkan,amd,apple,disable}` where stock llama-server
   infers from its loaded backend DSOs.

## The log scrapers — measured, mostly fine

`llamaServerRunner` does not ask the engine how much memory it used; it
**parses the engine's stderr** with four regexes (`llm/llama_server.go:2752`
onwards) to fill `memTotal`, `memGPU` and `gpuLayers`, which the scheduler
then uses for fit decisions:

```
deviceFreeRegex           using device (\S+) (...) - (\d+) MiB free
bufferSizeRegex           <name>: <dev> (model|KV|compute|output|RS) buffer size = N MiB
offloadedLayersRegex      offloaded (\d+)/(\d+) layers to GPU
fitOverflowingLayersRegex common_params_fit_impl: - ...: N layers ( M overflowing)
```

llamafile's base **does** contain `common_params_fit_impl`, `-ngl auto` and
the `MiB free` / `buffer size` emitters, so the surface exists. But opencoti's
patch series adds its own buffer lines (e.g. `KVarN buffer size = …`) that
these regexes will *not* match, so a PolyKV/rolling-KV allocation can be
invisible to the scheduler and be under-counted.

**Phase 0 ran this test — see
[`docs/evaluations/phase0-engine-compat.md`](../evaluations/phase0-engine-compat.md).**
Result: all four regexes match, and `memGPU` (the number that drives GPU fit)
is **identical** to stock, because the buffers live in a map keyed by
`{component, backend, kind}` and re-logged values overwrite rather than
accumulate. Only `memTotal` drifts, by one stale `CUDA_Host KV` entry that
rolling-KV's first pass leaves behind — host memory, not VRAM.

Fix before Phase 2 ships: reset a component's entries when a new block of the
same component starts, so a re-logged allocation replaces the block instead of
orphaning part of it. Engine-agnostic, contained in `memoryParsingWriter`, and
regression-testable from the logs Phase 0 captured.

## Design

One additive package, `llm/engine/`, plus the smallest possible hook.

```
llm/engine/
  resolve.go      which engine, for this platform + backend
  opencoti.go     argv translation, APE launch, version probe
  policy.go       the support matrix below
  policy_test.go
```

`XOLLAMA_ENGINE` selects: `auto` (default) | `opencoti` | `llamacpp`.

### Routing policy

`auto` routes to opencoti **only where it is tested**, and to stock
llama-server everywhere else. Untested is not "probably fine" — it is
llama.cpp.

| Platform / backend | Engine | Why |
|---|---|---|
| Linux x86_64 + CUDA | **opencoti** | primary, fully validated backend |
| Linux x86_64 + Vulkan | **opencoti** | parity-gated against CUDA |
| Linux aarch64 + CUDA (sbsa) | **opencoti** | shipped artifact |
| Linux / Windows CPU | **opencoti** | iqk FA kernels, always available |
| Windows x86_64 + CUDA/Vulkan | **opencoti** | `-win-gpu` artifact |
| **ROCm / Radeon** | `llama-server` | no tested opencoti backend |
| macOS (Metal / MLX) | untouched | MLX path, `x/mlxrunner` |
| anything else | `llama-server` | default deny |

The matrix lives in `policy.go` as data, with a test. Adding a backend to
opencoti is then a one-line change plus a test, not a hunt through `if`s.

### The hook

Exactly one surgical hook is expected, at `FindLlamaServer()` — it consults
the resolver, and falls through to today's behaviour when the resolver says
`llamacpp`. Argv translation happens in the adapter, reached from
`startLlamaServer`. With `XOLLAMA_ENGINE=llamacpp` the path is
byte-identical to upstream, which is what makes the A/B honest.

### Getting the binary

The opencoti repo is private; the engine is published on Hugging Face at
`ManniX-ITA/opencoti-llamafile`, and the artifacts are 0.7–2 GB — too big to
vendor in git and not ours to relicense.

**Implemented today: discovery of a pre-placed artifact.** `engine.Find` looks
at `XOLLAMA_ENGINE_PATH` first — set, it is used as given, and a missing file is
an error rather than a reason to keep looking, because silently ignoring an
explicit path is how you debug the wrong binary. Otherwise it takes the newest
`opencoti-llamafile-*.llamafile`/`.exe` across, in order:

```
~/.ollama/engines/
<ml.LibOllamaPath>/engines/
<ml.LibOllamaPath>/
```

Newest by mtime, not by version string: `0.10.5-c7` does not order under any
stock comparison and a wrong guess silently picks an older engine.

**Not implemented: the downloader.** The intent is unchanged — fetch on demand,
verify against `SHA256SUMS.composite`, cache in the ollama data dir, and treat a
mismatch as a hard error rather than a silent fallback. Until it exists, place
the artifact in one of the directories above.

## Phases

- **Phase 0 — verify (no code). DONE 2026-09-18, PASS.** Full result in
  [`docs/evaluations/phase0-engine-compat.md`](../evaluations/phase0-engine-compat.md):
  the argv is accepted whole, the ollama blob loads directly, all four log
  scrapers match, VRAM accounting is exact, and the whole API surface
  (including `/tokenize`) is live.
- **Phase 1 — pure replacement. SHIPPED 2026-09-18** (minus the downloader).
  `llm/engine/` — `policy.go` (the matrix, as data, with a test), `resolve.go`
  (`XOLLAMA_ENGINE` + policy → decision), `opencoti.go` (discovery, argv
  translation, APE launch) — plus the single `engine-select` hook in
  `startLlamaServer`.

  Verified on solidPC against a real load of `qwen3:4b`:

  | run | result |
  |---|---|
  | `XOLLAMA_ENGINE=llamacpp` | `using stock llama-server`, stock binary spawned, generate ok |
  | `XOLLAMA_ENGINE=opencoti` | `using opencoti-llamafile`, launched `sh <artifact> --server …`, generate ok |
  | unset (auto) | `opencoti-llamafile is tested on linux/amd64 with CUDA` → opencoti |
  | no artifact installed | falls back to stock, load still succeeds |

  The memory-parser fix this phase depended on is in: a component that starts a
  new block of buffer-size lines drops what it said last time, so opencoti's
  converging rolling-KV sizing no longer leaves a phantom 2.4 GB host
  allocation in `memTotal`. Both engines now account identically
  (`TestMemoryParsingRevisedAllocationSupersedesWholeBlock`).
- **Phase 2 — expose the engine.** Only now the unique features: PolyKV
  pools, rolling-KV window, DCA long context, MTP speculative decode, RYS
  layer duplication. Each one gets its own `docs/features/` note, a
  Modelfile `PARAMETER` and/or an `api.Options` field, and a default of
  *off*. The engine's own contract is "off means off, byte-identical to
  upstream" — xollama must not break that.

Phase 2 is deliberately not designed yet. The knob surface should be decided
against measurements from Phase 1, not guessed at now.
