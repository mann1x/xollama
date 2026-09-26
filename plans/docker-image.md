# Docker image

**Status:** ACTIVE — first `:dev` image published (run 36221348282) and GHCR public (2026-09-26); user testing open · **Index:** [MASTER_PLAN](MASTER_PLAN.md)

The owner wants a container image so users can test xollama and so the image
path gets exercised. Publishing mechanics (registries, channels, environments)
are in `docs/features/docker-release.md` and
`.github/workflows/docker-release.yaml`. This plan covers **how the image is
built**.

## The design (agreed 2026-09-25)

Nothing native is compiled here, because every native piece already exists
as a built artifact. The image is assembled on **GitHub-hosted runners**. The
self-hosted bs2 runner is not needed, and it was never registered.

| Layer | Source | Pinned by |
|---|---|---|
| llama.cpp CPU runtime | the fork's release `v0.34.2-2-thinkbudget`, `ollama-linux-amd64-runtime.tgz`, sha256 `33ac42c1107c68604ed9c2e2c991e9a7cdfd00b33c2a4f2c2ce758273df36447` (built at `d6e24119`) | sha256 + a check of the inputs digest |
| GPU backends | upstream v0.34.2 release tarballs: `cuda_v12`, `cuda_v13`, `vulkan`, `mlx_cuda_v13` | upstream tag + sha256 |
| opencoti engine | HF dev repo | `llm/engine/pin.txt` |
| xollama binary | a Go-only build stage on the planned toolchain (`xollama-release.yaml` `plan` job) | the commit |

- **amd64 first.** arm64 follows once the amd64 image has been through user
  testing.
- **Runtime pin decision:** reuse the fork's EXISTING tgz above, with no
  rebuild. The inputs-digest comparison excludes `llama/compat/README.md` on
  both sides, because the README is becoming static and is not a build input.
- The channel follows the GitHub pre-release flag, not the hyphen in the tag
  (see the Note in `docs/features/docker-release.md`).

## Unpark when

opencoti publishes b111 to HF and it is measured. Then:

1. Move the engine pin to b111. This goes through `llm/engine/pin.txt` and a
   measured `scripts/phase2-engine-ab.py` run as the `ollama` user.
2. Pin the runtime tgz above.
3. Trigger the workflow as a pre-release (`:dev`) for user testing.

## Results (2026-09-26)

- **The inputs digest matches.** The digest leaves out `llama/compat/README.md`
  on both sides. The fork at `d6e24119` and `dev` both come to
  `eff6800e1f27383e5f9b880f109fb0e70aad0108f8a52716201d9f77c1fc54c6`, and
  the README is their only difference.
  `scripts/docker-assemble.sh` checks both: this commit against the pin, and
  the pinned `built` commit against the pin whenever it is reachable.
- **Upstream sha256s:**
  - `ollama-linux-amd64.tar.zst` is `e155b835…`. It carries `cuda_v12`,
    `cuda_v13`, `vulkan` and a CPU set.
  - `ollama-linux-amd64-mlx.tar.zst` is `aafb2f2d…`, carrying `mlx_cuda_v13`.
  - Both match GitHub's published digests.
  - Upstream `LLAMA_CPP_VERSION` is `b10969`, the same as ours.
- **The overlay is clean.** The fork runtime replaces every CPU file upstream
  ships. Upstream contributes only its GPU directories, `libgomp` and licence
  files.
- **Local validation on solidPC:**
  - the full assembly ran at the b65 engine pin;
  - `docker build` produced a 5.43 GB image;
  - the container served on `0.0.0.0:22434`: `/api/version` and
    `/api/xollama` answered `0.34.2-dev.a4886db9` through a published port,
    and `xollama list` worked inside it.
  - GPU inside the container was not exercised.
- **Found on the way:**
  - Upstream's `Dockerfile` sets `OLLAMA_HOST`, which xollama ignores, so an
    image built from it is unreachable. `Dockerfile.xollama` sets
    `XOLLAMA_HOST`.
  - The old hyphen rule would have sent every `-xollama.<n>` release to
    `:dev`. The channel is now the GitHub pre-release flag.

## Progress

- [x] Design agreed (2026-09-25)
- [x] b111 on HF and measured; engine pin moved to it (2026-09-26)
- [x] Runtime pin + assembly workflow (2026-09-26: `llama/runtime-pin-linux.txt`, `scripts/docker-assemble.sh`, `Dockerfile.xollama`, `docker-release.yaml` on `ubuntu-latest`)
- [ ] First `:dev` image, user testing (image published 2026-09-26, run 36221348282; `ghcr.io/mann1x/xollama` public, verified anonymously; testing open)
