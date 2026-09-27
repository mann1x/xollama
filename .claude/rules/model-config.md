---
paths:
  - types/xollama/**
  - cmd/tweak/**
  - docs/features/model-config.md
  - docs/xollama/tweak.mdx
---

# The model config layer (`xollama.json`)

- `types/xollama/config.go` is the schema. It is written by `xollama tweak
  model` (`cmd/tweak/`) and by a Modelfile `XOLLAMA` line (`parser/parser.go`).
  A feature block in its own file (`devices.go`, `council.go`) is a pointer
  field with its own `IsZero` and `validate`, wired into `Config.IsZero`,
  `Config.Validate` and `prune` in `cmd/tweak/fields.go` in the same commit.
  `devices` is pruned through `IsZero`; the council has its own `Prune`.
  Missing one leaves an empty block on disk that reads as a stated setting.
- **The version written is the lowest that is true.** `SchemaVersion` is 4 and
  `requiredVersion` raises the floor only for a block an older build would
  misread silently: v2 for `kv.unified` / `kv.residency_mode`, v3 for a
  `devices` pin, v4 for a `council`. Never stamp `SchemaVersion` unconditionally
  — that makes every model this build touched unreadable to an older xollama.
- **Launch config versus request config.** A setting that changes how the model
  *loads* (engine, KV, slots, devices) belongs in the runner's config; one that
  changes how a *turn is answered* (the council) must not. `Config.LaunchConfig`
  in `types/xollama/council.go` strips the council, and
  `llamaServerConfigForModel` in `server/routes.go` passes it (the
  `model-config` hook), so the launch never sees the council's settings. The one
  exception is a count: a council on PolyKV adds its pool seats
  (`CouncilPools`, from `councilPoolSeats`), because the engine sizes its pools
  at launch. It does NOT make
  two tags share a runner: upstream's `ManifestDigest` is in the same config,
  so a council tag and its `FROM` base swap the runner like any two tags over
  one blob (measured on b111, 2026-09-26). Add a new request-side block to
  `LaunchConfig`, not to the hook.
- **A council is a property of the model, never of the server.** `council` has
  no `XOLLAMA_*` fallback; `council.enabled` is the switch, and settings stated
  without it are refused by `validate`. `council.polykv on` requires the
  opencoti engine; `auto` is the default.
- `cmd/tweak/fields.go` rows: a field whose questions only make sense once a
  feature is on is `quiet` — skipped silently in the full walk, skipped *with
  the reason* when a flag names it. `kindText` takes free text or `@path` to
  read a file; its usage string is `TEXT|@file|unset`.
- `xollama show` and the wizard name settings through one table
  (`tweak.SettingRows`); never spell a setting a second way in `cmd/cmd.go`.
- Round-trip guard: `server/modelfile_roundtrip_test.go` holds that
  `show --modelfile` prints the layer through `Config.Marshal`, so the version
  shown is the one a rebuild would store.
- Prose: `docs/features/model-config.md`, `docs/xollama/tweak.mdx`; the council
  plan is `plans/agentic-council-chat.md`; serving a council turn is
  `.claude/rules/council.md`. Registry row `model-config` in
  `docs/protocols/UPSTREAM-SYNC.md`.
