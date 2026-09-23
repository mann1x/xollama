# Publishing the container image

`.github/workflows/docker-release.yaml` builds and publishes the xollama
container image on every `v*` tag, to two registries:

| registry | image |
|---|---|
| Docker Hub | `docker.io/mannixita/xollama` |
| GHCR | `ghcr.io/mann1x/xollama` |

## Two channels

The repository has two GitHub environments, and the tag decides which one a
build uses:

| tag | environment | tags published |
|---|---|---|
| `v1.2.3` | `release` | `:1.2.3` and `:latest` |
| `v1.2.3-rc1`, `v1.2.3-dev.4` | `dev` | `:1.2.3-rc1` and `:dev` |

Anything after the first hyphen makes it a pre-release, as semver defines it. A
pre-release moves `:dev` and **never** `:latest`, so `docker pull <image>` can
never hand someone a dev build. A manual run can override the decision with the
`channel` input.

The environments are where release-only protection belongs — required
reviewers, or a secret that only the release channel may use — without having to
duplicate the workflow.

Each release publishes `:<version>` and its moving tag as a multi-arch manifest
over `linux/amd64` and `linux/arm64`, plus the per-architecture `:<version>-amd64`
and `:<version>-arm64` tags the manifest is assembled from. Those stay published
deliberately — they are what makes a broken architecture diagnosable afterwards.

## Why this is a separate workflow

Upstream's `release.yaml` already carries a complete Docker pipeline. It cannot
run here: seven of its jobs target upstream's own self-hosted runners (`linux`,
`linux-arm64`, `windows`, `macos-26-xlarge`) that this repository does not have,
and its Docker jobs are documented as cache-hit-only against
`ollama/release:cache-*`, a registry only upstream can push to. Changing those inside a 37 000-line upstream workflow
would conflict on every `git merge upstream/main`; a separate file never does.

What is inherited is the *shape* — build each architecture separately, then
merge — because one multi-platform build serialises the slow architecture behind
the fast one and gives no way to retry half of it.

## This does not build opencoti

The image does not compile the engine. The `opencoti-engine` stage in
`Dockerfile` downloads the artifact named by `llm/engine/pin.txt` through
`cmake/opencoti-fetch.cmake` and ships it inside the image, which is why xollama
never fetches an engine at run time. **New engine bytes reach the image through
a pin move, not a CI change.**

This is a different thing from the opencoti *build* images
(`opencoti/dso-build:cuda13.3-glibc2.28` and its siblings), which produce the
CUDA DSO, the Vulkan build and the Windows DLL, and run from opencoti's own
`BUILD_CYCLE.md` protocol. The glibc floor that forces those onto a Rocky-8 lane
needs no answer here either: upstream's `llama-server` stages already build on
`almalinux:8` (glibc 2.28).

## The build host is shared

Builds run on bs2, which is also opencoti's build and gate host. `BUILD_CYCLE.md`
records that a 32-thread job running beside a CPU-bound eval depresses that
eval's decode rate by 15–30% — it corrupts what the eval was measuring rather
than merely slowing it down.

CI fires on a tag push and has no idea what the box is doing, so the workflow
asks first: a `preflight` job refuses to start when an `nvcc`/`cicc`/`ptxas`
compile is running or the load average is above half the core count. Override
with the `ignore_busy_host` input on a manual run. Releases are serialised by a
`concurrency` group rather than cancelled — a half-pushed manifest is worse than
a queued one.

## Architectures

`amd64` is native on bs2. `arm64` is **emulated under QEMU**, because bs2 is
x86_64; it is published anyway because the fork targets DGX Spark and Raspberry
Pi, and opencoti publishes an aarch64 artifact. Give that leg a native arm64
runner and the cost disappears with no change to the workflow.

<Warning>
  **The arm64 image currently ships no CUDA payload.** The pinned *dev* engine
  declares `accel x86_64 CUDA` and carries `dso` rows for `x86_64` and
  `win-x86_64` only. The aarch64 `bin` row resolves — verified by fetching it —
  so the image builds and the portable engine binary is shipped, but with no
  `ggml-cuda-sbsa-aarch64.so` beside it, so opencoti runs CPU-only there and
  `pinUncoveredIn` routes acceleration to llama.cpp.

  The **release** channel does publish `dso/<ver>/ggml-cuda-sbsa-aarch64.so`.
  When a dev pin carries it, add the `dso aarch64` row to `llm/engine/pin.txt`
  and the image gains arm64 acceleration with no change here.
</Warning>

A failed arm64 leg degrades to an amd64-only manifest rather than failing the
release: an image that exists for one architecture beats no image.

The `-rocm` flavour upstream also publishes is deliberately absent until a ROCm
build is developed and validated for this fork — the pinned engine declares no
ROCm acceleration, so an image advertising it would be untested. Re-add it to
the `build` matrix and to `merge` once it is measured.

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

1. **Register the runner.** `scripts/setup-bs2-runner.sh`, run once on bs2 as
   root. It installs the `docker-buildx-plugin` (bs2 has Docker but not buildx),
   registers QEMU binfmt handlers for the emulated leg, creates a buildx builder
   and installs the Actions runner as a service labelled `xollama-build`. The
   registration token is minted at run time and never written to disk.

   Verify:

   ```shell
   gh api repos/mann1x/xollama/actions/runners \
     --jq '.runners[] | "\(.name) \(.status) \(.labels|map(.name)|join(","))"'
   ```

   The labels must include `self-hosted`, `linux`, `X64`, `xollama-build`.

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

   A GHCR package created by `GITHUB_TOKEN` starts **private**, and there is no
   way to pre-create it public — the package has to exist before it can be made
   public. So publish once, then make it public and link it to the repository:

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
   docker pull ghcr.io/mann1x/xollama:latest
   ```

   It is a one-time step. Once the package is public it stays public across
   every later push.

## Testing it without cutting a release

Run the workflow manually with `push: false`. It builds both architectures and
pushes nothing, which exercises everything except the registry writes. Do not do
this while bs2 is building — the preflight will refuse, which is the point.
