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
   from*). The runners only compile the Go binaries, the tray app and the
   installers.
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
   of something else. **A release candidate is never promoted**: the release
   made from it is (see *Versions and tags*).
6. **Verify the artifact, not the build log.** A green run means the steps
   exited 0. It does not mean the installer carries what it should. Steps 5 and
   7 exist for that.

## Versions and tags

Every version follows the upstream ollama release `dev` is based on. Upstream
v0.35.0 is only ever released here as a `v0.35.0-…xollama…` version:

| stage | tag | channel |
|---|---|---|
| release candidate *k* | `v<upstream>-rc.<k>.xollama`, e.g. `v0.35.0-rc.1.xollama` | pre-release, **never promoted** |
| the release | `v<upstream>-xollama`, e.g. `v0.35.0-xollama` | pre-release, promoted after the short check |
| re-release *n* | `v<upstream>-xollama.<n>`, e.g. `v0.35.0-xollama.1` | pre-release, promoted after the full check |

- `<upstream>` is the upstream ollama release that `dev` is based on (the first
  line of CLAUDE.md). The workflow checks that ollama/ollama has a tag
  `v<upstream>`, because the GPU backends come from that release (see below).
- `<k>` and `<n>` count from 1 and restart when `<upstream>` moves. The
  workflow refuses 0, and refuses any other shape.
- **Candidates are the dev builds.** Cut as many as the work needs. Each one is
  installed and checked on the test host like any release (steps 5–7), and is
  offered to installs on the pre-release channel.
- **The release ships the tree of its last candidate.** The version is stamped
  into the binaries, so the release is a rebuild, not a promotion of the
  candidate's bytes. The workflow refuses a `v<upstream>-xollama` PR whose tree
  is not the tree of the newest published `v<upstream>-rc.<k>.xollama`, and one
  with no candidate at all. A change after the last candidate is the next
  candidate. Because the tree and the pins are the same, the payload id is the
  same, and the release takes the **short check** (step 7).
- **A re-release has no candidates.** It fixes a release that is already out.
  It is cut from `dev` as `v<upstream>-xollama.<n>`, published as a pre-release,
  given the full check, and promoted as the same bytes. Once
  `v<upstream>-xollama` is published, a candidate of that upstream sorts below
  it and is refused.
- The binary reports the tag without the leading `v`
  (`xollama --version` → `0.35.0-rc.1.xollama`). The fork's name has to appear
  in that string: `ResolveHost` (`api/xollama_host.go`) tells an xollama apart
  from a stock ollama by it when `/api/xollama` is missing.

**Ordering is plain semver, and nothing may add a comparator of its own.** The
names are chosen so that semver (`app/updater/fork.go`) and `sort -V` (the
workflow's order check) agree:

```
v0.34.4-xollama.2 < v0.35.0-rc.1.xollama < v0.35.0-rc.2.xollama < v0.35.0-rc.10.xollama
  < v0.35.0-xollama < v0.35.0-xollama.1 < v0.35.0-xollama.2 < v0.35.1-rc.1.xollama
```

The candidate goes **before** the fork's name because a semver pre-release field
added after `xollama` always sorts higher: `v0.35.0-xollama.rc1` sorts after
`v0.35.0-xollama.3`, and `sort -V` puts it between `xollama` and `xollama.1`, so
the updater would offer a candidate to an install already on the release.
`TestReleaseNamesOrderAsTheyShip` holds the order.

A local build stamped by `git describe` after a candidate reads
`0.35.0-rc.1.xollama-3-g<sha>`, which sorts after that candidate and before the
next one. The cross-build deployed to test hosts is stamped
`0.35.0-dev.<sha>`, which sorts below every candidate of its base, so on the
pre-release channel it is offered the first candidate.

- The workflow refuses a version that is not greater than every release already
  published, and it refuses a tag that already exists. Tags from before this
  scheme (`v0.34.x-xollama.<n>`) keep their place in the order.

The channel is **not** part of the version. It is the GitHub pre-release flag.
A re-release is promoted as the bytes that were checked; a release is the
rebuild of a checked candidate's tree.

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

A release compiles nothing native. Every part of the payload is a pinned,
published artifact, and each pin has a check that fails the run when it no
longer fits. So the payload, and the payload id the delta update keys on, only
changes when one of these three pins moves.

| payload part | source | the check |
|---|---|---|
| `llama-server.exe` + CPU `ggml-*.dll` | `llama/runtime-pin.txt`: a `runtime-windows-amd64-<llama>-<digest>` release built once by `xollama-runtime.yaml` (MSYS2 clang64, `llama/server` preset `cpu_windows`) | the asset's sha256, and the pin's `inputs` digest must equal the release commit's digest of `LLAMA_CPP_VERSION`, `llama/server` and `llama/compat`. When the runtime was built, the compat patches had to be present in the fetched source (`WAITING_BOUNDARY`, `REASONING_BUDGET_SCOPE_RESPONSE`, `forced_end_pos`) |
| `cuda_v13\`, `vulkan\` | upstream's `ollama-windows-amd64.zip` from ollama/ollama release `v<upstream>` | upstream's `LLAMA_CPP_VERSION` at that tag must equal ours, or the run fails |
| opencoti-llamafile | `llm/engine/pin.txt` through `cmake/opencoti-fetch.cmake`, SHA-256 enforced | added only when the pin has a Windows `bin` row: `win-x86_64-gpu` when present, else a dev snapshot's bare `win-x86_64` APE with its `dso win-x86_64` CUDA DLL (`Pin.ArchFor`), staged in `lib\ollama\engines` as `<name>.exe` + `ggml-cuda.dll`, apart from `llama-server.exe` |

The Go binaries (`xollama.exe`, the tray app, `xollama-linux-amd64`) are
compiled, and they get one pinned toolchain too. `plan` reads the `go` line of
`go.mod` (upstream's, `go 1.26.0`) and takes the **newest patch release on that
line** from go.dev (`go1.26.8` on 2026-09-25). Both build jobs use exactly that,
each checks `go version` on what it built, and the notes record it. Upstream
builds its own releases with the bare `go.mod` version; we keep its line but
not its patch level, because go1.26.0 carries 20 standard-library fixes that
govulncheck finds reachable from this code. Before this, `GOTOOLCHAIN: auto`
let each runner decide, and `v0.34.2-xollama.1` shipped go1.26.0 on Windows and
go1.27.1 on Linux. A new patch release changes the Go binaries only; it does
not touch the payload or its id.

Why the GPU backends can come from upstream: they are loaded by ggml's
backend loader through ggml's C ABI. When both sides are built from the same
llama.cpp revision, upstream's `ggml-cuda.dll` works with our `ggml-base.dll`.
The fork's `llama/compat` patches touch `common/` and the hooks, not ggml, so
they do not change that ABI. The version guard is what makes this safe. When
`dev` moves `LLAMA_CPP_VERSION` ahead of the upstream release it is based on,
the release fails loudly. It must not ship a mismatched pair.

Why the CPU runtime is pinned and not rebuilt per release: the build is not
byte-reproducible. Two dry runs of `v0.34.2-xollama.1`, whose second commit
changed only Go code, produced payload ids `213e5a…` and `cd3148…`. So every
release would carry a new `payload-id.txt`, and the updater would always
download the full installer. Why it is ours and not the fork's
`ollama-windows-amd64-runtime.zip`: that one is built from `think-budget` at
its own point in time and can lag the `llama/compat` patches `dev` carries. It
did: the `v0.34.2-1-thinkbudget` runtime predates the 004 reasoning-budget fix.
The inputs digest ties our pin to the exact `llama/` tree it was built from.

### Moving the runtime pin

Move it in the **same PR** that changes `LLAMA_CPP_VERSION`, `llama/server` or
`llama/compat`. The release check names the mismatch if you forget.

A push to `dev` that touches any of those paths runs `xollama-runtime.yaml` by
itself. It skips when a runtime for the same inputs is already published. To
build by hand:

```sh
gh workflow run xollama-runtime.yaml --repo mann1x/xollama --ref dev -f ref=dev
```

The run's summary prints four lines (`tag`, `asset`, `sha256`, `inputs`).
Replace the directives in `llama/runtime-pin.txt` with them and commit. That
release carries a pre-release flag and a non-semver tag, so the updater never
offers it to anyone. Never delete a runtime release that a published xOllama
release was built from. Moving this pin changes the payload, so the first
update after it downloads the full installer, which is the correct behaviour.

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
  --title "release: v0.35.0-rc.1.xollama" \
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
gh workflow run xollama-release.yaml --repo mann1x/xollama --ref dev -f pr=<N>
```

`--ref dev` runs the workflow as `dev` has it. Without it GitHub uses `main`'s
copy, and until the first release lands `main` has none, so the dispatch fails
with HTTP 422 ("does not have 'workflow_dispatch' trigger"). The build itself
always uses the PR's commit, whatever `--ref` says.

A manual run against an **open** PR builds the PR's head and publishes the
result as a **draft** release. Drafts are invisible to the updater and to
anyone without write access, and no tag is created. Download it, install it on
the test host (steps 6–8) and delete it afterwards:

```sh
gh release delete v0.35.0-rc.1.xollama --repo mann1x/xollama --yes
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
tag=v0.35.0-rc.1.xollama
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
Keep every statement in such a script on **one line**. `-Command -` runs stdin
line by line, and a statement that continues onto the next line runs nothing
and prints nothing, so a failure looks exactly like success.

Run the installer through a one-time **interactive scheduled task**
(`New-ScheduledTaskPrincipal -UserId <user> -LogonType Interactive`), not
straight from SSH. The installer starts the tray app when it finishes. Started
from SSH, the app lands in the SSH session and is killed when that session
ends, so the install looks fine but nothing is left running.

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

A candidate and a re-release take the **full check**. The release made from a
candidate takes the **short check**, because the workflow has already proved it
is that candidate's tree with the same pins: install it over the candidate
through the updater's small installer, then check only that
`xollama --version` names the release, `lib\ollama\PAYLOAD_ID` equals its
`payload-id.txt`, `GET /api/xollama` answers on 22434, and one generation on the
GPU runs. Anything else failing in the short check means the rebuild differs
from the candidate: stop and investigate, do not promote.

The full check: verify each of these on the host. The installer's exit code is
not enough.

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

Promote only after step 7 passes, and only a release or a re-release, never a
candidate:

```sh
gh release edit v0.35.0-xollama --repo mann1x/xollama --prerelease=false --latest
```

From that point every installed xOllama on the stable channel is offered the
release. Nothing is rebuilt.

The promotion also announces the release on Discord:
`.github/workflows/discord-announce.yaml` runs on the `released` event, posts
the release notes through the `TECH_CORNER_DISCOWH` webhook (the same one
mann1x/osync uses), and never fires for a pre-release. It skips a candidate's
tag even if the candidate was promoted by mistake. Re-announce a tag with
`gh workflow run discord-announce.yaml --repo mann1x/xollama -f tag=<tag>`.
A failed post is a warning, not a failed release.

A candidate that fails step 7 stays a pre-release; fix it on `dev` and cut the
next candidate. A release or re-release that fails step 7 stays a pre-release
too; fix it on `dev` and cut the next re-release, `xollama.<n+1>`. A published tag is never moved or reused. If the bad
pre-release should not stay on offer, delete the release (`gh release delete`,
which leaves the tag) so the updater stops seeing it.

## Rollback

Installed copies never downgrade on their own, because the updater only moves
forward. To take a host back, run the previous release's `xOllamaSetup.exe`
over the top. It is the same AppId, so it replaces the files in place and keeps
models and settings. To take a bad stable release away from users, delete it
(or mark it pre-release again) and cut the fix as the next re-release
(`v<upstream>-xollama.<n+1>`).

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
- **Inno `AppVersion`** is the numeric `<upstream>` (`0.35.0`), because
  `VersionInfoVersion` must be numeric. Every candidate, release and re-release on one base shows
  the same version in Add/Remove Programs. Upgrades still work. Use
  `xollama --version` to tell them apart.
- **No macOS, no Windows arm64, no Linux runtime archive.** Linux hosts
  (solidPC) are still deployed from a local build with the full deployment
  script.
