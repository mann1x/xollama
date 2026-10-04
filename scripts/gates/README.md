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
| `engine-ab.sh` | G3 | solidPC, as `ollama` |
| `chat-paths.sh`, `chat-paths.py` | G4 | solidPC |
| `council.sh` (runs `scripts/council-gate.py`) | G5 | solidPC |
| `serve-side.sh`, `speech-linux.sh`, `video-linux.sh` | G6 | solidPC |
| `vulkan-linux.sh`, `bench.sh` | G7 | solidPC |
| `windows-side.ps1`, `windows-speech.ps1` | G8 | eleven2go, sent on stdin |
| `windows-install-pre.ps1`, `windows-install.ps1`, `windows-install-check.ps1` (stdin), `windows-install-measure.ps1` (`-File`) | G9 | eleven2go |
| `pi-image.sh` (engine replaced in `:dev`), `image-pi.sh` (a published image) | G10 | dietpi5 |
| `image-x86.sh` | G10 | solidPC |
| `bench.sh`, `speech-mac.sh` | G11 | Mac mini |
