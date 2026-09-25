# Release protocol

How a commit on `dev` becomes an xOllama release that people can install and
that installed copies update to. This covers the Windows installer, the
standalone binaries, the checksums and the channels. The container image has its
own workflow, `docs/features/docker-release.md`, and is **not yet** part of this
cycle (see *Known gaps*).

Written 2026-09-25, when the fork had never cut a release: origin had no tags, no
runners and no release workflow of its own, and installs were exes copied onto
the test host by hand. **If you are about to scp a binary onto a machine and call
it an update, stop. That is the procedure this replaces.**

## The rules

1. **A release is a pull request.** Open it from `dev` into `main` and title it
   `release: v<version>`. The PR body becomes the release notes, verbatim, so
   there is no second place to write them. **A release PR with an empty body
   does not build.**
2. **CI builds and publishes. Nobody does it by hand.** The build runs on
   GitHub-hosted runners in `.github/workflows/xollama-release.yaml`. No local
   host and no self-hosted runner is involved, because every native input
   already exists as a published, pinned artifact (see *Where the bytes come
   from*). The runners only compile the Go binary, the tray app, the CPU
   runtime and the installer.
3. **Never push a `v*` tag.** The workflow creates the tag when it publishes, on
   the merge commit. A hand-pushed tag names a commit no release was built from.
   It also wakes upstream's `release.yaml`, which the `fork-release` hook now
   skips, and `docker-release.yaml`, which waits for a runner that does not
   exist.
4. **Merge the PR with a merge commit.** Do not squash or rebase it. The workflow
   refuses to release if the PR's head is not an ancestor of what landed on
   `main`. A squash would leave `main` without `dev`'s history, and the next
   upstream sync would have no merge-base to work from.
5. **Every release starts as a pre-release.** Only people who opted in are
   offered it (`XOLLAMA_UPDATE_PRERELEASE=1`). It becomes a stable release, and
   is offered to everyone, only after it has been installed and checked on the
   test host (steps 6–8). Promotion is one explicit command, never a side effect
   of something else.
6. **Verify the artifact, not the build log.** A green run means the steps
   exited 0. It does not mean the installer carries what it should. Steps 5 and
   7 exist for that.

## Versions and tags

```
v<upstream>-xollama.<n>        e.g. v0.34.2-xollama.1
```

- `<upstream>` is the upstream ollama release that `main` is based on. It is the
  first part of `main`'s identity in CLAUDE.md ("upstream release v0.34.2 + fork
  changes"). The workflow checks that ollama/ollama has a tag `v<upstream>`,
  because the GPU backends come from that release (see below).
- `<n>` counts from 1 and restarts at 1 when `<upstream>` moves.
- The binary reports the tag without the leading `v`
  (`xollama --version` → `0.34.2-xollama.1`). The fork's name has to appear in
  that string: `ResolveHost` (`api/xollama_host.go`) tells an xollama apart from
  a stock ollama by it when `/api/xollama` is missing.
- Ordering is semver, and `app/updater/fork.go` relies on it.
  `0.34.2-xollama.2 > 0.34.2-xollama.1`, and `0.34.3-xollama.1` is greater than
  both. A local dev build (`0.34.2-xollama-05c16dfa`) sorts **above** every
  `xollama.<n>` of the same base, so a developer's hand-built binary is never
  offered a "downgrade" to a release.
- The workflow refuses a version that is not greater than every release already
  published, and it refuses a tag that already exists.

The channel is **not** part of the version. It is the GitHub pre-release flag.
Both channels use the same bytes, so promoting a release rebuilds nothing.

## What a release carries

| asset | built from | who uses it |
|---|---|---|
| `xOllamaSetup.exe` | `app/xollama.iss`, full payload | first installs, and updates whose payload changed |
| `xOllamaUpdate.exe` | same, `/DCORE=1` (no `lib\ollama`) | updates whose payload did not change |
| `payload-id.txt` | `payloadId` in `scripts/build_windows.ps1` | the updater, to pick between the two installers |
| `xollama-windows-amd64.exe` | `go build`, static | anyone replacing only the CLI/server |
| `xollama-linux-amd64` | `go build` inside AlmaLinux 8 (glibc 2.28) | Linux hosts that already have a runtime |
| `sha256sum.txt` | every asset above | the updater verifies the installer against it before running it |

The asset names are a contract with `app/updater/fork.go`. It matches
`xOllamaSetup.exe` / `xOllamaUpdate.exe` exactly and ignores everything else. A
misnamed installer is not an error there. The updater simply never sees it.
The final job of the workflow asserts the exact set, so it fails if any asset is
missing or an unexpected one is present.

The two installers are needed because the payload (`lib\ollama`, about 1 GB of
CUDA and Vulkan) rarely changes while the executables change every release.
`payload-id.txt` hashes the payload the full installer carries, using the same
exclusions as its `[Files]` section, and `lib\ollama\PAYLOAD_ID` records what is
installed. When the two match, the updater downloads the small installer.

## Where the bytes come from

Nothing native is compiled beyond the CPU runtime. Each GPU input is a pinned,
published artifact, and each has a check that fails the run when the pin no
longer fits.

| payload part | source | the check |
|---|---|---|
| `llama-server.exe` + CPU `ggml-*.dll` | built in CI with MSYS2 clang64, `llama/server` preset `cpu_windows`, the same toolchain the shipped DLLs were built with | `LLAMA_CPP_VERSION`, and the `llama/compat` patches must be present in the fetched source before compiling (`WAITING_BOUNDARY`, `REASONING_BUDGET_SCOPE_RESPONSE`, `forced_end_pos`) |
| `cuda_v13\`, `vulkan\` | upstream's `ollama-windows-amd64.zip` from ollama/ollama release `v<upstream>` | upstream's `LLAMA_CPP_VERSION` at that tag must equal ours, or the run fails |
| opencoti-llamafile | `llm/engine/pin.txt` through `cmake/opencoti-fetch.cmake`, SHA-256 enforced | added only when the pin has a `bin win-x86_64-gpu` row |

Why the GPU backends can come from upstream: they are loaded by ggml's
backend loader through ggml's C ABI. When both sides are built from the same
llama.cpp revision, upstream's `ggml-cuda.dll` works with our `ggml-base.dll`.
The fork's `llama/compat` patches touch `common/` and the hooks, not ggml, so
they do not change that ABI. The version guard is what makes this safe. When
`dev` moves `LLAMA_CPP_VERSION` ahead of the upstream release it is based on,
the release fails loudly. It must not ship a mismatched pair.

Why the CPU runtime is built here and not taken from the fork's release: the
fork's `ollama-windows-amd64-runtime.zip` is built from `think-budget` at its
own point in time. It can lag the `llama/compat` patches that `dev` carries, and
it did: the `v0.34.2-1-thinkbudget` runtime predates the 004 reasoning-budget
fix. Building it from the release commit makes the runtime match the Go code
that drives it.

**opencoti on Windows depends on the pin.** When `llm/engine/pin.txt` has no
Windows row, the installer carries llama.cpp only, and `pinUncovered` routes
Windows to it. That is the stated property of that snapshot, not a build
failure. The run writes the result into the release notes: engine included,
or "llama.cpp only on Windows at this pin". Adding a Windows row is a pin move,
so the measurement rules in `llm/engine_defects.go` apply to it, not this
protocol.

`cuda_v12` is not shipped (the `.iss` excludes it; see the comment there). The
same goes for ROCm, MLX and Windows arm64. Adding any of them means adding its
source to this table in the same commit.

## The cycle

### 1. Land the work on `dev`

The usual rules apply (UPSTREAM-SYNC hooks, FORK-SYNC merges, tests). Push
`origin/dev`.

### 2. Open the release PR

```sh
gh pr create --repo mann1x/xollama --base main --head dev \
  --title "release: v0.34.2-xollama.1" \
  --body-file notes.md
```

Write the body as the release notes: what changed for the user, what to watch
out for, and what is not included. Start headings at `##`. The workflow appends
a provenance block (commit, upstream base, llama.cpp pin, engine pin, payload
id), so do not write that part.

Opening the PR, editing it or pushing to `dev` while it is open runs the
workflow's **check** job only. It validates the title, the version order, the
tag, the non-empty body and the upstream GPU source, and it builds nothing.
Fix whatever it reports before going further.

The inherited `test.yaml` also runs on the PR. Its native legs target upstream's
self-hosted `linux`/`windows` runners, which this repository does not have, so
they stay queued. They are not required checks, so cancel them. The hosted legs
are the ones that count.

### 3. Dry-run it (recommended, and required for the first release of a new upstream base)

```sh
gh workflow run xollama-release.yaml --repo mann1x/xollama -f pr=<N>
```

A manual run against an **open** PR builds the PR's head and publishes the
result as a **draft** release. Drafts are invisible to the updater and to
anyone without write access, and no tag is created. Download it, install it on
the test host (steps 6–8) and delete it afterwards:

```sh
gh release delete v0.34.2-xollama.1 --repo mann1x/xollama --yes
```

A later run for the same tag replaces a draft. It never touches a published
release.

### 4. Merge

Use the **Create a merge commit** button, or
`gh pr merge <N> --merge --repo mann1x/xollama`. The merge triggers the
**release** run on the merge commit:

1. `plan` validates again and creates a draft release that the other jobs fill.
2. `windows` builds the runtime, borrows the GPU backends, fetches the engine,
   builds the Go binary, the UI, the tray app and both installers, and uploads
   them.
3. `linux` builds the Linux binary in AlmaLinux 8 and uploads it.
4. `publish` checks the exact asset set and sizes, writes `sha256sum.txt`, and
   only then turns the draft into a **pre-release**. That also creates the tag
   `v<version>` on the merge commit.

Until that last step nothing is visible. A failed run leaves a draft behind,
and a re-run replaces it:

```sh
gh workflow run xollama-release.yaml --repo mann1x/xollama -f pr=<N>
```

(On a **merged** PR a manual run is a real release, not a dry run.)

### 5. Verify the artifact

```sh
tag=v0.34.2-xollama.1
d=backup_models/release-check/$tag      # persistent disk, never /tmp
mkdir -p "$d" && cd "$d"
gh release download "$tag" --repo mann1x/xollama --clobber
sha256sum -c sha256sum.txt                       # every line OK
./xollama-linux-amd64 --version                  # names $tag without the v
strings xOllamaSetup.exe | grep -c 'repos/mann1x/xollama/releases'   # not 0 (Inno compresses; if 0, check the installed app in step 7)
```

The workflow already refuses a binary that imports MinGW DLLs and a tray app
that does not name the fork's releases endpoint. This step checks what reached
the release, not what the job had on disk.

### 6. Deploy to the test host

The test host is **eleven2go** (Windows, RTX 3090; `ssh eleven2go` lands in
`cmd`, so send PowerShell on stdin:
`ssh eleven2go 'powershell -NoProfile -ExecutionPolicy Bypass -Command -' < script.ps1`).

Copy the installer across with its full destination path, then run it:

```sh
scp "$d/xOllamaSetup.exe" 'eleven2go:C:/Users/ManniX/Downloads/xOllamaSetup.exe'
```

```powershell
# stop a running xollama first; the installer's TaskKill covers xollama.exe and the tray app
& 'C:\Users\ManniX\Downloads\xOllamaSetup.exe' /SILENT /SUPPRESSMSGBOXES /NORESTART
```

It installs per user into `%LOCALAPPDATA%\Programs\xOllama` (no elevation).

### 7. Verify the install

Check each of these on the host. The installer's exit code is not enough.

- `& "$env:LOCALAPPDATA\Programs\xOllama\xollama.exe" --version` names the tag.
- `lib\ollama\PAYLOAD_ID` equals the release's `payload-id.txt`.
- The server is listening on **22434**, and `GET /api/xollama` answers.
- `GET /api/xollama/devices` lists the GPU with the backend you expect.
- A generation of at least 512 tokens on the GPU, with tok/s recorded. Thinking
  models need a generous `num_ctx`/`num_predict`.
- Anything else on the host is untouched (see below). On eleven2go that means
  the think-budget ollama still answers on **11434**.

Record the result in `.wolf/memory.md` and pgvector (host, tag, tok/s, anything
odd).

### 8. Promote

Promote only after step 7 passes:

```sh
gh release edit v0.34.2-xollama.1 --repo mann1x/xollama --prerelease=false --latest
```

From that point every installed xOllama on the stable channel is offered the
release. Nothing is rebuilt.

A release that fails step 7 stays a pre-release. Fix it on `dev` and cut
`xollama.<n+1>`. A published tag is never moved or reused. If the bad
pre-release should not stay on offer, delete the release (`gh release delete`,
which leaves the tag) so the updater stops seeing it.

## Rollback

Installed copies never downgrade on their own, because the updater only moves
forward. To take a host back, run the previous release's `xOllamaSetup.exe`
over the top. It is the same AppId, so it replaces the files in place and keeps
models and settings. To take a bad stable release away from users, delete it
(or mark it pre-release again) and cut the fix as the next `<n>`.

## What installing does NOT touch

- **Other ollama installs.** xOllama has its own AppId, install directory,
  Add/Remove entry and port (22434). On eleven2go the "Ollama think-budget"
  install in `%LOCALAPPDATA%\Programs\Ollama` has its own `lib\ollama`. The
  xOllama installer does not read it, write it or uninstall it.
- **The model store.** Models are not in the installer, and uninstalling does
  not remove them.
- **`ollama://`.** It is registered only when nobody already owns it
  (`OllamaSchemeUnclaimed`, see `docs/features/windows-installer.md`).

Files put on a host by hand before this protocol existed (for example
`xollama-new.exe` and `run-xollama.cmd` on eleven2go) are removed **after** the
installed release passes step 7, never before. They are the fallback until then.

## Known gaps

- **Unsigned.** There is no code-signing certificate, so SmartScreen warns on
  the first run of the installer. Upstream's signing step (`KEY_CONTAINER`,
  Google KMS) is not wired up. The release notes should say so until it is.
- **Container image.** `docker-release.yaml` needs the bs2 runner, which has not
  been registered. Its automatic channel rule ("any hyphen is a pre-release")
  would also send every `-xollama.<n>` tag to `:dev`. Before the image joins
  this cycle, the channel has to come from the GitHub pre-release flag, the way
  it does here. Tags created by the workflow's `GITHUB_TOKEN` do not trigger
  other workflows, so this cycle cannot start it by accident.
- **Inno `AppVersion`** is the numeric `<upstream>` (`0.34.2`), because
  `VersionInfoVersion` must be numeric. Every `xollama.<n>` on one base shows
  the same version in Add/Remove Programs. Upgrades still work. Use
  `xollama --version` to tell them apart.
- **No macOS, no Windows arm64, no Linux runtime archive.** Linux hosts
  (solidPC) are still deployed from a local build with the full deployment
  script.
