# scripts/gates

The scripts the release gates were last run with, kept so a gate can be run
again the same way. What each gate proves, where it runs and what a pass looks
like is in `docs/protocols/VALIDATION.md`.

The Linux gates are tools: each takes the engine from `stage-engine.sh`, that
is from `llm/engine/pin` of the tree it is run from, the binary from `X` (a
`bin/xollama` inside a payload assembled by `scripts/docker-assemble.sh`; a
bare binary finds no `llama-server`) and its store from the environment. None
names an engine build, a run directory or a shared store, and a store is
always a scratch one (the server writes `manifests-v2` into what it serves).
Run them from the repository, never from a copy: until 2026-10-10 several were
records with that day's paths, runs were made from copies, and a copy kept a
check the repository had already corrected (`clef-flash` lists `decision` and
`vision`; the copy wanted `decision` alone and printed FAIL). `linux-all.sh`
runs G3 to G6 in order. Keep the working directory (`W`) on a persistent disk,
never `/tmp`. The Windows and macOS scripts are still records of their hosts'
paths.

| Script | Gate | Host |
|---|---|---|
| `stage-engine.sh <platform> [dir]`: the engine THIS TREE pins (`llm/engine/pin`), staged and sha256-checked by `cmake/opencoti-fetch.cmake`; prints its path | used by `serve-side.sh` and for `windows-speech.ps1` | solidPC |
| `linux-all.sh` (`ROOTFS=` an assembled payload, `STORE=`, `CSTORE=`, `MSTORE=` scratch stores): builds the tree and runs G3, G4, G6 and G5 in order | G3 to G6 | solidPC, as root |
| `engine-ab.sh` (`STORE=`) | G3 | solidPC, as `ollama` |
| `chat-paths.sh` (`STORE=`), `chat-paths.py` | G4 | solidPC |
| `decision.sh` (`STORE=`): `/v1/systemone` with `nimble` and `clef-flash`; its requests are `decision/req*.json` | G4 | solidPC |
| `council.sh <tag>...` (`STORE=`; runs `scripts/council-gate.py`) | G5 | solidPC |
| `serve-side.sh`, `side.sh` (sourced: `up <store> <log>`, `down`), `speech-linux.sh`, `video-linux.sh` (`MEDIA=`) | G6 | solidPC |
| `vulkan-linux.sh` (`IMAGE=` the image under test, `NODE=` the render node), `bench.sh`: the integrated GPU through the image's Mesa drivers | G7 | solidPC, Docker |
| `windows-side.ps1` (stdin); `windows-speech.ps1` (`-File`, `-EngineDir` a directory staged by `stage-engine.sh win-x86_64`) | G8 | eleven2go |
| `windows-gate-run.ps1 -Script <gate.ps1> -Out <file> [-ScriptArgs '…']`: runs a gate in the logged-in session (an ssh session cannot start GPU engines), through `schtasks /IT` and a `.cmd` wrapper, and waits for the gate's own exit line; call it with `powershell -Command "& …"` | G8, bug checks | eleven2go |
| `windows-twogpu-engine.ps1 -EngineDir <dir> -Blob <gguf>`: one model over the RTX 3090 and the RX 9070 XT, both on Vulkan, `-ts 1,1`; a fresh engine against one that served another request first, per-token logprobs (bug-3954). Run it on an older engine too: the control must differ | engine pin move | eleven2go |
| `windows-install-pre.ps1`, `windows-install.ps1`, `windows-install-check.ps1` (stdin), `windows-install-measure.ps1` (`-File`) | G9 | eleven2go |
| `windows-update.ps1` (`-File`, `-Exe <path>`): the small installer over an install | G9 | eleven2go |
| `pi-image.sh` (`TAG=`, `BASE=`: the tree's engine, staged by `stage-engine.sh aarch64` and copied to `ctx-<TAG>/engine`, replaced in a published image), `image-pi.sh` (a published image) | G10 | dietpi5 |
| `image-x86.sh` | G10 | solidPC |
| `mac-engine.sh` (`D=` a directory with `src/`, this tree, and `stage/`, from `stage-engine.sh macos-aarch64`): builds the server there and runs it in a copy of the installed app's Resources; `bench.sh`, `speech-mac.sh` | G11 before an app exists for the tree | Mac mini |
| `mac-ci.sh` | G11 on the app the release workflow built (the draft's zip and DMG) | Mac mini |
