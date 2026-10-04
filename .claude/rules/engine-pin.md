---
paths:
  - llm/engine/**
  - cmake/opencoti-fetch.cmake
  - cmake/opencoti-engine.cmake
---

# opencoti engine pin

- The pin is opencoti's **pin format 2** (their `docs/protocols/PIN_FORMAT.md`),
  in `llm/engine/pin/`: `index.txt` is OUR index (channel, tag, one line per
  component or `<component> absent`), and each component it names
  (`engine`, `cuda`, `cuda12`, `vulkan`, `macos`, `media`; `sbsa` is absent)
  has its pin file vendored beside it, **byte-identical** to the published
  one. Never edit a vendored pin: the index carries its sha256 and both
  parsers refuse a copy that differs. No pin is fetched at build time.
  `llm/engine/pin/.gitattributes` (`*.txt -text`) keeps a Windows checkout from
  converting their line endings.
- It is parsed twice, by `LoadPin` in `llm/engine/pin.go` (embedded,
  `DefaultPin`) and by `cmake/opencoti-fetch.cmake`.
  `TestCMakeStagesWhatGoReads` (`llm/engine/pin_cmake_test.go`) runs the real
  script per platform and holds the two to the same files, names and modes.
- **Moving a component is one index line** plus its pin file: `rev` (the commit
  the pin file is readable at), `sha256` (of the pin file) and `version`. A
  component pin's own `rev` is the payload commit its files are fetched from,
  always 40 hex, never a branch. Components move one at a time, so every
  `Asset` carries its own `Repo` / `Rev` (`Pin.URL`).
- **Compatibility is checked, in both parsers**: every `abi <name> <digest>`
  of a component must be in the engine's pin, and `engine-min` must not be
  newer than the engine (build ids are 13 digits and compare as strings).
  Guard: `TestCMakeAndGoRefuseAnIncompatibleComponent`.
- An unknown KEY is an error; an unknown `file` KIND is **not**: it is staged
  like any other, which is how a new sidecar arrives without a parser change.
  Extra trailing fields on a fixed KEY are ignored.
- **Published names only, nothing renamed.** A file is staged beside the
  engine under the name in its row (`ggml-cuda-x86_64.so`,
  `ggml-cuda-cu12-x86_64.so`, `ggml-vulkan-win-x86_64.dll`,
  `oc-codec-linux-x86_64.so`), which is the name the engine looks for. The
  engine is `file any bin` for every platform plus `file win-x86_64 bin
  ....exe` for Windows; a platform takes its own row when there is one. A
  name is unique across the components of an index (`Asset.StagedName`): both
  parsers refuse two components that stage one name.
  The fetch removes what the classic pin staged under other names
  (`ggml-cuda.so`, `engines/cuda_v12`): a left-over library is one the engine
  may load instead of the pinned one.
- Modes: kinds `bin` and `ape` are staged 0755, everything else 0644. On the
  dev channel the exec bit is how `isArtifact` tells the engine from the
  libraries beside it.
- A component with no row for a platform is not staged there (its
  `BUILD_INFO` alone is not a payload); `Pin.Files(arch)` is the list.
- `Pin` keeps the shape routing uses: `bin` (the engine, `Pin.Asset`), `dso`
  (CUDA is the bare label, Vulkan `<arch>-vulkan`, `Pin.DSO`), `dso-cuda12`
  (`Pin.CUDA12DSO`) and `sidecar` (everything else, `Role` = the pin's kind,
  `For` on a licence). `Pin.Tag` is the engine's file name, `Pin.Version` its
  build id, `Pin.Channel` the index's.
- **Accelerators are the rows**: a `cuda` / `vulkan` / `metal` file for a
  platform IS the claim (`deriveAccels`); there is no accel list. Routing is
  the tested matrix in `llm/engine/policy.go` intersected with it
  (`pinUncovered`), so an uncovered accelerator goes to llama.cpp instead of
  being served silently on the CPU.
- `sass` on the `cuda` component is `Pin.CUDASASS` (major*10+minor;
  `Pin.CoversCUDA` matches the same major at that minor or later, no line
  means no narrowing); on `cuda12` it is `Pin.CUDA12SASS` (no component
  covers nothing). `deviceUnsupported` refuses an uncovered device by name.
- **CUDA 12 sits beside the same engine.** One process loads one CUDA
  library; the engine picks CUDA 12 itself only when every NVIDIA card is
  older than 7.5. `cudaPayload` decides per load, a load spanning both goes
  to llama.cpp, and `LegacyCUDA` makes the launch set
  `OPENCOTI_CUDA_LEGACY=1` (`opencotiEnvsForStart`, `llm/engine_media.go`);
  discovery lists CUDA a second time with it (`discover/opencoti_cuda12.go`).
  An explicit `XOLLAMA_ENGINE_PATH` is used as given. CUDA 12 is legacy: it
  ships without waiting for a card to test on. Guards:
  `llm/engine/pin_cuda_test.go`, `discover/opencoti_cuda12_test.go`.
- Engine capabilities come from `feature` rows and are never inferred from the
  cut number in the engine's name: the engine pin's own rows
  (`cache_type_swa_v1`, `log_memory_plan_v1`; `Pin.HasFeature`;
  `Pin.HasFeatureOn` for one limited to some platforms).
- `channel` is `release` or `dev`, declared in the index, never guessed.
  Never assert which Hugging Face repo is pinned: tests check the
  `<owner>/<name>` shape only.
- A tested platform does **not** have to have an engine: it must be served by
  the pin or refused for a stated reason
  (`TestEveryTestedPlatformIsServedOrRefused`). `cmake/opencoti-engine.cmake`
  matches: no bin row is a STATUS line and a llama.cpp-only package.
- Licence texts travel with their library: a `licence` row `for espeak` /
  `for codec`, and the media component's `BUILD_INFO`
  (`TestEveryLicensedSidecarShipsItsText`).
- **macOS** is `macos-aarch64` (`ArchMacOS`; Apple silicon only): the same
  engine file, the `macos` component's `ape` (the loader, `MacLoader`, staged
  executable) and `metal`, and the media three. The engine is started as
  `<dir>/ape-macos-aarch64 <engine> ...` (`run` in `llm/engine/opencoti.go`).
  Guards: `llm/engine/macos_test.go`.
- Build offline with `-DXOLLAMA_OPENCOTI_ENGINE_FILE=<engine>` and
  `-DXOLLAMA_OPENCOTI_SIDECAR_DIR=<dir holding every other file by its
  published name>`; both are still verified, sha256 and size, and a file
  missing from the directory is an error. `-DMANIFEST=<file>` makes the fetch
  write what it staged (`scripts/docker-assemble.sh`,
  `.github/workflows/xollama-release.yaml` check the payload against it).
  Never hand-place a file beside the engine.
- `Find` in `llm/engine/opencoti.go` knows both artifact names: release
  `opencoti-llamafile-<version>-<tag>-<arch>.llamafile[.exe]` and the dev bare
  APE `opencoti-<version>-<build>[.exe]`. `isArtifact` decides by a known
  extension first, else the executable bit.
- A test about policy must not also be a test of what this branch pins: stub
  the pin with `withPin` (`llm/engine/coverage_test.go`), or build one from
  text with `pinFiles` / `mustLoad` (`llm/engine/pin_fixture_test.go`).
- Moving the engine is one commit: the index line, the vendored pin, and any
  `llm/engine_defects.go` row the new bytes are *measured* to retire (see
  `.claude/rules/engine-defects.md`). What was measured goes in the comment
  block at the top of `llm/engine/pin/index.txt`.
- Background and the channel table: `docs/features/engine-opencoti-llamafile.md`.
