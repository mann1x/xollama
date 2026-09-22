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

The update-only installer checks the same thing again in `InitializeSetup` and
refuses with a message naming `xOllamaSetup.exe` — because it can also be run by
hand, and an install with executables but no engine is worse than no install. It
also must never appear in `[InstallDelete]`'s sweep of `{app}\lib\ollama`,
which is why that entry is `#ifndef CORE`.

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
