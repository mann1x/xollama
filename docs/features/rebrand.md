# Feature — the xollama identity

> Status: **shipped** - 2026-09-18.

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
installer that comes from `AppId` in [`app/xollama.iss`](../../app/xollama.iss).
Upstream ships `{44E83376-CE68-45EB-8FC1-393500EB558C}`, whose uninstall key
`{44E83376-…}_is1` is the ProductCode the `Ollama.Ollama` winget manifest keys
off. xollama now ships its own:

```
AppId={{F6F806B4-09B2-43BA-8413-B1D52561CA60}
```

**Changing the GUID is what stops the auto-upgrade** — the rest is cosmetic:
`MyAppName`, `MyAppPublisher`, `DefaultDirName`, `OutputBaseFilename`, and
`app/xollama.rc`.

The same GUID also appears in `scripts/install.ps1` as `$InnoSetupUninstallGuid`,
which is how that script finds an existing install **and how it uninstalls
one**. Left pointing at upstream's key, `XOLLAMA_UNINSTALL=1` would have
uninstalled the user's real Ollama.

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
- `app/xollama.iss` installer identity, per the section above
- README and docs

Explicitly **not** renamed: the Go module path, package names, the API
routes, the data directory, the registry protocol, or anything in `LICENSE`.
Upstream's MIT licence and copyright stay exactly as they are.


## What shipped

| area | before | after |
|---|---|---|
| CLI / binary | `ollama` | **`xollama`** |
| display name | Ollama | **xOllama** |
| Windows installer | `OllamaSetup.exe`, AppId `{44E83376-...}` | `xOllamaSetup.exe`, AppId `{F6F806B4-...}` |
| macOS bundle | `Ollama.app`, `com.electron.ollama` | `xOllama.app`, `com.mann1x.xollama` |
| macOS LaunchAgent | `com.ollama.ollama.plist` | `com.mann1x.xollama.plist` |
| systemd unit | `ollama.service` | `xollama.service` |
| release assets | `ollama-linux-amd64.tar.zst`, ... | `xollama-linux-amd64.tar.zst`, ... |
| release source | `ollama.com/download` | `github.com/mann1x/xollama/releases` |

Deliberately unchanged, because they are **data identity**, not product
identity: the Go module path, the API routes, `~/.ollama`, the `OLLAMA_*`
environment variables, the `lib/ollama` payload directory, and the `ollama`
system user with its `/usr/share/ollama` home. That user's home *is* the models
directory, so keeping it is what lets an existing install's blobs be reused
instead of re-downloaded.

Also deliberately unchanged: every reference to **ollama.com, the Ollama
account and Cloud models**. Those are upstream's service, which this fork still
signs into - renaming them would have produced nonsense like "sign in to
xollama.com".

Three things the rename had to get right beyond string replacement:

- **`scripts/install.sh` downloaded from `ollama.com/download`.** Left alone it
  would have installed *stock ollama* under the xollama name. It now points at
  this fork's GitHub releases, and honours `XOLLAMA_VERSION` over
  `OLLAMA_VERSION`.
- **`scripts/install.ps1` required an Authenticode signature by `O=Ollama
  Inc.`** and threw otherwise. This fork is not signed by Ollama Inc. and must
  not pretend to be, so the check could not simply be retargeted; dropping it
  would install whatever the network returned. It now pins a signer when
  `XOLLAMA_EXPECTED_SIGNER` is set and otherwise verifies the download against
  the SHA256 published in the release's `sha256sum.txt`.
- **The Windows uninstaller no longer deletes `~/.ollama/history`.** That path
  is shared with a stock ollama install, which xollama is designed to sit
  beside, so removing it on uninstall would destroy the other install's data.

`go build .` still emits a binary called `ollama`, because Go names its output
after the module's last path element and the module path deliberately does not
change. Build with `go build -o xollama .`; both names are gitignored.
