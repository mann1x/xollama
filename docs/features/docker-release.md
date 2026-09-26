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
| llama.cpp CPU runtime (`llama-server`, CPU `ggml`, built with `llama/compat`) | the fork's release `v0.34.2-2-thinkbudget`, `ollama-linux-amd64-runtime.tgz` | `llama/runtime-pin-linux.txt`: sha256, plus the inputs digest |
| CUDA v12, CUDA v13, Vulkan, MLX CUDA v13 | upstream `v0.34.2`: `ollama-linux-amd64.tar.zst`, `ollama-linux-amd64-mlx.tar.zst` | `llama/runtime-pin-linux.txt`: sha256; upstream's `LLAMA_CPP_VERSION` must equal ours |
| opencoti engine | HF, whatever `llm/engine/pin.txt` names | `llm/engine/pin.txt` via `cmake/opencoti-fetch.cmake` |
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

## The listen address

xollama binds `XOLLAMA_HOST` only and never reads `OLLAMA_HOST`
(`.claude/rules/default-port.md`). The image therefore sets
`XOLLAMA_HOST=0.0.0.0:22434` and exposes **22434**:

```shell
docker run -d --gpus all -p 22434:22434 -v xollama:/root/.ollama ghcr.io/mann1x/xollama:dev
```

<Warning>
  Upstream's `Dockerfile`, which is still in the tree and still compiles
  everything, sets `OLLAMA_HOST=0.0.0.0:11434` and `EXPOSE 11434`. An image
  built from it listens on the container's loopback at 22434 and cannot be
  reached from outside. That is why the published image comes from
  `Dockerfile.xollama` and not from it. The CI smoke test fails a build whose
  server does not answer `/api/xollama` through a published port.
</Warning>

## Two channels

| run | environment | tags published |
|---|---|---|
| on a branch (`dev`) | `dev` | `:<upstream>-dev.<sha>`, `-amd64`, `:dev` |
| on a tag whose GitHub release is a **pre-release** | `dev` | `:<version>`, `-amd64`, `:dev` |
| on a tag whose GitHub release is a **full release** | `release` | `:<version>`, `-amd64`, `:latest` |

The channel is the **GitHub pre-release flag**, as it is for the desktop
updater. It is not the hyphen in the tag: every `v<upstream>-xollama.<n>` tag
has one, so under the old hyphen rule every release would have landed on `:dev`.
`:latest` never moves from a branch. The `channel: release` input is refused
unless the run is on a tag. A tag with no release, or with a draft, counts as a
pre-release.

A release that `xollama-release.yaml` creates with `GITHUB_TOKEN` triggers no
other workflow, so a release's image is a manual run on its tag, made after
promotion:

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

**amd64 only**, for now. arm64 follows once the amd64 image has been through
user testing. The per-architecture `:<version>-amd64` tag is published already,
so a later multi-arch manifest can be assembled from it without rebuilding.

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
