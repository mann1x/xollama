# Docker image

**Status:** PARKED, waiting for opencoti b111 on HF · **Index:** [MASTER_PLAN](MASTER_PLAN.md)

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

## Progress

- [x] Design agreed (2026-09-25)
- [ ] b111 on HF and measured
- [ ] Runtime pin + assembly workflow
- [ ] First `:dev` image, user testing
