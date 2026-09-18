# Feature — the opencoti-llamafile engine

> Status: **planned**. Nothing in this document is implemented yet.

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

## Open risk — the log scrapers

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

**This is the one thing to verify before writing any code.** It is cheap:
boot the artifact on a real model, capture stderr, run the four regexes over
it. Phase 0 below is exactly that, and its result decides whether the adapter
needs a log-shim or the engine needs a patch to emit an ollama-shaped line.

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
vendor in git and not ours to relicense. So: **download on demand, verify
against `SHA256SUMS.composite`, cache in the ollama data dir.** Mismatch is a
hard error, never a silent fallback. A pre-placed binary is honoured and not
re-downloaded.

## Phases

- **Phase 0 — verify (no code).** Boot the c7 artifact on one GGUF with the
  exact argv `startLlamaServer` builds. Capture stderr. Run the four regexes
  over it. Hit `/props`, `/slots`, `/completion`, `/v1/chat/completions`,
  `/tokenize`. Write down what differs. This either de-risks the whole thing
  in an afternoon or tells us the real cost up front.
- **Phase 1 — pure replacement.** `llm/engine/` + the one hook + the
  downloader. No new knobs, no API change. Success is: same model, same
  prompt, both engines, comparable output and no scheduler misfit.
- **Phase 2 — expose the engine.** Only now the unique features: PolyKV
  pools, rolling-KV window, DCA long context, MTP speculative decode, RYS
  layer duplication. Each one gets its own `docs/features/` note, a
  Modelfile `PARAMETER` and/or an `api.Options` field, and a default of
  *off*. The engine's own contract is "off means off, byte-identical to
  upstream" — xollama must not break that.

Phase 2 is deliberately not designed yet. The knob surface should be decided
against measurements from Phase 1, not guessed at now.
