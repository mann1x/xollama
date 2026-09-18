# Feature — the xollama identity

> Status: **planned**.

xollama is a drop-in ollama: same API, same data directory, same default
port. Only the product identity changes, so every existing client keeps
working and existing model blobs are reused rather than re-downloaded.

| | value | note |
|---|---|---|
| binary / CLI | `xollama` | can sit beside a stock `ollama` install |
| Go module path | `github.com/ollama/ollama` | **unchanged — see below** |
| models dir | `~/.ollama/models` | reuse the blobs you already have |
| default port | `11434` | drop-in for every client |
| API surface | `/api/*`, `/v1/*` | unchanged |

## Why the module path does not change

A module rename is one `sed` and a permanent tax. Measured on this tree:

- 547 `.go` files import `github.com/ollama/ollama`, over 1238 lines.
- Upstream touched 474 `.go` files between `v0.34.0` and `v0.34.2` — two
  patch releases, three weeks apart.
- 155 of those are files a rename would have rewritten: a **~33% conflict
  rate on every sync**, bought for a cosmetic import path nobody imports.

The whole point of this fork is that syncing stays cheap. Renaming the module
is the single most expensive thing we could do to that, for the least return.

## Windows update identity

A stock ollama install and an xollama install must not be mistaken for each
other by the package manager, or `winget upgrade` will replace xollama with
upstream ollama.

What winget matches on is the Add/Remove Programs entry, and for an Inno Setup
installer that comes from `AppId` in [`app/ollama.iss`](../../app/ollama.iss):

```
AppId={{44E83376-CE68-45EB-8FC1-393500EB558C}
```

That GUID becomes the uninstall key `{44E83376-…}_is1`, which is the
ProductCode the `Ollama.Ollama` winget manifest keys off. **Changing the GUID
is what stops the auto-upgrade** — the rest is cosmetic: `MyAppName`,
`MyAppPublisher`, `DefaultDirName`, `OutputBaseFilename`, and `app/ollama.rc`.

The package identifier is **`ManniX.xOllama`**. winget-pkgs moderation ties the
publisher segment to the actual publisher, so a fork published under Ollama's
namespace would be rejected; `ManniX` is ours and passes. Keep the identifier in
one constant so it is a one-line change if that ever needs revisiting.

The in-app half of this is already solved in `mann1x/ollama`
(`app/updater/fork.go`, `app/updater/consent.go`): the updater does not replace
a fork build with a stock one. That is fork-specific rather than an upstream PR,
so it belongs here rather than in the carried-patch registry.

## Scope of the rename

- `cmd/` binary name and install paths
- user-facing strings, `--help`, version output
- the app / tray identity
- `.gitignore`: `/ollama` → `/xollama` (the built binary)
- `app/ollama.iss` installer identity, per the section above
- README and docs

Explicitly **not** renamed: the Go module path, package names, the API
routes, the data directory, the registry protocol, or anything in `LICENSE`.
Upstream's MIT licence and copyright stay exactly as they are.
