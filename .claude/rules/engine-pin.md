---
paths:
  - llm/engine/**
  - cmake/opencoti-fetch.cmake
  - cmake/opencoti-engine.cmake
---

# opencoti engine pin

- `llm/engine/pin.txt` is the only place that names the engine artifact, and it
  is parsed twice — by `ParsePin` in `llm/engine/pin.go` and by
  `cmake/opencoti-fetch.cmake`. A new directive must be readable by both;
  `TestPinFormatIsWhatCMakeParses` in `llm/engine/pin_test.go` holds them to it.
- `rev` is a 40-character commit sha, never a branch or tag: opencoti re-cuts a
  release in place, so a moving rev fetches bytes the pinned `sha256` rows
  reject. `ParsePin` rejects anything shorter or non-hex; on the dev repo it
  names the *snapshot* commit carrying the files, not the `pin ->` pointer
  commit a second later.
- `channel` is required and is `release` or `dev` (`ChannelRelease` /
  `ChannelDev` in `llm/engine/pin.go`). It is declared, never guessed from the
  repo name; `cmake/opencoti-fetch.cmake` names a dev channel in the build log.
- Never assert which Hugging Face repo is pinned — the dev channel points at a
  different repo whenever a cut is in flight. Tests check the `<owner>/<name>`
  shape only.
- Engine capabilities come from `feature` rows, read through `pin.HasFeature`
  (`featureSWACacheTypes` in `llm/engine/capability.go`, `featureLogMemoryPlan`
  in `logArgs` in `llm/engine/opencoti.go`: `--log-verbosity 4 --log-memory-plan`
  with the row, `--log-verbosity 5` without), and are never inferred
  from the cut number in `tag`: a dev build carries part of the next cut under
  the previous cut's tag.
- `accel <arch> <backend>` rows declare what the pinned BYTES accelerate, which
  is a different fact from the tested matrix in `llm/engine/policy.go`. Routing
  is the two intersected — `pinUncovered` / `pinUncoveredIn` call
  `pin.Accelerates`, so an uncovered accelerator goes to llama.cpp instead of
  being served silently on the CPU. Backend spellings must be in `knownBackends`
  (`llm/engine/policy.go`); a typo is a parse error.
- `cuda-sass <cc>...` (major*10+minor, e.g. `86 120`) lists the compute
  capabilities the CUDA payload carries SASS for. `Pin.CoversCUDA` matches same
  major at that minor or later; `deviceUnsupported` in `llm/engine/policy.go`
  refuses an uncovered device by name so it goes to llama.cpp rather than the
  CPU. No row means no narrowing. Guard: `llm/engine/pin_windows_sass_test.go`.
  It is written `#! cuda-sass 86 120`: opencoti's own pin parsers skip `#`
  lines and refuse a bare directive, so `ParsePin` reads a `#!` line only for
  a key in `machineKeys`; any other `#!` key stays a comment.
- The optional CUDA 12 payload is `#! dso-cuda12 <arch> <path> <sha256>` plus
  `#! cuda12-sass <cc>...` (`Pin.CUDA12DSO`, `Pin.CoversCUDA12`; unlike
  `cuda-sass`, no row covers nothing). One process loads one payload, so it is
  staged beside a second engine copy in `engines/cuda_v12` (`CUDA12Dirs`,
  `scripts/docker-assemble.sh`); `cudaPayload` in `llm/engine/policy.go` picks
  it per load in `Launch`, and a load spanning both payloads goes to llama.cpp.
  An explicit `XOLLAMA_ENGINE_PATH` is used as given. Guards:
  `llm/engine/pin_cuda12_test.go`,
  `TestAVoltaCardGoesToLlamaCPPWhenThePinHasNoCUDA12Payload`.
- Media sidecars are `#! sidecar <arch> <kind> <path> <sha256>` (`Pin.Sidecars`;
  `SidecarCodec`, `SidecarAudioCpp`, the kind list is open and checked for shape
  only, `isSidecarKind`). A `-gpu` bin shares its platform's rows; one row per
  kind per arch. It is the one `#!` key `cmake/opencoti-fetch.cmake` reads too:
  every row of the arch is staged beside the engine under its published file
  name, never renamed (offline: `-DXOLLAMA_OPENCOTI_SIDECAR_DIR=<dir>`, still
  verified), and `scripts/docker-assemble.sh` and
  `.github/workflows/xollama-release.yaml` check the staged bytes against the
  pin. Guards: `llm/engine/pin_sidecar_test.go`,
  `llm/engine/pin_sidecar_cmake_test.go`.
- Windows: `Pin.ArchFor` (used by `pinUncoveredIn`) falls back to the bare
  `bin win-x86_64` (+ `dso win-x86_64`) when no `win-x86_64-gpu` bin exists; the
  `-gpu` row wins when both do. `cmake/opencoti-engine.cmake` makes the same
  choice, stages an extensionless APE as `<name>.exe`, and puts it in
  `lib/ollama/engines` so stock `llama-server.exe` never loads its `ggml-cuda.dll`.
  A dev snapshot's `dso win-x86_64` row can lag the build: without it Windows
  CUDA is refused to llama.cpp for a stated reason
  (`TestTheCommittedPinRoutesWindowsCUDAByItsDLLRow`).
- A tested platform does **not** have to have an artifact — a dev pin ships a
  subset. It must be served by the pin or refused for a stated reason, which is
  what `TestEveryTestedPlatformIsServedOrRefused` in `llm/engine/pin_test.go`
  asserts: no `bin` row for a tested platform means `pinUncoveredIn` has to
  return a reason, never route. `cmake/opencoti-engine.cmake` matches it: no
  `bin <arch>` row is a STATUS line and a llama.cpp-only package, not a
  configure failure; more than one row is still fatal.
- Asset rows are `bin` (the engine), `dso` (a side-loadable GPU payload staged
  beside the binary), `dso-cuda12` or `sidecar`; release bins embed their payloads, a dev
  snapshot is a bare APE that needs one. Address them with `pin.Asset` (bin rows
  only), `pin.DSO`, `pin.CUDA12DSO` and `pin.Sidecars`; build offline with
  `-DLOCAL_DSO_FILE=<payload>`. One row per kind per arch and at least one
  `bin` — `TestCommittedPinParses` fails duplicates and dso-only pins.
- **Media sidecars** are `#! sidecar <arch> <kind> <path> <sha256>` rows
  (`Pin.Sidecars`, `kindSidecar`): libraries the engine loads from its own
  directory under their PUBLISHED file name — `codec` (oc-codec: mp3, opus,
  aac, mp4), `audiocpp` (oc-audiocpp: Kokoro, Supertonic, KittenTTS) and
  `espeak` (oc-espeak: eSpeak-ng, the phonemiser Kokoro and KittenTTS use,
  loaded by audio.cpp; the three are taken as a set from one `rev`). A
  library whose licence obliges its text to travel with it has its own rows,
  staged like any other: `espeak-licence` (`COPYING.espeak-ng`) and
  `build-info` (`BUILD_INFO.md`, the codec's source references), one per arch
  (`licensedSidecars`, `TestEveryLicensedSidecarShipsItsText` in
  `llm/engine/pin_licence_test.go`). The
  kinds are opencoti's and open-ended: `ParsePin`
  and `cmake/opencoti-fetch.cmake` check a row's shape (`[a-z0-9-]+`, a
  repo-relative path, a sha256, one row per kind per arch), never a list of
  kinds, and the fetch stages EVERY row of the arch beside the engine, never
  renamed. `arch` is a bin label; a `-gpu` bin takes its platform's rows. That
  one script is the CMake build's, `scripts/docker-assemble.sh`'s and
  `xollama-release.yaml`'s staging, so all three ship them; the Windows
  installer takes `lib\ollama` recursively and `payloadId` hashes every file
  under it. Build offline with `-DXOLLAMA_OPENCOTI_SIDECAR_DIR=<dir>`
  (`-DLOCAL_SIDECAR_DIR` to the script). Never hand-place a sidecar: every
  media test before 2026-10-03 ran on hand-placed copies, and no build
  shipped them. Guards: `llm/engine/pin_sidecar_test.go`,
  `TestCMakeStagesTheSidecarsThePinNames` (runs the real script).
- `Find` in `llm/engine/opencoti.go` knows **both** artifact names: release
  `opencoti-llamafile-<version>-<tag>-<arch>.llamafile[.exe]` and the dev bare APE
  `opencoti-<version>-<build>` (no extension; `filepath.Ext` sees the version's
  dots). `isArtifact` decides by a known extension first, else the executable bit
  (APE `0755`, CUDA payload `0644`). Missing either shape falls back silently.
- A test about policy must not also be a test of what this branch pins: stub the
  pin with `withPin` (`llm/engine/coverage_test.go`), which swaps `loadPin` and
  restores it in `t.Cleanup`. `TestSupportsDeviceHonoursTheCUDAComputeFloor` in
  `llm/engine/policy_test.go` routes against an all-payload pin so what it
  measures is the compute floor, not the payloads the shipped pin happens to carry;
  `TestResolve` in `llm/engine/resolve_test.go` does the same with `allPayloadsPin`.
- Moving to a new artifact is one commit: `repo`, `rev`, `tag`, `channel`, every
  `sha256`, the `feature`, `accel`, `cuda-sass` and `cuda12-sass` rows corrected
  to what the new bytes carry, and any `llm/engine_defects.go` row those bytes
  retire (see `.claude/rules/engine-defects.md`). A re-published cut keeps the
  tag and the file names and changes every `sha256`.
- Background and the channel table: `docs/features/engine-opencoti-llamafile.md`.
