# The Windows installer, and the update path it hands off to

## What already existed

`app/xollama.iss` is an Inno Setup script, built by
`scripts/build_windows.ps1 installer` and produced as `dist\xOllamaSetup.exe`.
The `windows-app` job in `.github/workflows/release.yaml` runs
`deps sign installer installerUpdate zip`, and the `release` job **fails** if
`xOllamaSetup.exe`, `xOllamaUpdate.exe` or `payload-id.txt` is missing — so
neither the installer nor the delta path is optional to a release.

It installs per-user into `%LOCALAPPDATA%\Programs\xOllama` with
`PrivilegesRequired=lowest`, puts `{app}` on the user's `Path`, registers a
URL protocol handler, and excludes `cuda_v12` from the payload — that
backend ships as a separate legacy zip because the installer would otherwise
cross GitHub's 2 GiB release-asset cap now that an engine ships inside it.

It has never been released: as of 2026-09-22 the repository has no tags and no
releases. The pipeline is complete and has simply never been run.

## Why Inno Setup and not something else

Considered and rejected, briefly, so the next person does not re-run it:

- **MSI / WiX** — the enterprise answer, and genuinely better for GPO or Intune
  deployment. It handles a ~1.5 GB per-user payload badly, per-user MSI is
  awkward, and a ProductCode makes external-updater correlation *easier*, which
  is the one thing this fork must avoid. Worth revisiting only if someone asks
  to deploy xollama to a fleet.
- **NSIS** — a lateral move from Inno with a worse wizard and worse scripting.
- **MSIX** — ruled out by the repository owner (no Microsoft Store), and the
  app-container model fights a CLI on `PATH` and GPU DLL loading anyway.
  Sideloading still needs a trusted certificate, so it does not even buy the
  signing problem back.
- **Velopack / Squirrel** — the good ideas there are about *updating*, not
  installing, and they can be adopted without changing installer. They assume
  they own the install layout, which a 1.5 GB payload with a `PATH` entry and
  native backends does not fit.

Staying on Inno also keeps `app/xollama.iss` a small, readable diff against
upstream's `app/ollama.iss`, which is the entire point of
[UPSTREAM-SYNC.md](../protocols/UPSTREAM-SYNC.md).

## The Add/Remove Programs entry is a security surface

The fork has already been overwritten once by an updater it did not own. A
stock-ollama winget manifest carries no `ProductCode`, so winget correlates on
**DisplayName + Publisher**; the Microsoft Store's background updater drove
`WindowsPackageManagerServer.exe` through that correlation and "upgraded" a
fork install back to stock. Nothing inside the application could see it, and
nothing inside the application could stop it.

So the entry this installer writes must never read like Ollama's:

| Directive | Value | Why |
|---|---|---|
| `AppId` | `{F6F806B4-…}` | our own GUID, never upstream's |
| `UninstallDisplayName` | `xOllama` | the DisplayName half of the correlation |
| `AppPublisher` | `ManniX` | the Publisher half |
| `VersionInfoProductName` / `VersionInfoCompany` | `xOllama` / `ManniX` | the setup binary's own version resource |

Changing any of those to match upstream re-opens the hole.

## The update path

Upstream's updater, which this fork inherited verbatim, does three things that
are wrong here in the same direction:

1. it asks `https://ollama.com/api/update`, which answers for Ollama;
2. it signs that query with the user's own SSH key and sends os, arch, version
   and (on macOS) the install's device id;
3. it installs the result if it is Authenticode-signed by **`Ollama Inc.`** —
   a check that **passes** for a stock ollama installer and **fails** for ours.

The background checker runs every hour and `auto_update_enabled` defaults to
`1`, so the composite behaviour was: a fresh xollama install polls ollama.com,
downloads stock `OllamaSetup.exe`, verifies it (correctly — it really is signed
by Ollama Inc.), and runs it `/VERYSILENT` over the top. It was inert only
because nothing had shipped.

`app/updater/fork.go` replaces the feed. Four differences from upstream, each
deliberate:

**The feed is the fork's own releases.**
`https://api.github.com/repos/mann1x/xollama/releases`. No server to run, and
it works the day a tag is pushed. `XOLLAMA_UPDATE_FEED` redirects it for a
private mirror or a test.

**Nothing identifying is sent.** No signature, no device id, not even the
version — a release listing is a static document, and the answer does not
change based on who asks. Asking with the user's key attached would leak a
stable identifier for nothing.

**The release is chosen here, not by the server.** The release job creates
every release as `--draft --prerelease`, which `/releases/latest` skips
entirely, so the list endpoint is used and the newest allowable release is
picked locally with a semver comparison. Pre-releases are refused unless
`XOLLAMA_UPDATE_PRERELEASE` is set — so an update happens when the owner
promotes a build, not when CI finishes. A release that carries no installer for
this platform is not an update for this platform, however new it is.

**The answer is not trusted.** The check records the sha256 that the release's
own `sha256sum.txt` gives for the asset it points at — from *the same release*,
so one release cannot hand out another's checksum — and the download is
rejected unless the bytes match. This is the check that distinguishes our
installer from another product's, which a signature check structurally cannot:
a stock ollama installer is genuinely, validly signed. A release that publishes
no checksum is refused rather than installed; refusing costs the user an update
they could have had, accepting costs the only guarantee this feed has.

The staged filename comes from `content-disposition`, which the feed controls,
so the digest is bound to the asset name it was recorded for.

### Signing

There is no code-signing certificate for xollama today, so the Authenticode
requirement is demoted to advisory: the signature is reported when one is
present and valid, and its absence is not fatal because integrity is carried by
the digest instead. The allowlist now names **our** organisation, never
`Ollama Inc.` The code that reads the signer is left in place; restore it to a
hard requirement the day a certificate exists. Until then users see a
SmartScreen warning on first install, which is the honest cost of the choice.

### `%LOCALAPPDATA%`

Upstream's Windows updater stages into `%LOCALAPPDATA%\Ollama` and, on startup,
deletes `%LOCALAPPDATA%\Ollama\updates` to clean up after the old desktop app
it replaced. On this fork both are wrong: that directory belongs to the stock
ollama xollama is designed to sit beside, the uninstaller in `app/xollama.iss`
only removes the `xOllama` one, and the sweep would delete a stock install's
staged update. Staging, the upgrade log and the marker file now live in
`%LOCALAPPDATA%\xOllama`, and the sweep is gone.

The rest of the app's state followed on 2026-09-25 (`app-state` hook). That
includes `app.log`, `server.log`, `ollama.pid` and the settings database
`db.sqlite`. The updater had moved, but these had not, and the first real
install found out why they must. On eleven2go a think-budget ollama was
running beside it and held `Ollama\server.log` open, so the app could not open
its server log. `Run` returned that error to a channel nobody logs, and the
xOllama server never started. `cleanup()` would also have stopped whatever
process a stock app had written into the shared `ollama.pid`. Settings are
**not** migrated from `%LOCALAPPDATA%\Ollama`, because they belong to the stock
app.

The login shortcut is `Startup\xOllama.lnk`, copied from the
`{app}\lib\xOllama.lnk` the installer creates. Until then the app looked for
`lib\Ollama.lnk`, which the installer never created. It also treated a stock
install's `Startup\Ollama.lnk` as its own, so xOllama never registered itself
to start at login.

## Delta updates

An update used to cost ~1.5 GB whatever changed. Measured on this payload:

| | size |
|---|---|
| `lib\ollama` total | ~1.5 GB |
| — the opencoti artifact | ~700 MB |
| — `cuda_v13` (mostly cuBLAS) | ~785 MB |
| `xollama.exe` | ~36 MB |

So a release that only moves Go code was shipping forty times the bytes it
changed, and the part that dominates — a pinned engine and a CUDA toolkit —
moves on its own, much slower schedule.

The split follows that cadence rather than any file-type boundary. The release
publishes **two installers built from the same `app/xollama.iss`**:

- `xOllamaSetup.exe` — everything. First install, and any release whose payload
  moved.
- `xOllamaUpdate.exe` — the executables and nothing under `lib\ollama`, built
  with `/DCORE=1`.

There is no diff format, no patch applier and no bespoke file replacer. The
small installer is the same Inno installer that already knows how to stop the
tray app, rewrite `PATH`, keep the uninstall entry and hand over to the running
process — it simply carries less. That is the point: the risky part of an
update is the apply step, and this changes only the download.

### Payload identity

`payloadId` in `scripts/build_windows.ps1` hashes relative path plus content
digest for every file, sorted, over **the set the installer actually ships** —
`cuda_v12` and `mlx_*` are excluded there, so they must be excluded here too or
every release would look like a payload change and the split would buy nothing.

That digest goes three places:

1. into the full installer, written to `{app}\lib\ollama\PAYLOAD_ID` — inside
   the payload, so it cannot outlive it;
2. into the release, as the `payload-id.txt` asset;
3. into the update-only installer, as `PKG_PAYLOAD_ID`.

`chooseCoreInstaller` in `app/updater/fork.go` compares (1) against (2) and
takes the small installer only when they match. **Every way of not knowing falls
back to the full installer**: no core asset for this platform, no marker on
disk, no `payload-id.txt` in the release, a marker that is not a bare sha256, or
a mismatch. Not knowing costs bytes; it never costs correctness.

`payloadId` reports its progress with `Write-Host`: a PowerShell function
returns everything it writes to the output stream, and with `Write-Output` the
update installer of v0.35.1-xollama.1 was compiled against the progress line
and the id together, so it refused every install. `requirePayloadId` now stops
a build whose id is not a bare sha256, and the release workflow runs both
installers on the runner (step "the installers install") before attaching them.

The update-only installer checks the same thing again in `PrepareToInstall`
(`PayloadRefusal`; `{app}` does not exist in `InitializeSetup`, and asking there
ended every run of the v0.35.1-xollama update with a runtime error) and
refuses, before it stops anything, with a message naming `xOllamaSetup.exe` — because it can also be run by
hand, and an install with executables but no engine is worse than no install. It
also must never appear in `[InstallDelete]`'s sweep of `{app}\lib\ollama`,
which is why that entry is `#ifndef CORE`.

## The setup pages

An interactive install walks four pages, all in `app/xollama-setup-pages.iss`
(included from `xollama.iss`, full installer only). A silent install -- the
updater's path -- shows none of them and changes nothing they would.
In particular it never removes Ollama: `InitializeWizard` builds the Ollama
page only when `not WizardSilent()`, and `NextButtonClick` checks it again.
v0.34.4-xollama.1 built the page anyway, and a silent wizard takes every
default, which was "uninstall Ollama". On eleven2go (2026-09-27) `/SILENT` ran
Ollama's uninstaller, which gave up only because Ollama was running.

1. **Ollama found.** Any Add/Remove Programs entry named `Ollama` or
   `Ollama <something>` (stock, or a fork such as `Ollama think-budget`), in
   HKCU or either HKLM view, or a bare `Programs\Ollama\ollama.exe`.
   - *Uninstall first* runs its uninstaller `/SILENT`. The silent mode matters:
     upstream's interactive dialog ticks "Remove models" by default, silent mode
     never deletes them. `%LOCALAPPDATA%\Ollama` (its app database and chats) is
     first copied to `~\.ollama\ollama-app-backup-<timestamp>`, which neither
     uninstaller removes. Inno uninstallers relaunch from `%TEMP%` and return at
     once, so the page waits (up to 3 min) for `unins000.exe` to disappear, then
     detects again.
   - *Keep it* runs xOllama beside it.
2. **Port.** 22434 (default), 11434, or custom. 11434 is greyed out, labelled
   "in use by Ollama", while an Ollama stays installed; it comes back if the
   page before removed it. Writes `XOLLAMA_HOST` to `HKCU\Environment` only for
   a port other than the default, keeping a scheme or host the user already
   had; a variable the install created is removed on uninstall, one the user
   had is not.
3. **API key.** Optional; empty skips (or keeps an existing key). *Generate*
   draws 256 bits from `BCryptGenRandom` in the server's own `xok_` +
   base64url format; *Copy* goes through `clip.exe` from a temp file, so the key
   never meets a command line. Writes `~\.ollama\xollama-server.json` (the
   SHA-256, as `envconfig.ServerKeyFileContent`) and this user's
   `~\.ollama\xollama-api-key`.
4. **KV cache.** `XOLLAMA_K_CACHE_TYPE` and `XOLLAMA_V_CACHE_TYPE` for opencoti
   (plain types and `kvarn2`–`kvarn8`, turbo tiers left out as frozen; a KVarN
   half needs a KVarN other half), and `XOLLAMA_KV_CACHE_TYPE` (`f16`, `q8_0`,
   `q4_0`), the legacy type stock llama.cpp falls back to -- read only by
   xOllama, so a stock Ollama beside it keeps `OLLAMA_KV_CACHE_TYPE`. Each list
   starts on "leave as is".

The first launch comes from the installer, whose environment predates what it
just wrote, so `AppRunParams` hands the new values to that launch explicitly.

## Things the installer must not claim

Two entries in this script were quietly taking something a stock ollama install
owns. Both are the same rule as the listen port
(`.claude/rules/default-port.md`): a cache type is shareable, an identity is not.

- **The `ollama://` URL protocol.** It was registered under
  `HKCU\Software\Classes\ollama` with `uninsdeletekey`, so installing xollama
  made it the handler for a stock install's links, and *uninstalling* xollama
  deleted the key — leaving a working ollama whose links opened nothing.

  It cannot simply be dropped, which is the part worth writing down: sign-in
  opens `https://ollama.com/connect?…&launch=true`, and **ollama.com** chooses
  the scheme it redirects back on. That is `ollama://connect`, and we do not
  control it. Declaring only `xollama://` would leave that redirect with no
  handler at all on a machine with no stock ollama, and break sign-in silently.

  So the installer always registers `xollama://`, and registers `ollama://`
  **only when `OllamaSchemeUnclaimed` finds no existing registration** in
  `HKEY_CLASSES_ROOT` (the merged HKLM + HKCU view, so a stock install in either
  hive keeps it). The `Check` gates the recorded uninstall action too: a row
  that was never installed is never removed, so this can no longer delete
  somebody else's key. `app/cmd/app/app.go` accepts either scheme.
- **`~/.ollama/models` on uninstall.** The uninstaller offers to delete it, and
  the checkbox was **pre-ticked** — six lines below a note in
  `[UninstallDelete]` saying that directory is shared and must be left alone.
  The dialog's default action is "Uninstall", so the default path through it
  deleted a stock install's model store. It is now unticked, and the caption
  says the directory is shared.

## Still open

- **Nothing has been released.** No tag, no release, so the feed has nothing to
  find. The first tag is what turns all of this on.
- **The delta is two tiers, not many.** A CUDA toolkit bump and an engine pin
  move both read as "payload changed" and cost the full download even though
  they are independent. Splitting `lib\ollama` into per-component archives with
  their own digests would go further, at the cost of an apply step that is no
  longer a single installer run.
- **Payload identity is checked by digest only.** The installer now writes
  `VersionInfoProductName` into its version resource, so a future check can
  read it back and refuse anything that is not `xOllama` before running it.
  That belt-and-braces check is not implemented; the digest is the gate.
- **macOS** takes the same feed (it must — the fork must not update to upstream
  there either), but has no payload split: `CoreInstaller` is empty there, so
  every update is the full bundle.
- Nothing outstanding on the URL schemes. The darwin bundle declares
  **both** `xollama` and `ollama`, which is right there and wrong on Windows for
  one reason: LaunchServices arbitrates between claimants and lets the user
  choose, and removing a bundle never removes another app's registration, so
  declaring a shared scheme on macOS takes nothing from anyone. The Windows
  registry key had neither property, which is why only that side is conditional.
  `app/darwin/xOllama.app/Contents/Info.plist` also had `CFBundleDisplayName`
  still set to `Ollama` while `CFBundleName` said `xOllama` — so Finder, the
  Dock and the Open With list all named the app `Ollama`, which is the macOS
  twin of the Add/Remove Programs DisplayName above.

## Stopping xOllama before an install or uninstall

`app/xollama-stop.ps1` stops xOllama's own processes and waits for them to
exit. The installer runs it from `PrepareToInstall`, before any file is
replaced (the engine and runners under `lib\ollama` are copied after the
executables), and the uninstaller runs it from `[UninstallRun]`. It stops:

- every process whose executable is under the install directory;
- the engines and runners (`llama-server`, `opencoti-*`, `xollama`) those
  started, wherever they run from, since `XOLLAMA_ENGINE_PATH` can point
  anywhere;
- an opencoti engine whose parent is gone. Only xOllama launches opencoti; one
  started from a shell has a live parent and is left alone.

It replaces `taskkill /im llama-server.exe`, which also stopped a stock
Ollama's runners beside it and never reached the opencoti engine.
`-List` prints what it would stop without stopping anything.
