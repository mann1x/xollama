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
| `engine-select` | `llm/llama_server.go` — in `startLlamaServer`, immediately before `exec.Command`: `engine.Launch(exe, params, engineBackends(launch.gpus), ml.LibOllamaPath)`, plus the `llm/engine` import and the `engineBackends` helper just above `startLlamaServer`. Returns `(exe, params)` unchanged for llama.cpp, so the off path is byte-identical. `Launch` also returns whether it chose opencoti, which `startLlamaServer` passes back as a fourth result (one caller) so `Load` can offer the opt-in `XOLLAMA_ENGINE_FALLBACK` retry via `retryOnStockEngine`, modelled on upstream's own `retryWithMMProjCPUOffload`; `llamaServerLaunchConfig.forceStockEngine` skips the hook on that retry. | Engine — opencoti-llamafile | [features/engine-opencoti-llamafile.md](../features/engine-opencoti-llamafile.md) |
| `memory-scrape` | `llm/llama_server.go` — `bufferSizeRegex` accepts the per-stream KV shape (`KV buffer (stream N) size =`, printed whenever `-np > 1` without `--kv-unified`; dynamic slots do pass that flag, but only on an opencoti load that raised its ceiling, so both shapes still occur) and counts `KVarN` as KV; `memoryBufferKey` gains `stream` so streams sum instead of colliding. Without it a multi-slot load silently under-counts its KV — no warning fires, because the model and compute lines still match. Guarded by `TestMemoryParsingCountsPerStreamKVBuffers` and siblings. | Engine — opencoti-llamafile | [features/engine-opencoti-llamafile.md](../features/engine-opencoti-llamafile.md) |
| `env-namespace` | `envconfig/config.go` — `Prefix`, `XollamaKey`, the XOLLAMA_-first lookup in `Var`, the two `XOLLAMA_ENGINE*` rows and the `Name`-branding loop at the end of `AsMap`; `cmd/cmd.go` — the OLLAMA_-fallback note in `appendEnvDocs` and the `XOLLAMA_ENGINE*` entries in `serve`'s list. Seven call sites also moved from `os.Getenv("OLLAMA_…")` to `envconfig.Var` (`cmd/bench`, `app/cmd/app` ×2, `app/ui`, `app/store`, `mlx/dynamic.go`, `server/routes.go`); those carry no marker because `TestNoDirectReadsOfOurOwnEnvironment` in `envconfig/namespace_test.go` fails if a merge puts one back. | XOLLAMA_ environment namespace | [features/rebrand.md](../features/rebrand.md#the-xollama_-environment-namespace) |
| `draft-assistant` | `llm/llama_server.go` — `draftTypeAssistant` plus `specTypeArg`/`retargetSpecType`; `externalDraftType` selects it from the drafter's `requires_target_arch` and rejects a mismatched target; `appendDraftArgs` emits upstream's `draft-mtp` spelling, and the rewrite to `draft-assistant` happens only after `engine.Launch` says opencoti won, so the stock path is byte-identical. A gemma-4 drafter is a head with no context of its own and the two engines name that driver differently — upstream reaches it through `draft-mtp`, opencoti through `draft-assistant`. Guarded by `TestRetargetSpecType`, `TestExternalDraftType` and `TestAppendDraftArgsAssistantUsesUpstreamSpelling`. | Gemma-4 assistant drafters | [features/gemma4-drafter.md](../features/gemma4-drafter.md) |
| `model-config` | `types/xollama/` is additive and carries the schema; the hooks are the wiring. `api/types.go` — `CreateRequest.Xollama`; `parser/parser.go` — the `xollama` command in `isValidCommand` and `Command.String`, the `case "xollama"` in `CreateRequest` and the `parseXollamaConfig` helper; `create/manifest.go` — `ModelfileLayerOptions.Xollama` plus `appendXollamaConfigLayer`/`removeXollamaConfigLayer`; `server/create.go` — one field passed through; `server/images.go` — `Model.Xollama` and the `case xollama.MediaTypeImageJSON` that reads it back; `llm/server.go` — `LlamaServerConfig.Xollama` and its two accessors; `server/routes.go` — one field in `llamaServerConfigForModel`; `llm/llama_server.go` — the engine pin at launch and the spec-type override. The media type is upstream's own, so push, pull and blob GC carry the layer without knowing what it is; the layer NAME (`xollama.json`) is what separates it from upstream's `config.json`. A model with no config produces no layer and takes every upstream path unchanged. Guarded by `types/xollama/config_test.go`, `parser/xollama_test.go`, `create/xollama_test.go` and `TestLlamaServerConfigXollamaAccessorsTolerateNil`. | Model config carrier | [features/model-config.md](../features/model-config.md) |
| `engine-session` | `llm/engine_session.go` is additive and holds the whole resolver; the hooks are the wiring. `llm/server.go` — `SessionID`/`PoolID` on `CompletionRequest` and `ChatRequest`, plus the `sessionAffinity`/`sessionPool` accessors; `llm/llama_server.go` — `session_id`/`pool_id` on `llamaServerCompletionRequest` and in the chat body map, both written through `sessionFieldsFor`/`applySession`; `server/routes.go` — `sessionIDForRequest` and the four request construction sites that pass it. The engine gate is checked first and is absolute: on stock llama.cpp no field is written at all, so the request body is upstream's byte for byte. Guarded by `llm/engine_session_test.go`. | Engine sessions | [xollama/sessions.mdx](../xollama/sessions.mdx) |
| `launch-config` | `llm/engine_launch.go`, `llm/engine_admission.go`, `llm/engine_estimate.go` and `llm/engine_dca.go` are additive and hold the resolvers; the hooks are the wiring. `llm/llama_server.go` — KV cache types emitted through `resolveKVCacheTypes`/`appendKVCacheArgs` instead of the single upstream type, the post-`engine.Launch` refusal for a type stock llama.cpp cannot parse, `appendKVCacheRingArgs` `appendSlotArgs` and `appendDCAArgs` (all after the engine is known, like `retargetSpecType`), the DCA refusals for a stock engine and for an architecture with no chunked route, `trainContext` on `llamaServerLaunchConfig`, the semaphore resize in `startProcess`, and the completion POST routed through `postWaitingForAdmission` so the engine's 429 becomes a wait rather than an error; `llm/server.go` — `SingleSequenceOnly` on `LlamaServerConfig`, and the `DCAUnlocksContext` branch on upstream's `num_ctx` clamp in `NewLlamaServer`; `server/routes.go` — `parallelUnsafeArchitectures`/`singleSequenceOnly` and one field in `llamaServerConfigForModel`; `server/sched.go` — `load()` calling `singleSequenceOnly`, `slotCeilingVRAM` added to the four `llm.PredictServerVRAM` call sites so the memory prediction is made against the slot ceiling rather than the slot the runner starts on, an `unlocked` argument on `effectiveModelContext`/`effectiveLlamaServerContext` with `LlmRequest.contextUnlocked` feeding it, and `runnerServesPastTrainedContext` guarding the clamp in `needsReload`. Every engine-specific flag is gated on opencoti, so a stock launch is upstream's argv unchanged. Guarded by `llm/engine_launch_test.go`, `llm/engine_slots_test.go`, `llm/engine_estimate_test.go`, `llm/engine_dca_test.go`, `server/sched_slot_estimate_test.go` and `server/sched_dca_test.go`. | KV cache, dynamic slots, memory prediction, DCA | [xollama/kv-cache.mdx](../xollama/kv-cache.mdx), [xollama/slots.mdx](../xollama/slots.mdx), [xollama/dca.mdx](../xollama/dca.mdx) |
| `engine-introspect` | `llm/engine_introspect.go` and `server/routes_engine.go` are additive and hold the whole feature; the hooks are two lines. `server/routes.go` — `r.GET("/api/engine", s.EngineHandler)` beside `/api/ps`; `server/sched.go` — an `llama` field on the fork-local `loadedModel` struct and one assignment in `loadedModels`. `EngineIntrospector` is a deliberately OPTIONAL interface, type-asserted at the call site rather than added to `LlamaServer`: only the llama-server family has an HTTP surface, and a method on the upstream interface purely so the MLX runner can return "unsupported" is a method every merge has to carry. The endpoint whitelist (`props`, `slots`, `metrics`, `polykv/pools`) is validated before any connection is opened. Read-only, adds nothing to the inference path. Guarded by `llm/engine_introspect_test.go` and `server/routes_engine_test.go`. | Engine introspection | [xollama/introspection.mdx](../xollama/introspection.mdx) |
| `engine-defects` | `llm/engine_defects.go` is additive and holds the table and the matcher; the hook is one line in `llm/llama_server.go` — `Load` returns `s.annotateEngineDefect(err)` instead of a bare `err` on the final opencoti failure path. Diagnosis only: it never retries, downgrades or changes what was launched, and the original error is wrapped rather than replaced so anything downstream that inspects it keeps working. A row matches only when BOTH the artifact file name and the engine's dying words match — version alone would blame the build for unrelated failures, signature alone would blame a defect a newer cut has fixed. `TestKnownDefectsMatchThePinnedArtifact` fails when the pin moves past a listed build, which is the reminder to retire the row. Guarded by `llm/engine_defects_test.go`. | Known engine defects | [xollama/slots.mdx](../xollama/slots.mdx) |
