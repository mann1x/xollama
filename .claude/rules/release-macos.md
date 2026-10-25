---
paths:
  - .github/workflows/xollama-release.yaml
  - scripts/build_darwin.sh
  - scripts/gates/mac-ci.sh
  - llama/runtime-pin-darwin.txt
  - docs/protocols/RELEASE.md
---

# The macOS job of the release workflow

- `.github/workflows/xollama-release.yaml` has a `macos` job (`macos-26`,
  hosted) that runs `sh scripts/build_darwin.sh build sign app` as the Mac mini
  does, and attaches `xOllama.dmg` and `xOllama-darwin.zip` to the draft.
  `publish` needs it, and its `want` asset list and `sha256sum.txt` include
  both files.
- **macOS builds only the Go binaries and ships no stock llama.cpp**
  (`macos-stockless` hook): every GGUF load runs on opencoti through Metal
  (`llm/engine/stockless.go`), so `scripts/build_darwin.sh` builds `ollama-go`,
  refuses `-a amd64`, and the job fails a bundle carrying `llama-server` or
  `llama-quantize`. The opencoti engine comes from `llm/engine/pin/`.
- **MLX is never compiled** (`macos-mlx-pin` hook, Registry row in
  `docs/protocols/UPSTREAM-SYNC.md`). It is upstream's build, republished by
  the fork and pinned in `llama/runtime-pin-darwin.txt` (`repo`, `tag`,
  `asset`, `sha256`, `upstream`, `mlx`, `mlx_c`). `_mlx_pinned` and
  `_take_mlx_runtime` in `scripts/build_darwin.sh` fetch it, check the sha256
  and unpack `mlx_metal_v3` and `mlx_metal_v4` into
  `dist/darwin-arm64/lib/ollama`. Apple silicon only; `XOLLAMA_MLX=compile` is
  upstream's path unchanged.
- **The pin's `mlx` and `mlx_c` must equal `MLX_VERSION` and `MLX_C_VERSION`,**
  and its `upstream` the release's base: the build refuses otherwise, and the
  workflow's `plan` refuses it first, along with an asset the fork's release
  does not publish. The pin moves with the two Linux runtime pins on every
  fork sync (`docs/protocols/FORK-SYNC.md`).
- **Six repository secrets, each refused by name when empty:**
  `MACOS_SIGNING_KEY`, `MACOS_SIGNING_KEY_PASSWORD`, `APPLE_IDENTITY`,
  `APPLE_NOTARY_KEY`, `APPLE_NOTARY_KEY_ID`, `APPLE_NOTARY_ISSUER`. The `.p12`
  goes into a keychain of the run's own and is removed once imported; the
  notary key is written to `$RUNNER_TEMP/AuthKey.p8` under `umask 077`; the
  last step (`if: always()`) deletes the keychain and both files.
- The DMG is built headless (`XOLLAMA_DMG_HEADLESS: "1"`, `VOL_NAME: xOllama`):
  Finder styling needs a logged-in desktop.
- **Both files are checked, not only the one built last.** Gatekeeper
  (`spctl`, `source=Notarized Developer ID`) and `xcrun stapler validate` on
  the app, the DMG and the app inside `xOllama-darwin.zip`, which is the only
  file the app's updater takes. Then the version, the Go toolchain of both
  executables (the plan's `gotoolchain`), `arm64`, the fork's releases endpoint
  in `Contents/MacOS/xOllama` (without it the app asks ollama.com and replaces
  the install with a stock ollama), and the
  `allow-unsigned-executable-memory` entitlement on
  `Resources/engines/ape-macos-aarch64` (without it the hardened runtime kills
  the engine loader at start).
- No third-party actions, as in the rest of the workflow: `git` and `gh` only.
- **`docs/protocols/RELEASE.md` says macOS is built by the workflow** (since
  `v0.35.1-xollama.5`); the Mac mini checks what the workflow built (the
  draft's two files against `sha256sum.txt`, put in `~/dev/xollama/ci-app`,
  then G11 with `scripts/gates/mac-ci.sh`). Releases up to `.4`
  were built there and attached by hand, which remains the fallback.
