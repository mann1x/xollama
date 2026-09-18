# Protocol — Upstream Sync

xollama is a **soft fork of [ollama](https://github.com/ollama/ollama)**. It
exists to carry fixes and features upstream has not merged, and to serve GGUF
models on the [opencoti-llamafile](https://huggingface.co/ManniX-ITA/opencoti-llamafile)
engine instead of stock llama.cpp.

Staying cheap to sync is the whole design constraint. Everything below exists
to keep `git merge upstream/main` a review, not an archaeology dig.

## Remotes

| Remote | Repository | Role |
|---|---|---|
| `origin` | `mann1x/xollama` | this fork |
| `upstream` | `ollama/ollama` | source of truth for the base |
| `fork` | `mann1x/ollama` | where PRs to upstream are staged |

`main` was seeded from upstream `v0.34.2` (`dfabde45`, 2026-09-17) **with the
full 5771-commit history**, deliberately. A squashed import has no merge-base,
and every later sync then needs a `git replace --graft` trick to find one —
opencoti had to do exactly that once, and once was enough. Here the merge-base
is real from day one.

## The rule

> **Every xollama change is either an additive file outside the upstream tree,
> or a surgical hook inside it.**

### Additive (preferred)

A file that does not exist upstream. New files live in:

- `docs/protocols/`, `docs/features/` (this tree)
- `llm/engine/` — the engine resolver and the opencoti adapter
- top-level identity files we own: `README.md`, `CLAUDE.md`, `AGENTS.md`

Additive files never conflict on an upstream merge.

### Surgical hook

A small edit to an existing upstream file, allowed **only** when there is no
additive way to get the effect. A surgical hook MUST:

1. Be the minimum-line edit that achieves it — one import plus one call site,
   or one conditional.
2. Carry a marker comment on the line above:
   `// xollama-hook: <feature-id> — see docs/features/<plan>.md`
3. Be inert when the feature is off. With `XOLLAMA_ENGINE=llamacpp` and no
   xollama flags set, behaviour must be byte-identical to upstream.
4. Be listed in the Registry below, in the same commit that adds it.

### What is NOT a surgical hook

- A refactor of an upstream file "while we're here". Send it upstream instead.
- A rename of an upstream symbol. Wrap, never rename.
- An unconditional behaviour change. Gate it.

## Why we do not rename the Go module

`github.com/ollama/ollama` stays the module path even though the product is
called xollama. This is measured, not sentimental:

- 547 `.go` files import that path, over 1238 import lines.
- Upstream touched 474 `.go` files between `v0.34.0` and `v0.34.2` alone —
  two patch releases, three weeks.
- **155 of those files are ones we would have rewritten.** A module rename
  buys a cosmetic import path and pays ~33% conflict rate on every sync,
  forever.

The binary, the CLI name, the app identity and the user-facing strings are
xollama's. The import path is upstream's. See
[`docs/features/rebrand.md`](../features/rebrand.md).

## Sync workflow

Sync on a branch, never on `main` — a half-resolved merge on the trunk is
hard to back out of.

```bash
git fetch upstream --tags
git rev-list --count upstream/main..main    # ours they do not have
git rev-list --count main..upstream/main    # theirs we do not have — the review size

git checkout main && git pull --ff-only
git checkout -b sync/upstream-YYYYMMDD
git merge upstream/main       # expect conflicts only where the tables say
```

Then, in order, and do not skip one because the previous passed:

1. `go build ./...` and `go vet ./...`
2. `go test ./llm/... ./server/... ./model/parsers/... ./thinking/...` —
   the carried patches live there and their tests are the guard.
3. `scripts/check-hooks.sh` — greps `xollama-hook:` markers and reconciles
   them against the Registry. A marker with no Registry row fails.
4. Re-check every carried PR in
   [`CARRIED-PATCHES.md`](./CARRIED-PATCHES.md): any that landed upstream
   gets **retired**, not merged twice.
5. Boot both engines against one model and compare:
   `XOLLAMA_ENGINE=llamacpp` vs `XOLLAMA_ENGINE=opencoti`.
6. Read `git diff main...HEAD` over the parser/thinking paths. Not the merge
   diff — the diff against our own trunk, which is what actually changed for us.

Merge with `--no-ff` so the sync is one identifiable commit range.

## Take gladly / read line by line

- **Take gladly.** GPU discovery, scheduler, registry/transfer, the app,
  build and CI, dependency bumps, new model architectures.
- **Read line by line.** `model/parsers/`, `thinking/`, `llm/llama_server.go`,
  `llm/server.go`, `server/routes.go`. This is precisely the surface the
  carried patches change *and* the surface upstream edits most. A merge that
  resolves cleanly is not evidence it resolved correctly — a parser fix
  upstream rewrote and a parser fix we wrote touch the same lines for
  unrelated reasons, and git will happily keep one of them.

## Registry — known surgical hooks

Add the row in the same commit as the hook.

| Hook ID | File | Feature | Plan |
|---|---|---|---|
| `engine-select` | `llm/llama_server.go` — in `startLlamaServer`, immediately before `exec.Command`: `engine.Launch(exe, params, engineBackends(launch.gpus), ml.LibOllamaPath)`, plus the `llm/engine` import and the `engineBackends` helper just above `startLlamaServer`. Returns `(exe, params)` unchanged for llama.cpp, so the off path is byte-identical. | Engine — opencoti-llamafile | [features/engine-opencoti-llamafile.md](../features/engine-opencoti-llamafile.md) |
| `env-namespace` | `envconfig/config.go` — `Prefix`, `XollamaKey`, the XOLLAMA_-first lookup in `Var`, the two `XOLLAMA_ENGINE*` rows and the `Name`-branding loop at the end of `AsMap`; `cmd/cmd.go` — the OLLAMA_-fallback note in `appendEnvDocs` and the `XOLLAMA_ENGINE*` entries in `serve`'s list. Seven call sites also moved from `os.Getenv("OLLAMA_…")` to `envconfig.Var` (`cmd/bench`, `app/cmd/app` ×2, `app/ui`, `app/store`, `mlx/dynamic.go`, `server/routes.go`); those carry no marker because `TestNoDirectReadsOfOurOwnEnvironment` in `envconfig/namespace_test.go` fails if a merge puts one back. | XOLLAMA_ environment namespace | [features/rebrand.md](../features/rebrand.md#the-xollama_-environment-namespace) |
