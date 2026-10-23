---
paths:
  - app/xollama.iss
  - app/xollama-setup-pages.iss
  - app/xollama-mlx.iss
  - llama/runtime-pin-windows-mlx.txt
  - app/xollama-stop.ps1
  - app/updater/installer_script_test.go
  - app/updater/fork_payload_windows.go
  - scripts/gates/windows-update.ps1
  - docs/features/windows-installer.md
---

# The Windows installer (`app/xollama.iss`)

- **Never expand `{app}` in `InitializeSetup` or `InitializeWizard`.** Inno
  Setup creates it only once the wizard has an install directory. The
  update-only installer of `v0.35.1-xollama` asked there and ended every run
  with a runtime error (2026-10-05); the release was not promoted. Guard:
  `TestTheInstallerNeverAsksForAppBeforeItExists` in
  `app/updater/installer_script_test.go`, which reads `app/xollama.iss`,
  `app/xollama-setup-pages.iss` and `app/xollama-mlx.iss`.
- **The update-only installer (`CORE`) checks its payload in
  `PrepareToInstall`**: `PayloadRefusal` returns the message, or `''`, from
  `{app}\lib\ollama\PAYLOAD_ID` against `PKG_PAYLOAD_ID`. It is asked before
  `StopXollama`, so a machine the update is not for keeps its running xOllama.
  Guard: `TestTheUpdateInstallerStillChecksThePayload`.
- **The small installer is run by a gate, not only the full one.** G9 in
  `docs/protocols/VALIDATION.md` runs the same release's `xOllamaUpdate.exe`
  over the full install with `scripts/gates/windows-update.ps1` (copied to
  eleven2go, run with `-File`, `-Exe <path>`): exit 0, the version unchanged, a
  new server on 22434. An update built for another payload exits 7 and leaves
  the running server alone. The two candidates before `v0.35.1-xollama` had
  only ever been installed with `xOllamaSetup.exe`.
- **MLX is downloaded, never carried** (owner, 2026-10-07): installer plus
  archive would pass GitHub's 2 GiB asset cap. `app/xollama-mlx.iss`
  (`windows-mlx` hook, included by `app/xollama.iss`) fetches the archive
  `llama/runtime-pin-windows-mlx.txt` pins (`PKG_MLX_URL`, `PKG_MLX_SHA256`
  from the release workflow's `plan`); `DownloadTemporaryFile` refuses other
  bytes. `InstallMLX` runs from `CurStepChanged` in both installers;
  `KeepMLX` comes after `StopXollama` in `PrepareToInstall` (full only).
  `/MLX=auto` (NVIDIA only), `always`, `skip`; `MLX_ID` skips a match. A
  failure is said, and the install finishes without MLX. Guard:
  `TestBothInstallersFetchThePinnedMLX`.
- **The pinned archive is the fork's `ollama-windows-amd64-mlx-reldir.zip`,
  never upstream's zip**: the same MLX with `MLX_CUDA_BIN_DIR` and
  `MLX_CUDNN_BIN_DIR` set to `.`. Upstream's `mlx.dll` looks for cuDNN in
  `C:/Program Files/NVIDIA/CUDNN/bin/x64` and, without it, panics on the first
  cuDNN op and hangs the request (eleven2go, 2026-10-08). The asset comes from
  the fork (`docs/protocols/FORK-SYNC.md`).
- Prose: `docs/features/windows-installer.md`; the release steps are in
  `docs/protocols/RELEASE.md`.
