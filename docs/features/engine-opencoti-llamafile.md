# Feature — the opencoti-llamafile engine

> Status: **Phase 1 shipped** (routing, adapter, hook, build-time packaging)
> — 2026-09-18. Phase 2's A/B is measured:
> [`docs/evaluations/phase2-engine-ab.md`](../evaluations/phase2-engine-ab.md).
>
> **It found that `auto` can route a load into a failure.** opencoti loads 3 of
> 8 tested models where llama.cpp loads 8 of 8 — because our baseline is not
> vanilla llama.cpp: it links ollama's `llama/compat` layer in-process, which is
> what reads ollama's monolithic registry blobs, and the engine had no
> equivalent. Its VRAM-overflow path also aborts. **Both fixed on their side:** a
> local, non-pinnable re-run on dev build 1831001 loads 8 of 8 and turns the
> overflow case into a 47% win over stock. Multi-slot remains −25.6%. Handed to
> opencoti as
> `/shared/dev/handover/2026-09-18-xollama-opencoti-phase2-findings.md`; the
> fork will move to a c8 artifact and re-run the same axes.

## `XOLLAMA_ENGINE_FALLBACK` — opt-in, and off by default

When set, a load that fails on opencoti is retried once on stock llama-server.
Unset, it fails.

Off is the default on purpose. An automatic fallback reads like a kindness and
is not one: every model the engine cannot serve would load anyway, with none of
the engine's behaviour, and nothing in the response would say which engine
answered. An A/B against vanilla would quietly become an A/A, and the
compatibility gap this fork exists to measure would stop being visible at all.
Failing is the honest default; opting in is a statement that availability
matters more than knowing which engine served the request.

The retry is modelled on upstream's own `retryWithMMProjCPUOffload`: stop the
process, set `forceStockEngine` on the launch config, reset load accounting,
start again. It happens at most once per load, and logs a `WARN` naming the
model and the variable that enabled it.

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

MLX is a separate path entirely (`server/sched.go:588` →
`x/mlxrunner.NewClient`) and is not touched. GGUF models on Apple silicon go
through this seam like any other: see "macOS" below.

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
| Linux aarch64 + CUDA (`sbsa`: DGX Spark 12.1, Jetson Thor 11.x) | **opencoti** | taken with c11, built blind: never run by opencoti or here. Orin (8.7) and every other arm64 card: `llama-server` |
| Linux / Windows CPU | **opencoti** | iqk FA kernels, always available |
| Windows x86_64 + CUDA/Vulkan | **opencoti** | `-win-gpu` artifact |
| **NVIDIA below compute 7.5** | `llama-server` | engine has no code for it; see below |
| **ROCm / Radeon** | `llama-server` | no tested opencoti backend |
| macOS arm64 + Metal / CPU | **opencoti only** | started through `ape-macos-aarch64`; no stock `llama-server` ships on macOS; see "macOS" |
| macOS x86_64 | not built | opencoti publishes no Intel Mac files, and there is no stock engine |
| MLX models | untouched | MLX path, `x/mlxrunner` |
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

### Decision models

A decision model (`decision.type` in its GGUF, capability `decision`) is scored
by `/v1/systemone` in one of two ways, and only one of them needs anything from
the engine:

- **From token probabilities** (`nimble`): `/completion` with `n_probs` and a
  logit bias. opencoti serves it as stock does; measured on c9, the answers are
  within 0.006 of stock's.
- **Through the Clef head** (`decision.type = clef`: `clef`, `clef-flash`):
  extra tensors in the model and `score_fields` on `/embedding`, which upstream
  compiles into `llama-server` from `llama/clef/`. opencoti through c9 has
  neither, and does not load such a model ("wrong number of tensors; expected
  549, got 427").

So a Clef model is served by stock `llama-server` while the pinned engine does
not declare the feature `clef_score_v1`: `clefEngine` in
`server/decision_engine.go` names `llamacpp` in the launch config, which is the
path a model's own `engine: llamacpp` takes, logged once per model. An engine
the model states itself is kept. Nothing is refused, and the day the pin carries
`feature clef_score_v1` the function returns the config untouched (opencoti
took the head for c10). Gate: `scripts/gates/decision.sh` (G4).

### The hook

Exactly one surgical hook is expected, at `FindLlamaServer()` — it consults
the resolver, and falls through to today's behaviour when the resolver says
`llamacpp`. Argv translation happens in the adapter, reached from
`startLlamaServer`. With `XOLLAMA_ENGINE=llamacpp` the path is
byte-identical to upstream, which is what makes the A/B honest.

### Getting the binary

The opencoti repo is private; the engine is published on Hugging Face, and the
artifacts are 0.7–2 GB — too big to vendor in git and not ours to relicense.

**Which repo is a variable, and nothing in the tree may assume it.** There are
two, and the pin in `llm/engine/pin/` is the only place that says which one
this branch follows:

| Channel | Repo | What it holds |
|---|---|---|
| `release` | [`ManniX-ITA/opencoti-llamafile`](https://huggingface.co/ManniX-ITA/opencoti-llamafile) | cut releases, stable, backed up |
| `dev` | [`ManniX-ITA/opencoti-llamafile-dev`](https://huggingface.co/ManniX-ITA/opencoti-llamafile-dev) | snapshots published on request, for integration testing only |

xollama is built for the scope of the **next** cut, not the scope of whatever
is published today, so the dev branch pins a dev snapshot whenever one is in
flight and the release repo when nothing is. The pin carries a `channel`
directive so a dev build can never be mistaken for a release by omission.

Two properties of that arrangement drive the pin format:

- **A dev build is tagged with the PREVIOUS release tag** (`opencoti-0.10.5-c7-<id>`).
  That is a build id, not a claim about which cut it is. Deriving capabilities
  from the tag would therefore refuse the very flags the snapshot was pinned to
  test, which is why `feature` rows are declared rather than inferred.
- **A release is re-cut in place.** The c7 r2 re-cut replaced all five host
  binaries under their existing names on `main`. So `rev` is a commit sha and
  never a branch; `ParsePin` rejects anything else.

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

### A health check that could not reach the engine

Upstream fails a request on the first failed dial to the engine's `/health`.
On opencoti, while the engine process is still running, a check that could not
reach it (a dial error, or refused) is tried up to four more times, with
backoff from 0.25 to 2 s and a Warn each time (`engine-health-retry`,
`llm/engine_health_retry.go`). Measured cause, on eleven2go with a0968aea: one
loopback dial to a live engine timed out, the engine answered three seconds
later, and a council turn was lost. Stock llama.cpp keeps upstream's single
failure.

On Windows an opencoti engine is tied to the server that started it
(`engine-lifetime` hook, `engine.BindLifetime` in
`llm/engine/lifetime_windows.go`): every engine, LLM or media, goes into one
job object with kill-on-close, so a server that is killed or crashes takes its
engines with it. Windows does not do that by itself, and an orphaned engine
holding a model on an AMD discrete card through Vulkan hung the display driver
within seconds (eleven2go, 2026-10-04: watchdog dump `0x141`, once the card
gone until a reboot, once the machine frozen), while an engine terminated at
once left the card fine. A binding that fails is a Warn, never a failed load.
Stock llama.cpp is started as upstream starts it.

## Packaging

The pin is opencoti's **pin format 2**, in `llm/engine/pin/`. The engine and
everything that travels beside it are published as **components**, each with
its own version, its own pin file and its own immutable commit: `engine`,
`cuda`, `cuda12`, `sbsa`, `vulkan`, `macos`, `media`. An **index** composes
them. opencoti publishes its recommended index per channel; xollama keeps its
own, `llm/engine/pin/index.txt`, and vendors the component pin files it names
byte-identical beside it (`sbsa`, the CUDA library of Linux aarch64, is taken
since c11; its `sass` list is its own, `Pin.SBSASASS`, and its `121` is
`sm_121a`, which serves compute 12.1 exactly). The build reads only this directory, then fetches each payload
file by its component's `repo` / `rev` / `path` and verifies sha256 and size.
No pin is fetched at build time.

| File | Whose | What it states |
|---|---|---|
| `index.txt` | xollama's | `channel`, `tag`, and per component the pin file, the commit it is readable at, its sha256 and its version, or `absent` |
| `engine.txt`, `cuda.txt`, `cuda12.txt`, `vulkan.txt`, `macos.txt`, `media.txt` | opencoti's, vendored unchanged | `repo`, `rev` (the payload commit), `abi` digests, `engine-min`, `sass`, `feature` rows, and one `file <platform> <kind> <path> <sha256> <bytes>` row per file |

A staggered move is one index line and its pin file; nothing else changes.
What makes that safe is checked in both parsers: every `abi` a component
states must be one the engine's pin provides, name and digest, and the engine
must not be older than the component's `engine-min`. A library and an engine
that disagree on an interface do not fail to load; they crash or run wrong.

Files are staged beside the engine under their **published names**, never
renamed: that name is what the engine looks for in its own directory
(`ggml-cuda-x86_64.so`, `ggml-cuda-cu12-x86_64.so`, `ggml-vulkan-x86_64.so`,
`oc-codec-linux-x86_64.so`; on Windows the engine has a row of its own under
the `.exe` name). A name is unique across the components of an
index (each publishes its `BUILD_INFO.<component>.md`), and both parsers refuse
two components that stage one name.

Two parsers read the directory - `cmake/opencoti-fetch.cmake` at build time and
`llm/engine/pin.go` via `//go:embed` - and `llm/engine/pin_cmake_test.go` runs
the real script against a pin both read, per platform, and holds them to the
same files, names and modes. `llm/engine/pin_test.go` keeps the committed pin
honest: every platform in the routing matrix is served or refused for a stated
reason, every library ships its licence text, and the files stay ASCII
(CMake's regex `.` does not match multi-byte UTF-8, which once let a comment
leak into the parser). Until engine `2610040837001` the pin was one file,
`llm/engine/pin.txt` (`bin` / `dso` rows and `#!` comment rows); opencoti
stopped writing that format on 2026-10-04.

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
cmake -B build . -DXOLLAMA_OPENCOTI_SIDECAR_DIR=<dir>     # every other file, verified
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

## Where the GPU payload lands

A **self-extracting** artifact carries its `ggml-*.so` inside itself and unpacks
them on first run to `$HOME/.llamafile/v/<engine-version>/`. Only `$HOME` is
ours to choose: `.llamafile` comes from llamafile's `g_app_name`, settable only
by an in-process C call, and the version segment is compiled in.

That last part is the problem. opencoti re-cuts a release **in place**, so c7 r1
and c7 r2 are different bytes under one tag and resolve to the same directory.
The engine unpacks only when what is already there is older, then loads what it
found — so a machine that has run r1 can go on running r1's CUDA kernels under
an r2 binary, silently. opencoti already hit the coarser version of this (their
bug-2272: c5, c6 and c7 all landing in `v/0.10.3/`) and namespaced by cut, which
cannot separate re-cuts of a single cut. Measured on one host: that one
directory name held three different `ggml-cuda.so` within 36 hours, and two
users held two different ones simultaneously.

So xollama does not share it. The engine subprocess is given a `HOME` holding
exactly one payload — the one belonging to the artifact about to run — and
anything another artifact left is deleted first.

That `HOME` goes where the rest of the engine's runtime already lives:
ollama's own library directory, beside `llama-server` and the ggml backends.

| Platform | Preferred payload root |
|---|---|
| Windows | `%LOCALAPPDATA%\Programs\Ollama\lib\ollama\engines\payload` |
| Linux | `/usr/local/lib/ollama/engines/payload` |
| macOS | `~/.ollama/engines/payload` (never the app bundle) |

giving, in full:

```
<lib>/ollama/engines/payload/.llamafile/v/<engine-version>/
```

It is not always writable, and that is expected rather than an error. A packaged
Linux install leaves that directory owned by `root` while the service runs as
`ollama`. On macOS the runtime directory is inside the signed application
bundle, which a user-owned install leaves writable and which a write would
break, so it is not offered at all (`payloadRoots`). Where the
preferred root cannot be written, xollama falls back to
`~/.ollama/engines/payload` — still its own directory, never your `~/.llamafile`.
Whether a root is writable is **checked before any work**, so a root that cannot
be used is not paid for with a hash of the artifact first.

### Who owns it

On Linux the server is usually a system service running as `ollama`, while an
administrator occasionally runs a command as root. Anything root creates would
otherwise be root-owned and unusable by the service afterwards — the same
mistake against the model store once turned a 0.06 s model list into a 9.89 s
one, reported by clients as a timeout rather than as any kind of error.

So when xollama runs as root it does not create the payload directory as root.
It takes the owner of the model store — whoever owns that must be able to write
it, so that identity is the service — and hands the directory over, with setgid
and group write. A purge removes files by writing the *directory*, so the
service can still clear a payload some earlier root invocation unpacked.

A non-root server already creates files as the right user, so nothing happens at
all. Windows has no equivalent problem: Ollama installs per user there, with no
unprivileged service account for an elevated process to lock out.

Two consequences worth stating plainly. Your own `~/.llamafile` is never read or
written, so you can run any opencoti build by hand without it interacting with
the one xollama launches. And the directory holds one payload rather than
accumulating one per artifact, so switching pins re-unpacks instead of growing.

Nothing here touches stock `llama-server`, which extracts nothing and is
launched with the environment it inherited. If no root can be used at all,
xollama logs a warning and falls back to the inherited `HOME` rather than
failing the load.

A **split** artifact — a bare APE with its `ggml-cuda.so` staged beside it, the
shape the `dev` channel publishes — extracts nothing at all, so none of this
applies to it. That is the better arrangement, because both halves can then be
pinned by sha256 in `llm/engine/pin/` and verified at fetch, where a fat
bin's payload is unverifiable once unpacked.

## Queued for the next pin (reported 2026-09-21, not published)

opencoti's dev repo moves only on its owner's word, so these are recorded here
rather than acted on. When they land, the pin moves in one commit and each row
is retired only on a measurement (`.claude/rules/engine-pin.md`).

- **0331 (bug-3535) — `--swa-seq-budget N` admitted N+1 windows.** The pool's
  `n_ubatch` batch slack was being sold as a session, so three sessions arriving
  under `N=2` produced a `find_slot` failure storm (3–590 failures per burst,
  clients timing out). **This one is ours to care about**: `appendSWABudgetArgs`
  in `llm/engine_launch.go` passes the flag whenever
  `XOLLAMA_SWA_SEQ_BUDGET` or a model's setting sizes it, and
  `llm/engine_estimate.go` budgets `min(SWASeqBudget, seqs)` windows of VRAM —
  the correct number. On the bytes pinned today the engine can hold one window
  more than the estimate paid for, silently. After the fix, expect one extra
  429 per burst and no stalls, and size on N.
- **0332 (bug-3538) — a second SIGTERM could deadlock the server** (upstream's
  `exit()` inside the signal handler). Measured on old bytes: 13/40 hung when
  the second signal followed within 0–3 ms, 12/12 when it beat the main thread's
  cleanup; 0/40 after the fix. **Not reachable from xollama**: the engine
  subprocess is stopped with `Process.Kill()` in `llm/llama_server.go`, a single
  SIGKILL, never a TERM → TERM → KILL escalation. It matters only to an operator
  driving the artifact directly.
- **0333 — `--repeat-layers` composes with a KVarN cache** (it refused to boot
  before).

The first of these is the advisory category again — a defect that answers worse
without saying anything — but it is *not* an `engineArgsAdvisories` row, because
that list covers flags an operator passes through `XOLLAMA_ENGINE_ARGS` and this
flag is one xollama emits itself. See `.claude/rules/engine-args.md`.

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

Phase 2's measurements are now in, and they reorder this: the knob surface is
not the next thing. The compatibility gap is, and it is opencoti's to close --
five of the eight models fail inside the engine, on argv identical to the one
stock llama-server accepts. `XOLLAMA_ENGINE_FALLBACK` exists so a user who wants
availability can have it, but it is off by default and is not a fix.

A second thing the A/B exposed is ours and is fixed: a `-np > 1` load prints its
KV lines per stream (`KV buffer (stream N) size =`) and the scraper matched none
of them, so `memGPU` lost the whole KV cache without a warning. That means the
27% multi-slot gap was measured against a wrong memory plan and has to be
re-measured on a corrected build before anyone acts on it.

What the A/B settled about the engines themselves: single-stream throughput is
parity (within 2%), concurrency at `-np 4` is 27% slower on opencoti, and the
VRAM-overflow path aborts rather than spilling. See
[`docs/evaluations/phase2-engine-ab.md`](../evaluations/phase2-engine-ab.md).

## macOS

Apple silicon only, and **opencoti only**: since the c10 release (owner,
2026-10-05, held until c10 served the Clef head) macOS ships no stock
`llama-server` or `llama-quantize`, and llama.cpp is not compiled for it.
`engine.StockShipped` (`llm/engine/stockless.go`) is false on darwin, so
`startLlamaServer` does not look for the stock binary, and a load that does
not get opencoti -- `XOLLAMA_ENGINE=llamacpp`, a model pinned to
`engine=llamacpp`, a missing engine or loader -- is refused with the reason
(`stockless` hook); the opt-in stock retry (`XOLLAMA_ENGINE_FALLBACK`) does
nothing there. Discovery takes the Metal device from the engine's own listing,
marked integrated. `scripts/build_darwin.sh` builds the Go binaries only
(`macos-stockless` hook) and refuses an Intel build: opencoti publishes no
Intel files, and there would be no engine at all.

- **The package** is the engine (the same APE file as Linux, the engine
  pin's `file any bin` row), the `macos` component's `ape` (the loader) and
  `metal` (`ggml-metal-aarch64.dylib`), and the `media` component's
  `macos-aarch64` rows: `codec`, `audiocpp`, `espeak` and the licence texts.
  The `metal` row is what routes Metal to the engine.
- **The launch** is `<dir>/ape-macos-aarch64 <engine> --server …` (`run` in
  `llm/engine/opencoti.go`, shared by the launch, the device listing and the
  link probe). Started any other way the engine compiles a loader with `cc`
  on first use, which a Mac without the Xcode tools cannot do. A missing
  loader is a refused load naming it (there is no stock engine to fall back
  to).
- **`--gpu apple`** selects Metal (`gpuFlag`). Discovery takes the engine's
  own device line (`MTL0`), as it does for CUDA and Vulkan.
- **The build** stages the files with `cmake/opencoti-fetch.cmake` from
  `scripts/build_darwin.sh` (`_stage_opencoti_engine`) into
  `Contents/Resources/engines`; the loader is staged executable. The same
  script signs them (`_sign_opencoti_engine`): the loader with
  `app/darwin/engine-loader.entitlements`
  (`com.apple.security.cs.allow-unsigned-executable-memory`; without it the
  hardened runtime kills it at start), and the libraries with the same
  identity, because library validation refuses another team's. The engine
  file is not a Mach-O and is sealed as a resource.
- **Limits on Metal** (opencoti's, not xollama's): KVarN cache types are
  refused by name, DCA attention runs on the host, and the rolling KV window
  has no meaning on unified memory.

Registry row `macos-engine`.
