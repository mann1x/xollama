# Publishing the container image

`.github/workflows/docker-release.yaml` assembles the xollama container image on
a GitHub-hosted runner and publishes it to two registries:

| registry | image |
|---|---|
| Docker Hub | `docker.io/mannixita/xollama` |
| GHCR | `ghcr.io/mann1x/xollama` |

## Nothing native is compiled

Every native piece of the image already exists as a published, sha256-pinned
artifact, so the image is **assembled**, not built (design:
`plans/docker-image.md`). `scripts/docker-assemble.sh` stages the context and
`Dockerfile.xollama` is the runtime layer; CI and a local check run the same
script.

| Layer | Source | Pinned by |
|---|---|---|
| llama.cpp CPU runtime (`llama-server`, CPU `ggml`, built with `llama/compat`) | the fork's release `v0.35.1-thinkbudget`, `ollama-linux-amd64-runtime.tgz` and `ollama-linux-arm64-runtime.tgz` | `llama/runtime-pin-linux.txt` and `llama/runtime-pin-linux-arm64.txt`: sha256, plus the inputs digest |
| CUDA v12, CUDA v13, Vulkan, MLX CUDA v13 | upstream `v0.35.1`: `ollama-linux-amd64.tar.zst`, `ollama-linux-amd64-mlx.tar.zst` | `llama/runtime-pin-linux.txt`: sha256; upstream's `LLAMA_CPP_VERSION` must equal ours |
| opencoti engine and its GPU libraries (CUDA 13, Vulkan) | HF, the components `llm/engine/pin/index.txt` names, staged in `lib/ollama` under their published names | `llm/engine/pin/` via `cmake/opencoti-fetch.cmake` (sha256 and size), re-checked by `scripts/docker-assemble.sh` against the fetch's manifest |
| opencoti media sidecars (`oc-codec`: mp3, opus, aac, mp4; `oc-audiocpp`: Kokoro, Supertonic, KittenTTS) | HF, the `media` component, staged beside the engine under their published names with their licence texts | as the engine |
| opencoti CUDA 12 payload (older cards, e.g. V100) | HF, the `cuda12` component: `ggml-cuda-cu12-x86_64.so` beside the same engine, which loads it for a load on such cards (`OPENCOTI_CUDA_LEGACY=1`) | as the engine |
| `xollama` | Go-only build in AlmaLinux 8 (glibc 2.28), `-buildmode=pie` | the commit; the Go toolchain is the newest patch on `go.mod`'s line |

The overlay order matters. Upstream's tarball goes down first, and the fork's
runtime replaces every CPU file in it: measured on the pinned bytes, upstream
contributes only its GPU directories, `libgomp`, and licence files.
`GO_LICENSE` is regenerated from this tree.

**The inputs digest.** The runtime was built by the fork, from its own
`LLAMA_CPP_VERSION`, `llama/server` and `llama/compat`. The assembly refuses to
build unless this commit's `git ls-tree` digest of those paths equals the
pinned `inputs`. It also checks that the pinned `built` fork commit has that
same digest, when the commit is reachable (CI fetches it). Both sides leave out
`llama/compat/README.md`: it is not a build input, and the two copies are
allowed to differ. Measured 2026-09-26, the fork at `d6e24119` and `dev` both
come to `eff6800e…`, and the README is their only difference. Move the pin in
the same change that moves those paths.

A new engine reaches the image through a pin move, not a CI change. The same
goes for a new runtime or new GPU backends.

**The engine's dlopen helper.** The engine is a Cosmopolitan APE; on Linux it
loads `ggml-cuda.so` through a small helper linked against the system libc,
which it compiles with the system `cc` into `$HOME/.cosmo` the first time. The
runtime image has no compiler, so every GPU load in the first images failed
with `dlopen() isn't supported on this platform` and then `support for --gpu
nvidia was explicitly requested, but it wasn't available` (found by the V100
tester, reproduced on solidPC's 3090, 2026-09-27). `Dockerfile.xollama` now
builds the helper from `scripts/cosmo-dlopen-helper.c` (the source the engine
writes) in a gcc stage on the same base, and places it in `/root/.cosmo` and
in the engine's private home `/usr/lib/ollama/engines/payload/.cosmo`.
Verified on solidPC: discovery lists the 3090 through the engine and a load
runs 100% on GPU. A bare-metal Linux host without a C compiler has the same
problem; that is open with opencoti.

## The listen address

xollama binds `XOLLAMA_HOST` only and never reads `OLLAMA_HOST`
(`.claude/rules/default-port.md`). The image therefore sets
`XOLLAMA_HOST=0.0.0.0:22434` and exposes **22434**:

```shell
docker run -d --gpus all -p 22434:22434 -v xollama:/root/.ollama ghcr.io/mann1x/xollama:dev
```

<Warning>
  Upstream's `Dockerfile`, still in the tree and still compiling everything,
  set `OLLAMA_HOST=0.0.0.0:11434` and `EXPOSE 11434`. An image built from it
  listened on the container's loopback at 22434 and could not be reached from
  outside. It now sets `XOLLAMA_HOST=0.0.0.0:22434` and `EXPOSE 22434` too
  (`docker-release` hook), but the published image still comes from
  `Dockerfile.xollama`, not from it. The CI smoke test fails a build whose
  server does not answer `/api/xollama` through a published port.
</Warning>

## Two channels

| run | environment | tags published |
|---|---|---|
| on a branch (`dev`) | `dev` | `:<upstream>-dev.<sha>`, `-<arch>`, `:dev` |
| on a tag whose GitHub release is a **pre-release** | `dev` | `:<version>`, `-<arch>`, `:dev` |
| on a tag whose GitHub release is a **full release** | `release` | `:<version>`, `-<arch>`, `:latest` |

`:<version>` and the moving tag are a manifest list over the
`:<version>-<arch>` images the run built (the `manifest` job).

The channel is the **GitHub pre-release flag**, as it is for the desktop
updater. It is not the hyphen in the tag: every xOllama tag has one
(`v<upstream>-rc.<k>.xollama`, `v<upstream>-xollama`, `v<upstream>-xollama.<n>`;
see `docs/protocols/RELEASE.md`), and a release candidate is always a
pre-release, so it only ever moves `:dev`; so under the old hyphen rule every release would have landed on `:dev`.
`:latest` never moves from a branch. The `channel: release` input is refused
unless the run is on a tag. A tag with no release, or with a draft, counts as a
pre-release.

A tag that `xollama-release.yaml` creates with `GITHUB_TOKEN` raises no push
event, so its `publish` job starts this workflow on the tag itself: every
candidate and release gets its image, on `:dev` because it is a pre-release
at that moment. Promotion rebuilds nothing, so `:latest` moves with one more
run on the tag, made after promotion:

```shell
gh workflow run docker-release.yaml --ref v0.34.2-xollama.2
```

And a `:dev` image from the tip of `dev`:

```shell
gh workflow run docker-release.yaml --ref dev -f push=true -f channel=dev
```

The run uses the workflow file **at the ref**, so `dev`'s version of this
workflow builds `dev`. `workflow_dispatch` only needs the file to exist on the
default branch, and it does.

## Architectures

**amd64 and arm64.** Each architecture has its own runtime pin
(`llama/runtime-pin-linux.txt`, `llama/runtime-pin-linux-arm64.txt`), is
assembled by `ARCH=<arch> scripts/docker-assemble.sh` and built on a runner of
its own kind (`ubuntu-latest`, `ubuntu-24.04-arm`), and is pushed as
`:<version>-<arch>`. The `plan` job builds arm64 only while the arm64 pin
carries the fork's runtime rows; an incomplete pin gives an amd64-only image
and an assembly that refuses arm64, naming the missing rows. The Go binary is
built with clang on arm64, as upstream does: AlmaLinux 8's gcc lacks a header
the MLX bindings include.

The arm64 payload: the fork's arm64 CPU runtime, upstream's arm64 CUDA 12,
CUDA 13 and JetPack 5/6 tarballs, and the engine's `aarch64` rows of
`llm/engine/pin/` (the engine and its three media libraries; no GPU
library, so on arm64 the engine serves the CPU and llama.cpp serves CUDA).
The engine's arm64 audio library needed the system's `libatomic` up to
snapshot `2610031615001`; from `2610040710001` it does not, and the image no
longer installs it (run on the Pi with the package removed).

Measured on a Raspberry Pi 5 (8 GB, Debian 13, 16K pages), 2026-10-04, in the
published `:dev` image: `qwen2.5:1.5b`, 512 tokens, the two engines
interleaved over four rounds, 9.8-10.0 tok/s on the engine and 10.9-11.0 on
llama.cpp; Kokoro, Supertonic and
KittenTTS each transcribed back by Whisper. The CUDA and JetPack payloads
have not been run on arm64 hardware. `ALLOW_UPSTREAM_RUNTIME=1` (local only,
refused in CI) assembles with upstream's CPU runtime when a pin has no runtime
rows.

ROCm is deliberately absent: the pinned engine declares no ROCm acceleration,
and an image advertising it would be untested.

## Why this is a separate workflow

Upstream's `release.yaml` carries a complete Docker pipeline. It cannot run
here. It compiles every native piece on upstream's own self-hosted runners, and
it is cache-hit-only against `ollama/release:cache-*`, a registry only upstream
can write to. Changing that inside a 37 000-line upstream workflow would conflict
on every `git merge upstream/main`; a separate file never does.

The previous version of this workflow compiled everything with upstream's
`Dockerfile`, on a self-hosted bs2 runner. That runner was never registered, and
bs2 is opencoti's build and gate host. Assembly makes the runner unnecessary.

## macOS is not built here

`release.yaml`'s `darwin-build` job is guarded off on the fork. It builds and
signs the macOS app, which needs a Metal toolchain and an Apple signing identity
the fork has neither of, on a per-minute `macos-26-xlarge` runner — it would
fail on the first release rather than produce anything. It is guarded rather
than deleted so it keeps merging from upstream, and it was removed from the
`release` job's `needs` so a skipped job cannot skip the release itself. Nothing
in that job's `required=` artifact list is a darwin artifact, so verification is
unchanged.

## One-time setup

1. **Runner.** None: the workflow runs on `ubuntu-latest`. The `make room` step
   removes toolchains the job never uses (dotnet, Android, GHC, CodeQL). The
   payload unpacks to about 5.5 GB and the image is about as large again, which
   does not fit the runner's default free space.

2. **Environments.** `release` and `dev` exist on the repository. Add required
   reviewers or environment secrets to `release` if publishing should need an
   approval step.

3. **Secrets.** `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` (a Docker Hub access
   token with write permission) as repository secrets. GHCR needs no secret — the
   workflow authenticates with the job's `GITHUB_TOKEN` and `packages: write`.

4. **First publish.** Neither image exists until the first push creates it.

   `docker.io/mannixita/xollama` is created by the push itself. Docker Hub
   applies the account's *default repository privacy* setting, so check it
   afterwards under the repository's **Settings → Visibility**.

   **Done for `ghcr.io/mann1x/xollama` (2026-09-26).** The first push (run
   36221348282) created the package public, and an anonymous pull token lists
   its tags. GitHub's documentation says a package created by `GITHUB_TOKEN`
   can start **private**, and a package has to exist before its visibility can
   change. If a new package ever comes up private, publish once, then make it
   public and link it to the repository:

   **In the UI** — the package page
   (`https://github.com/users/mann1x/packages/container/xollama/settings`) →
   *Danger Zone* → **Change visibility** → Public. On the same page,
   *Manage Actions access* → add the `xollama` repository with **Write**, which
   is what lets later runs push to an existing package.

   **Or with the API**, which is scriptable but needs a PAT with
   `write:packages` (the workflow's `GITHUB_TOKEN` cannot change visibility):

   ```shell
   gh api -X PATCH user/packages/container/xollama \
     -f visibility=public
   ```

   Verify anonymously — this must succeed with no credentials at all:

   ```shell
   docker logout ghcr.io
   docker pull ghcr.io/mann1x/xollama:dev
   ```

   Or without docker:

   ```shell
   tok=$(curl -s "https://ghcr.io/token?scope=repository:mann1x/xollama:pull" | jq -r .token)
   curl -s -H "Authorization: Bearer $tok" https://ghcr.io/v2/mann1x/xollama/tags/list
   ```

   It is a one-time step. Once the package is public it stays public across
   every later push.

## Testing it without publishing

Run the workflow manually with `push: false`. It assembles, builds and
smoke-tests the image and pushes nothing, so it exercises everything except the
registry writes.

Locally, with the downloads kept off tmpfs:

```shell
VERSION=0.34.2-dev.local GO_TOOLCHAIN=go1.26.8 \
  ASSET_DIR=/path/on/disk/docker-assets \
  scripts/docker-assemble.sh /path/on/disk/docker-ctx
docker build -t xollama:local /path/on/disk/docker-ctx
docker run -d -p 127.0.0.1:22434:22434 xollama:local
curl -s http://127.0.0.1:22434/api/xollama
```
