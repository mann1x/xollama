# CLAUDE.md — xollama

@.wolf/OPENWOLF.md

Soft fork of ollama. `main` = upstream release v0.34.2 + fork changes, carrying the
full upstream history so every `git merge upstream/main` has a real merge-base.
Shared agent notes: @./AGENTS.md · Upstream contribution rules: @./CONTRIBUTING.md

## Two standing rules

1. **Never rename the Go module path.** It stays upstream's, exactly as declared
   on the first line of `go.mod`. Renaming costs a ~33% conflict rate per sync
   (measured: 155 of the 474 files upstream touched between the v0.34.0 and
   v0.34.2 tags). The binary is xollama; the import path is upstream's.
2. **Off means off.** With `XOLLAMA_ENGINE=llamacpp` and no xollama flags,
   behaviour must be byte-identical to upstream. That is what keeps an A/B
   against vanilla honest.

## Read before touching the upstream tree

- `docs/protocols/UPSTREAM-SYNC.md` — every change is either an additive file or
  a marked surgical hook, and hooks go in the Registry in the same commit.
- `docs/protocols/CARRIED-PATCHES.md` — the open upstream PRs this fork carries.
  Each is its own `--no-ff` merge, retired the day upstream takes it.
- `docs/features/engine-opencoti-llamafile.md` · `docs/features/rebrand.md`
- `docs/evaluations/phase0-engine-compat.md` — measured engine-compat baseline.

Remotes: `origin` = mann1x/xollama · `upstream` = ollama/ollama ·
`fork` = mann1x/ollama (where PRs to upstream are staged).

## Commands

Go-only iteration against an existing native payload:

```sh
go build -o xollama .
go run . serve
go test ./server/... ./model/... ./thinking/... ./llm/...
golangci-lint run
```

Full native build (prereqs and GPU notes in `docs/development.md`):

```sh
cmake -B build .
cmake --build build --parallel 8
cmake -B build . -DOLLAMA_LLAMA_BACKENDS="cuda_v13;vulkan"
cmake -B build . -DOLLAMA_MLX_BACKENDS=cuda_v13
```

End-to-end suites live in `integration/` and are build-tagged, so they are not
part of the default sweep:

```sh
go test -tags integration ./integration/...
```

Secret scan — `.githooks/pre-commit` runs it locally, `.github/workflows/gitleaks.yml` in CI:

```sh
gitleaks protect --staged --config .gitleaks.toml
```

## Architecture

**Entry**: `main.go` → `cmd/cmd.go` (Cobra) · **HTTP**: `server/routes.go` (Gin),
scheduling `server/sched.go`, model IO `server/images.go` `server/create.go`
`server/prompt.go`.
**Runners**: GGUF as a `llama-server` subprocess via `llm/server.go` +
`llm/llama_server.go`, built from `llama/server/CMakePresets.json`; MLX via
`mlxrunner/` (`runner.go`, `pipeline.go`, `prefix_cache.go`, `cache/`, `model/`,
`tokenizer/`, `xgrammar/`). `llm/engine_args.go` appends the operator's
`XOLLAMA_ENGINE_ARGS` last on the engine command line.
**Prompting**: `model/renderers/` (per-model `Render`) ↔ `model/parsers/`
(streaming output), plus `template/`, `thinking/`, `harmony/`.
**API shims**: `api/types.go`, `openai/openai.go`, `anthropic/anthropic.go`,
`middleware/`. **Config**: `envconfig/config.go` holds every `OLLAMA_*` var.
**Discovery** `discover/` · **Transfers** `x/transfer/` · **GGUF** `fs/gguf/`,
`fs/safetensors/` · **Types** `types/model/`.
**CLI support packages**: Modelfile parsing in `parser/` (`parser.go`,
`expandpath_test.go`), terminal progress bars and spinners in `progress/`
(`bar.go`, `spinner.go`, `progress.go`), human-readable sizes and durations in
`format/` (`bytes.go`, `time.go`), line editing in `readline/`.
**Registry and on-disk store**: `auth/auth.go` signs registry requests with the
local SSH keypair; `manifest/` holds the manifest/blob model (`manifest.go`,
`layer.go`, `paths.go`) that `server/images.go` reads and writes.
**Logging**: `logutil/logutil.go` — the shared `slog` handler and `LevelTrace`.
**Internal-only packages** under `internal/`: `internal/cloud` (cloud host
policy), `internal/modelref` (model reference parsing), `internal/onboarding`
(first-run app state), `internal/orderedmap` (insertion-ordered maps behind the
tool schemas), plus `internal/testutil` and `internal/proxy`.
**Integration tests**: `integration/` — build-tagged end-to-end suites
(`basic_test.go`, `tools_test.go`, `vision_test.go`, `concurrency_test.go`) with
fixtures in `integration/testdata/`; they need a running server and pulled models.
**Launchers**: `cmd/launch/` (`claude.go`, `opencode.go`, `codex_app_profile.go`…)
with the Bubble Tea menu in `cmd/tui/tui.go`.
**Desktop UI**: `app/ui/app/src/routes/` (React 19 + TanStack Router + Vite),
sibling to the `app` workspace (`vite.config.ts`, `vitest.config.ts`).
Pinned natives: `LLAMA_CPP_VERSION`, `MLX_VERSION`, `MLX_C_VERSION`,
orchestrated by `CMakeLists.txt` / `CMakePresets.json`; the opencoti engine
artifact is pinned by `llm/engine/pin.txt` (`repo`, `rev` commit sha, `tag`,
`channel`, `feature`, `accel`, plus `bin` / `dso` asset rows), read by both
`llm/engine/pin.go` and `cmake/opencoti-fetch.cmake`. Moving that pin retires
only the rows in `llm/engine_defects.go` the new bytes are *measured* to fix —
a changelog is not a measurement; the measurement is `scripts/phase2-engine-ab.py`,
run as the `ollama` user (`.claude/rules/solidpc-testing.md`).

## Tooling and conventions

- MCP servers available: `pgvector` (persistent memory + KG — store fork
  decisions there), `lsp` (gopls/clangd, wired through `cclsp.json`),
  `code-graph`, `gitnexus`, `context7`, `github-mcp`, `filesystem`, `searxng`.
- Compile-aware LSP engine config: `.claude-hooks/lsp-engine.toml`.
- Commit subjects follow `CONTRIBUTING.md`: `<package>: <short description>`,
  lowercase, a continuation of "This changes Ollama to…".
- Docs are Mintlify `.mdx` under `docs/`, indexed by `docs/docs.json`.

<!-- caliber:managed:pre-commit -->
## Before Committing

**IMPORTANT:** Before every git commit, you MUST ensure Caliber syncs agent configs with the latest code changes.

First, check if the pre-commit hook is already installed:
```bash
grep -q "caliber" .git/hooks/pre-commit 2>/dev/null && echo "hook-active" || echo "no-hook"
```

- If **hook-active**: the hook handles sync automatically — just commit normally. Tell the user: "Caliber will sync your agent configs automatically via the pre-commit hook."
- If **no-hook**: run Caliber manually before committing:
  1. Tell the user: "Caliber: Syncing agent configs with your latest changes..."
  2. Run: `caliber refresh && git add CALIBER_LEARNINGS.md CLAUDE.md .claude/ 2>/dev/null`
  3. After it completes, briefly tell the user what Caliber updated. Then proceed with the commit.

**Valid `caliber refresh` options:** `--quiet` (suppress output) and `--dry-run` (preview without writing). Do not pass any other flags — options like `--auto-approve`, `--debug`, or `--force` do not exist and will cause errors.

**`caliber config`** takes no flags — it runs an interactive provider setup. Do not pass `--provider`, `--api-key`, or `--endpoint`.

If `caliber` is not found, tell the user: "This project uses Caliber for agent config sync. Run /setup-caliber to get set up."
<!-- /caliber:managed:pre-commit -->

<!-- caliber:managed:learnings -->
## Session Learnings

Read `CALIBER_LEARNINGS.md` for patterns and anti-patterns learned from previous sessions.
These are auto-extracted from real tool usage — treat them as project-specific rules.
<!-- /caliber:managed:learnings -->

<!-- caliber:managed:model-config -->
## Model Configuration

Recommended default: `claude-sonnet-4-6` with high effort (stronger reasoning; higher cost and latency than smaller models).
Smaller/faster models trade quality for speed and cost — pick what fits the task.
Pin your choice (`/model` in Claude Code, or `CALIBER_MODEL` when using Caliber with an API provider) so upstream default changes do not silently change behavior.

<!-- /caliber:managed:model-config -->

<!-- caliber:managed:sync -->
## Context Sync

This project uses [Caliber](https://github.com/caliber-ai-org/ai-setup) to keep AI agent configs in sync across Claude Code, Cursor, Copilot, and Codex.
Configs update automatically before each commit via `caliber refresh`.
If the pre-commit hook is not set up, run `/setup-caliber` to configure everything automatically.
<!-- /caliber:managed:sync -->
