# Feature — the opencoti-llamafile engine

> Status: **Phase 1 shipped** (routing, adapter, hook, build-time packaging)
> — 2026-09-18. Not yet: Phase 2's knobs.

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
| **NVIDIA below compute 7.5** | `llama-server` | engine has no code for it; see below |
| **ROCm / Radeon** | `llama-server` | no tested opencoti backend |
| macOS (Metal / MLX) | untouched | MLX path, `x/mlxrunner` |
| anything else | `llama-server` | default deny |

The matrix lives in `policy.go` as data, with a test. Adding a backend to
opencoti is then a one-line change plus a test, not a hunt through `if`s.

**Backend is not the only axis.** opencoti-llamafile's CUDA build emits gencode
for `compute_75/80/86/89/90` and nothing older (opencoti's
`vendors/sources/llamafile/llamafile/cuda.sh`), so Maxwell (5.x), Pascal (6.x)
and Volta (7.0) have no code in the artifact at all. They are still `Library:
"CUDA"` on `linux/amd64`, a row that *is* in the matrix above — so routing on
the backend name alone sends a Tesla V100 into an engine that cannot run it.

The failure is silent, which is why this is enforced rather than documented:
an artifact with no code for the device does not refuse to start, it falls back
to CPU, and the symptom is a load that runs at a tenth of the speed. So
`policy.go` carries `minCUDACompute = 75` and `engineDevices` in
`llm/llama_server.go` passes `ComputeMajor`/`ComputeMinor` through instead of
reducing every GPU to its `Library` string. A capability that discovery could
not read is treated as unsupported, not assumed modern: routing wrongly to
llama.cpp is logged, routing wrongly to opencoti is not.

Those cards are served by llama.cpp's **`cuda_v12`** payload, and only by it -
`llama_cuda_v13_*` in `llama/server/CMakePresets.json` floors at 75 exactly as
the engine does. That is why `cuda_v12` ships as its own release asset rather
than being dropped; see "Packaging".

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

**The runtime never downloads an engine.** The artifact is fetched once, at
BUILD time, and ships inside the installation package beside `llama-server`.
A model load must not be able to stall on a 650 MB fetch, and an installed
package must work with no network at all.

## Packaging

`llm/engine/pin.txt` pins the artifact: `repo`/`rev`/`tag` plus one
`bin <arch> <hf-path> <sha256>` row per published artifact. Two parsers read
it - `cmake/opencoti-fetch.cmake` at build time and `llm/engine/pin.go` via
`//go:embed` - and the format is deliberately trivial so they cannot drift.
`llm/engine/pin_test.go` holds both honest, including an invariant that every
platform in the routing matrix has an artifact row, and a guard that the file
stays ASCII (CMake's regex `.` does not match multi-byte UTF-8, which once let
a comment leak into the parser).

`cmake/opencoti-engine.cmake` stages the artifact into
`${OLLAMA_PAYLOAD_INSTALL_PREFIX}/${OLLAMA_LIB_DIR}`, which the catch-all
`install(DIRECTORY ... USE_SOURCE_PERMISSIONS)` at the end of
`cmake/local.cmake` already packages - so nothing had to be taught about the
engine. The pinned SHA256 is enforced on every path including an explicit local
file; a mismatch fails the build rather than shipping an unverified inference
engine. Linux releases build through Docker, where the `opencoti-engine` stage
does the same fetch and is copied into the `amd64` and `arm64` archive stages
(not `rocm`, which routes to llama.cpp).

Building without it, for Go iteration or offline:

```sh
cmake -B build . -DXOLLAMA_OPENCOTI_ENGINE=OFF            # no engine at all
cmake -B build . -DXOLLAMA_OPENCOTI_ENGINE_FILE=<path>    # verified local copy
cmake -B build . -DXOLLAMA_OPENCOTI_ENGINE_CACHE=<dir>    # reuse one download
```

### Two tiers, because CUDA would otherwise ship twice

GitHub refuses a release asset over 2 GiB, and the engine barely compresses -
1.18-1.30x measured, because its embedded CUDA fatbins are already compressed.
Bundling it into the assets as they stood put `OllamaSetup.exe` at ~2007 MiB,
41 MiB under the cap.

The cause is duplication: ollama's base asset carried `cuda_v12` *and*
`cuda_v13`, and the engine carries its own CUDA. Since opencoti serves every
CUDA device it can (7.5+), llama.cpp's payloads on those platforms are the
escape hatch, not the default. So `cuda_v12` - the larger of the two, and the
only one Maxwell/Pascal/Volta can use - moves to its own asset:

| asset | before | after |
|---|---|---|
| `ollama-linux-amd64.tar.zst` | 1361.4 MiB | **1093.0 MiB** |
| `ollama-windows-amd64.zip` | 1393.2 MiB | **1063.3 MiB** |
| `OllamaSetup.exe` | 1497.3 MiB | **~1130 MiB** |
| `ollama-*-cuda12.*` | - | **786.5 MiB** (linux amd64) |

The base assets end up smaller than upstream's while carrying the engine, and
headroom goes from 41 MiB to ~920 MiB. A CI step in `.github/workflows/release.yaml`
fails the release at 1900 MiB so the cap is never met at upload time.

**Anyone on a pre-Turing NVIDIA card needs the `-cuda12` asset**, on Linux and
Windows alike. Without it that hardware falls back to CPU.

## Phases

- **Phase 0 — verify (no code). DONE 2026-09-18, PASS.** Full result in
  [`docs/evaluations/phase0-engine-compat.md`](../evaluations/phase0-engine-compat.md):
  the argv is accepted whole, the ollama blob loads directly, all four log
  scrapers match, VRAM accounting is exact, and the whole API surface
  (including `/tokenize`) is live.
- **Phase 1 — pure replacement. SHIPPED 2026-09-18.**
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
