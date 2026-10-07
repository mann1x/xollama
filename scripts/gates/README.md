# scripts/gates

The scripts the release gates were last run with, kept so a gate can be run
again the same way. What each gate proves, where it runs and what a pass looks
like is in `docs/protocols/VALIDATION.md`.

They are records, not tools: each names the host's own paths (solidPC's model
stores, `/srv/ml/xb140`, the engine file of that day). Set the engine path and
the store before reusing one, and keep the working directory on a persistent
disk, never `/tmp`.

| Script | Gate | Host |
|---|---|---|
| `stage-engine.sh <platform> [dir]`: the engine THIS TREE pins (`llm/engine/pin`), staged and sha256-checked by `cmake/opencoti-fetch.cmake`; prints its path | used by `serve-side.sh` and for `windows-speech.ps1` | solidPC |
| `engine-ab.sh` | G3 | solidPC, as `ollama` |
| `chat-paths.sh`, `chat-paths.py` | G4 | solidPC |
| `decision.sh`: `/v1/systemone` with `nimble` and `clef-flash` | G4 | solidPC |
| `council.sh` (runs `scripts/council-gate.py`) | G5 | solidPC |
| `serve-side.sh`, `speech-linux.sh` (`MEDIA=` a writable scratch store; the engine from `stage-engine.sh`), `video-linux.sh` | G6 | solidPC |
| `vulkan-linux.sh`, `bench.sh` | G7 | solidPC |
| `windows-side.ps1` (stdin); `windows-speech.ps1` (`-File`, `-EngineDir` a directory staged by `stage-engine.sh win-x86_64`) | G8 | eleven2go |
| `windows-gate-run.ps1 -Script <gate.ps1> -Out <file> [-ScriptArgs '…']`: runs a gate in the logged-in session (an ssh session cannot start GPU engines), through `schtasks /IT` and a `.cmd` wrapper, and waits for the gate's own exit line; call it with `powershell -Command "& …"` | G8, bug checks | eleven2go |
| `windows-twogpu-engine.ps1 -EngineDir <dir> -Blob <gguf>`: one model over the RTX 3090 and the RX 9070 XT, both on Vulkan, `-ts 1,1`; a fresh engine against one that served another request first, per-token logprobs (bug-3954). Run it on an older engine too: the control must differ | engine pin move | eleven2go |
| `windows-install-pre.ps1`, `windows-install.ps1`, `windows-install-check.ps1` (stdin), `windows-install-measure.ps1` (`-File`) | G9 | eleven2go |
| `windows-update.ps1` (`-File`, `-Exe <path>`): the small installer over an install | G9 | eleven2go |
| `pi-image.sh` (engine replaced in `:dev`), `image-pi.sh` (a published image) | G10 | dietpi5 |
| `image-x86.sh` | G10 | solidPC |
| `bench.sh`, `speech-mac.sh` | G11 | Mac mini |
| `mac-ci.sh` | G11 on the app the release workflow built (the draft's zip and DMG) | Mac mini |
