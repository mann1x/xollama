---
name: xollama-build-test
description: Runs the correct build/test/lint/secret-scan loop for a change in the xollama fork: `go build -o xollama .` plus targeted `go test` for pure-Go edits, `cmake -B build . [-DOLLAMA_LLAMA_BACKENDS=... | -DOLLAMA_MLX_BACKENDS=...]` + `cmake --build build --parallel 8` for native edits under llama/, mlx/, mlxrunner/xgrammar/native/ or cmake/, `go generate ./...` + `git diff --exit-code` for generated-file drift, `golangci-lint run`, and the `.githooks/pre-commit` gitleaks scan before committing. Use when the user says 'build it', 'run the tests', 'does it compile', 'build with CUDA/Vulkan/ROCm/MLX', 'lint it', 'is it ready to commit', or after any edit to native code or CMake. Do NOT use for release packaging (scripts/build_linux.sh, build_darwin.sh, build_windows.ps1, release.yaml), for Docker image builds, for writing new tests, or for merging upstream (see docs/protocols/UPSTREAM-SYNC.md).
paths:
  - **/*.go
  - CMakeLists.txt
  - CMakePresets.json
  - cmake/**
  - llama/**
  - mlx/**
  - mlxrunner/**
  - app/ui/app/**
  - .golangci.yaml
  - .gitleaks.toml
  - .githooks/**
  - .github/workflows/**
---
# xollama — Build, Test, Lint, Scan

Pick the smallest loop that actually covers the change, run it, report the real
output. Never claim a build or test passed without the command output.

## Critical

1. **Scope the loop by what changed.** Run `git status --short` first and map it:

   | Changed paths | Required loop |
   |---|---|
   | `*.go` only (e.g. `llm/`, `server/`, `model/`, `openai/`) | Step 2 → 3 → 8 |
   | `app/**` Go or `app/ui/app/**` | Step 2 → 4 → 5 → 8 |
   | `llama/**`, `mlx/**`, `mlxrunner/xgrammar/native/**`, `cmake/**`, `CMakeLists.txt`, `CMakePresets.json`, `LLAMA_CPP_VERSION`, `MLX_VERSION`, `MLX_C_VERSION` | Step 6 (+ Step 7 for MLX) → 3 → 8 |
   | `api/types.go` or any struct exported to the UI | Step 5 (generated-file gate) → 3 |
   | docs/`.wolf/`/workflows only | Step 9 only |

2. **Build with `go build -o xollama .`** The CMake build already produces
   `xollama` (`OLLAMA_GO_OUTPUT` in `cmake/local.cmake`), but a bare
   `go build .` names its output after the module's last path element, and the
   module path deliberately stays `github.com/ollama/ollama` — so it drops a
   stray `ollama` binary instead. Both names are gitignored. Do not "fix" this
   by renaming the module path — see CLAUDE.md rule 1.

3. **Off means off.** After touching `llm/server.go`, `llm/llama_server.go` or any
   engine hook, verify the vanilla path is unchanged:

   ```sh
   XOLLAMA_ENGINE=llamacpp ./xollama serve
   ```

   must behave byte-identically to upstream with no xollama flags set. State
   explicitly whether you checked this.

4. **Never run `scripts/build_linux.sh`, `scripts/build_darwin.sh`,
   `scripts/build_windows.ps1` or `docker build .` to verify a change.** Those are
   release packaging; they are slow, they write a dist tree, and they are out of
   scope for this skill.

5. **Secret scan is a commit gate, not an afterthought** (Step 9). The hook is the
   primary gate — a manual scan after `git commit` gates nothing (see the incident
   note at the top of `.gitleaks.toml`).

6. **OpenWolf bookkeeping is mandatory.** Any failed build, failed test, or lint
   error → read `.wolf/buglog.json` first (the fix may be known), then append a
   `bug-NNN` entry after fixing. Append one line per run to `.wolf/memory.md`:
   `| HH:MM | go test ./llm/... | llm/repeat_guard.go | PASS | ~1.2k |`.

## Instructions

### Step 1 — Classify the change

```sh
git status --short
git diff --stat
```

Map the touched paths with the table in Critical §1. If the tree has unmerged
paths (`UU`/`DU` in `git status`), **stop**: a merge is in progress and any build
result is meaningless. Resolve the conflict first, then restart at Step 1.

Verify you have a concrete list of loops to run before proceeding.

### Step 2 — Go-only loop (uses the change list from Step 1)

This builds against the *existing* native payload in `build/lib/ollama`; it does
not rebuild native code.

```sh
gofmt -l .            # must print nothing
go build -o xollama . # a bare `go build .` would emit `ollama` instead
```

Then run the packages you touched, not the whole tree:

```sh
go test ./server/... ./model/... ./thinking/... ./llm/...
```

Narrow further when iterating on one file — e.g. for `llm/repeat_guard.go`:

```sh
go test -count=1 -run TestRepeatGuard ./llm/...
```

Verify `go build -o xollama .` exits 0 and every `go test` line reads `ok` or `no test files`
before proceeding. On a `FAIL`, re-run that single package with `-v` and fix
before any wider run.

### Step 3 — Full sweep (uses the passing state from Step 2)

Match what CI actually runs (see `.github/workflows/test.yaml`):

```sh
go test -count=1 -bench=. -benchtime=1x ./...
go test -race -count=1 ./...
```

`-bench=. -benchtime=1x` smoke-runs each benchmark once to catch panics; it
asserts nothing about timings. The `-race` run needs `CGO_ENABLED=1` (the default
here; go1.26.0 toolchain).

Integration tests live in `integration/`, are behind a build tag, and need a
running server plus pulled models — they are **not** part of this gate. Run them
only when asked:

```sh
go test -tags integration ./integration/...
```

Verify both sweeps exit 0 before proceeding.

### Step 4 — UI build (only when `app/**` changed)

`app/ui/app.go` has `//go:embed app/dist`, so `./app/...` will not even compile
until the UI is built:

```sh
cd app/ui/app && npm ci && npm run build   # tsc -b && vite build
npx vitest run                              # one-shot; bare `npm test` watches
cd - && go test -count=1 ./app/...
```

Verify `app/ui/app/dist/` exists and `go test ./app/...` passes before proceeding.

### Step 5 — Generated-file drift gate (uses Step 4's UI deps)

CI fails on drift, so run this whenever Go types crossing into the UI or MLX
wrappers changed:

```sh
go install github.com/tkrajina/typescriptify-golang-structs/tscriptify@latest
go generate ./...
git diff --exit-code -- app/ui/app/codegen/gotypes.gen.ts
```

For MLX wrapper headers (`mlx/generated.h`, `mlx/generated.c`, `mlx/include/mlx/c`):

```sh
cmake -S . -B build/mlx-generate -DOLLAMA_MLX_BACKENDS=cuda_v13
cmake --build build/mlx-generate --target ollama-mlx-generate-wrappers
git diff --exit-code -- mlx/generated.h mlx/generated.c mlx/include/mlx/c
```

Verify both `git diff --exit-code` calls exit 0. A non-zero exit means the
generated file must be committed alongside the source change — commit it, do not
revert it.

### Step 6 — Native build (llama.cpp / GGML side)

Baseline, CPU-only on Linux/Windows, Metal on macOS arm64:

```sh
cmake -B build .
cmake --build build --parallel 8
```

GPU backends are opt-in and explicit (`docs/development.md`):

```sh
cmake -B build . -DOLLAMA_LLAMA_BACKENDS="cuda_v13;vulkan"
cmake --build build --parallel 8
```

Valid values only: `cuda_v12`, `cuda_v13`, `rocm_v7_1`, `rocm_v7_2`, `vulkan`,
`cuda_jetpack5`, `cuda_jetpack6`. Narrow to local hardware to cut build time:

```sh
cmake -B build . -DOLLAMA_LLAMA_BACKENDS=cuda_v13 -DCMAKE_CUDA_ARCHITECTURES=native
cmake -B build . -DOLLAMA_LLAMA_BACKENDS=rocm_v7_2 -DCMAKE_HIP_ARCHITECTURES=gfx1100
```

Useful targets from `cmake/local.cmake`: `ollama-go` (Go binary only),
`ollama-local` (Go binary + CPU `llama-server`), `ollama-llama-server-backends`,
`ollama-mlx-backends`. Build one with:

```sh
cmake --build build --target ollama-local --parallel 8
```

Verify the payload landed before proceeding:

```sh
ls build/lib/ollama          # llama-server, llama-quantize, ggml-*.so
./xollama --version
```

### Step 7 — MLX native build (only when `mlx/**` or `mlxrunner/**` native code changed)

```sh
cmake -B build . -DOLLAMA_MLX_BACKENDS=cuda_v13     # Linux/Windows, CUDA 13+ and cuDNN 9+
cmake --build build --parallel 8
```

On macOS arm64 use the shipped preset (`CMakePresets.json`, `metal_v3;metal_v4`):

```sh
cmake --preset "MLX Metal" && cmake --build --preset "MLX Metal"
```

Against a local MLX checkout:

```sh
OLLAMA_MLX_SOURCE=/path/to/mlx OLLAMA_MLX_C_SOURCE=/path/to/mlx-c cmake -B build .
```

Verify the build exits 0, then re-run Step 3 — MLX changes cross into Go via cgo.

### Step 8 — Lint (uses the compiling tree from Step 2/6)

```sh
golangci-lint run
```

Config is `.golangci.yaml` (schema `version: "2"`). It will not run on a tree that
does not compile, so Step 2 must pass first. Fix every finding — CI runs
`golangci-lint-action@v9` with this same config and `severity.default: error`.

Verify `golangci-lint run` prints nothing and exits 0 before proceeding.

### Step 9 — Secret scan, before every commit

One-time per clone (git will not do it for you):

```sh
git config core.hooksPath .githooks
```

Then `git commit` runs `.githooks/pre-commit`, which executes:

```sh
gitleaks git --staged --config .gitleaks.toml --redact --no-banner -v
```

To scan by hand before staging a commit, run exactly that command. CI
(`.github/workflows/gitleaks.yml`, gitleaks pinned to 8.30.1) re-scans the **full
history** with `gitleaks git . --config .gitleaks.toml --redact --no-banner -v`
and is not skippable.

Verify `leaks found: 0` before committing. Never reach for `--no-verify` or
`XOLLAMA_SKIP_GITLEAKS=1` on your own initiative — ask the user first.

### Step 10 — Record the result

Append one line per significant run to `.wolf/memory.md`. If anything failed at
any step, append a `bug-NNN` entry to `.wolf/buglog.json` with `error_message`
(verbatim), `file`, `root_cause`, `fix`, `tags`. If the failure taught a build
gotcha, add it to `.wolf/cerebrum.md` under `## Do-Not-Repeat` with today's date.

## Examples

### Example 1 — "run the tests" after a pure-Go change

**User says:** "I added `llm/repeat_guard.go` and its test — run the tests."

**Actions taken:**

```sh
git status --short
#  A  llm/repeat_guard.go
#  A  llm/repeat_guard_test.go
#  M  llm/server.go
#  M  openai/openai.go
gofmt -l .
go build -o xollama .
go test -count=1 ./llm/... ./openai/...
go test -count=1 -bench=. -benchtime=1x ./...
go test -race -count=1 ./...
golangci-lint run
```

No native paths changed → Steps 6/7 skipped. `llm/server.go` is an engine hook, so
also confirm the off-path:

```sh
XOLLAMA_ENGINE=llamacpp ./xollama serve
```

with no xollama flags set.

**Result:** `ok github.com/ollama/ollama/llm`, full sweep and race clean, lint
clean. One line appended to `.wolf/memory.md`. Report: which packages ran, that
native was untouched, and that the `llamacpp` off-path was verified.

### Example 2 — "build it with CUDA and Vulkan"

**User says:** "Build with CUDA 13 and Vulkan."

**Actions taken:**

```sh
cmake -B build . -DOLLAMA_LLAMA_BACKENDS="cuda_v13;vulkan" -DCMAKE_CUDA_ARCHITECTURES=native
cmake --build build --parallel 8
ls build/lib/ollama
./xollama --version
go test -count=1 -bench=. -benchtime=1x ./...
golangci-lint run
```

**Result:** `build/lib/ollama/{cuda_v13,vulkan}/` payloads present, the repo-root
binary rebuilt by the `ollama-go` target, Go sweep clean. Report the backends
actually built and where the payload landed.

## Common Issues

**`CMake Error: Unknown backend "cuda"` / backend silently missing from `build/lib/ollama`**
The value list is exact. Use one of `cuda_v12`, `cuda_v13`, `rocm_v7_1`,
`rocm_v7_2`, `vulkan`, `cuda_jetpack5`, `cuda_jetpack6`. Re-configure from scratch:

```sh
rm -rf build && cmake -B build . -DOLLAMA_LLAMA_BACKENDS=cuda_v13
```

**`golangci-lint: command not found`**
It is not installed in this environment by default. Install a version matching CI:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
export PATH=$PATH:$(go env GOPATH)/bin
```

Do not report lint as "passing" when the binary is missing — report it as not run.

**`golangci-lint run` reports a depguard error on a test helper import**
`.golangci.yaml` denies test helpers in non-test files. Move the helper usage into
a `_test.go` file, or move the helper itself out of `internal/testutil` /
`mlx/mlxtest` / `mlx/mlxthread/mlxthreadtest`.

**`pattern app/dist: no matching files found` when building or testing `./app/...`**
`app/ui/app.go` embeds `app/dist`. Run Step 4 first:

```sh
cd app/ui/app && npm ci && npm run build
```

**`npm test` hangs and never exits**
`package.json` maps `test` to bare `vitest`, which is watch mode on a TTY. Use
`npx vitest run` locally.

**CI fails on `git diff --exit-code -- app/ui/app/codegen/gotypes.gen.ts` (or `mlx/generated.*`)**
Generated output is checked in and must move with the source change. Run Step 5
and commit the regenerated file in the same commit — do not `git checkout` it away.

**Random cgo crashes, `unexpected fault address`, or struct-layout nonsense after a native change**
CGO went out of sync with the rebuilt native code (`docs/development.md`). Run
`go clean -cache`, then redo Step 6.

**The server runs but no GPU is used, logs say no acceleration libraries found**
The payload is looked up in `build/lib/ollama` and the per-platform dist lib
directory for dev builds. Confirm `ls build/lib/ollama` is non-empty and that you
ran `cmake --build build`, not just `go build -o xollama .` — a Go build never produces the
native payload.

**`pre-commit: gitleaks found a secret in the STAGED changes — commit refused.`**
If the secret is real: revoke it at the provider **first**, then strip it from the
diff. If it is a false positive: add an allowlist entry to `.gitleaks.toml`
pinned by value (and by path when it belongs to one file) with a comment saying
why. Never add a bare path to quiet the scan.

**`pre-commit: gitleaks not installed, skipping scan (CI still gates)`**
The hook is non-fatal without the binary, but CI is not. Install gitleaks 8.30.1
(the version pinned in `.github/workflows/gitleaks.yml`) before pushing.

**`git commit` produces no scan output at all**
`core.hooksPath` was never set for this clone. Run
`git config core.hooksPath .githooks` and re-commit.

**Build or test results look meaningless / files appear both old and new**
Check `git status --short` for `UU`/`DU` unmerged paths. A merge in progress
(carry branches merge upstream PRs with `--no-ff`) means the tree is not a
valid build input. Resolve first; see `docs/protocols/CARRIED-PATCHES.md`.
