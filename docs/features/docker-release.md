# Publishing the container image

`.github/workflows/docker-release.yaml` builds and publishes the xollama
container image on every `v*` tag, to two registries:

| registry | image |
|---|---|
| Docker Hub | `docker.io/mannixita/xollama` |
| GHCR | `ghcr.io/mann1x/xollama` |

Each release publishes `:<version>` and `:latest` as a multi-arch manifest over
`linux/amd64` and `linux/arm64`, plus the per-architecture `:<version>-amd64`
and `:<version>-arm64` tags the manifest is assembled from. Those stay published
deliberately — they are what makes a broken architecture diagnosable afterwards.

## Why this is a separate workflow

Upstream's `release.yaml` already carries a complete Docker pipeline. It cannot
run here: seven of its jobs target upstream's own self-hosted runners (`linux`,
`linux-arm64`, `windows`, `macos-26-xlarge`), nine reference an
`environment: release` this repository does not define, and its Docker jobs are
documented as cache-hit-only against `ollama/release:cache-*`, a registry only
upstream can push to. Changing those inside a 37 000-line upstream workflow
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

2. **Secrets.** `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` (a Docker Hub access
   token with write permission) as repository secrets. GHCR needs no secret — the
   workflow authenticates with the job's `GITHUB_TOKEN` and `packages: write`.

3. **First publish.** `docker.io/mannixita/xollama` does not exist until the
   first push creates it; check its visibility afterwards, and note that a GHCR
   package created by `GITHUB_TOKEN` starts **private** and linked to the
   repository — make it public from the package settings if the image is meant
   to be pullable anonymously.

## Testing it without cutting a release

Run the workflow manually with `push: false`. It builds both architectures and
pushes nothing, which exercises everything except the registry writes. Do not do
this while bs2 is building — the preflight will refuse, which is the point.
