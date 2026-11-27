---
paths:
  - .github/workflows/xollama-release.yaml
  - scripts/linux-tarballs.sh
  - scripts/docker-assemble.sh
  - scripts/install.sh
  - llama/runtime-pin-linux.txt
  - llama/runtime-pin-linux-arm64.txt
  - docs/protocols/RELEASE.md
---

# The Linux install tarballs of the release workflow

- **The image and the tarballs are one payload.** The `linux` job of
  `.github/workflows/xollama-release.yaml` builds `xollama-linux-<arch>` in
  AlmaLinux 8 (glibc 2.28), runs `scripts/docker-assemble.sh` around it with
  `LIST_DIR=<out>/lists`, and `scripts/linux-tarballs.sh` splits that rootfs.
  Never stage a tarball's files a second way.
- Archives, each under GitHub's 2 GiB asset cap, unpacked by
  `scripts/install.sh` under its prefix (`/usr/local`):
  - `xollama-linux-<arch>.tar.zst`: `bin/xollama`, the CPU runtime and
    upstream's base GPU libraries (CUDA 12 and 13, Vulkan);
  - `xollama-linux-<arch>-opencoti.tar.zst`: the engine, every file the pin
    stages beside it, and its prebuilt dlopen helper
    (`lib/ollama/engines/payload/.cosmo`);
  - one per further `gpu` row of the runtime pin: `xollama-linux-amd64-mlx`,
    `xollama-linux-arm64-jetpack6` (upstream dropped JetPack 5 in v0.40.18,
    #19865, and its row went with it).
- **Part of every release** (owner, 2026-11-21): `publish` refuses a release
  without all six tarballs. The pins are `llama/runtime-pin-linux.txt`
  (amd64) and `llama/runtime-pin-linux-arm64.txt` (arm64).
- The dlopen helper is compiled in AlmaLinux 8, the Linux binary's floor, so
  `scripts/linux-tarballs.sh` needs docker and zstd.
- Check a cut by running `scripts/install.sh` against the downloaded assets
  (`XOLLAMA_DOWNLOAD_BASE`) in a clean container with no compiler, then a GPU
  load on opencoti (`docs/protocols/RELEASE.md`).

```sh
ARCH=amd64 scripts/linux-tarballs.sh <assemble-out-dir> <dist-dir>
```
