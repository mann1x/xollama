# Validation: the gates of a release

What a release has to pass, where, how, and what a pass looks like. The
release cycle itself is [`RELEASE.md`](RELEASE.md); this file is the list of
gates that cycle refers to, with the result each one gave the last time it
was run.

## The rule

- **The first release (`v0.35.1-xollama`) passes every gate below.** That run
  is the baseline: the numbers in the *Baseline* column.
- **Every later release passes the delta.** Run the gates the change touches
  (the table in *Which gates a change needs*), plus every gate that failed or
  regressed since the last baseline, plus the gates that always run. A gate
  that was not run keeps its previous baseline and its date; never copy a
  number forward as if it were new.
- **A gate that is run updates its row** here, in the same commit as the
  change or the release record, with the date, the tree and the engine it ran
  on. A result that is worse than the baseline by more than run-to-run noise
  (about 3% on tok/s) is a regression: it is fixed or explained in
  `STATE_SUMMARY.md` before the release, not averaged away.
- **A gate that cannot be run is said so**, with the reason, in the release's
  `STATE_SUMMARY.md` entry. It is never reported as passed.
- A probe generates at least 256 tokens (512 where the row says so), warm,
  several runs. Tests on solidPC run as the `ollama` user, never root. Side
  servers use port **22498**; an installed xOllama (22434) and a stock ollama
  (11434) on a test host are never touched.

## Which gates a change needs

| The change touches | Gates |
|---|---|
| anything (always) | G1, G2, G12 |
| the engine pin (`llm/engine/pin/`), any component | G3, G4, G6, G7, G8, G10, G11 for the platforms of the components that moved; G5 if the engine itself moved |
| `llm/`, `server/sched.go`, launch, placement, device selection | G3, G4, G8 |
| the council (`internal/council/`, `server/council*.go`, `types/xollama/council.go`) | G5 |
| media (`server/media*.go`, `llm/engine_media.go`, the `media` component) | G6, the speech parts of G8, G10, G11 |
| the llama.cpp runtime (`LLAMA_CPP_VERSION`, `llama/server`, `llama/compat`, a runtime pin) | G3 on llama.cpp, G9, G10 |
| the Windows app, installer or updater (`app/`) | G9 |
| the image (`Dockerfile.xollama`, `scripts/docker-assemble.sh`, its workflow) | G10 |
| the macOS app or its build (`scripts/build_darwin.sh`, `app/darwin/`) | G11 |
| an upstream sync | G1 to G5, G9, G10 |
| docs, plans, `.claude/`, `.wolf/` only | G1 |

## The gates

The scripts are in [`scripts/gates/`](../../scripts/gates/), as they were run
for the baseline: they name solidPC's paths and the engine file of that day,
so set the engine path and the store before reusing one. Working directory and
logs of the baseline run: `/srv/ml/xb140`.

Baseline: **2026-10-04**, tree `4db0bcedb`, engine `2610041714001`, vulkan
`2610041656001`, cuda `2610040656001`, media `2610040945001`, macos
`2610041000001`; candidate rows from `v0.35.1-rc.1.xollama`.

**On opencoti c8** (2026-10-05, engine `2610042347001`, the libraries
unchanged; working directory `/srv/ml/xc8`), every engine gate was run again
on the new bytes before candidate 2. G3: compat 8/8, llama3 77.3 tok/s, 4
slots 304, and the overflow model loads, which retired the last known-defect
row. G4: all pass, 0 refusals. G5: see its row. G6: speech 8/8, video 33/33
on both models. G7: 34.7 tok/s, polls 200. G8: RTX 3090 123.3 to 123.5 tok/s,
RX 9070 XT 103.2 warm and clean after the idle, engine gone 830 ms after the
kill, integrated GPU 5.4 to 5.5, speech 4/4. G10 (engine replaced in the rc.1
image on the Pi): 10.0 to 10.2 against 11.1, speech 5/5. G11: 125.0 to 127.9
and 31.5 to 31.7 tok/s, speech 7/7 answered.

### G1. Repository checks

- **Where:** solidPC, the tree.
- **Steps:** `gofmt -l .` · `go build -o xollama .` ·
  `go test -count=1 ./server/... ./llm/... ./types/... ./internal/council/... ./cmd/... ./discover/... ./model/... ./thinking/...`
  · `golangci-lint run` · `scripts/check-hooks.sh` ·
  `scripts/check-compat-origin.sh origin/main..HEAD` (after `git fetch fork`
  and `git fetch upstream`).
- **Expected:** nothing from `gofmt`, every package `ok`, `0 issues`, every
  hook with a Registry row, `0 not from the fork or upstream`.
- **Baseline:** all so; 35 hooks. Known noise: `go test ./...` fails in `mlx`,
  `mlxrunner` and `mlxrunner/sample` on solidPC (no MLX runtime there), and
  `TestTheCLIFallsBackToTheStockPortOnlyForAnXollama` fails when another
  service holds port 8191.

### G2. Hosted CI on the release PR

- **Where:** GitHub, the PR `dev` into `main`.
- **Steps:** after every push, `gh pr checks <N> -R mann1x/xollama`, read all
  of it.
- **Expected:** every check `pass` or `skipping`; none failed, none pending.
- **Baseline:** PR #5 at `4db0bcedb`: 23 pass, 12 skipping, none failed (the Windows and macOS test legs included).

### G3. Engine measurement, opencoti against stock llama.cpp

- **Where:** solidPC, RTX 3090, as `ollama`.
- **Steps:** `scripts/gates/engine-ab.sh` (it runs
  `scripts/phase2-engine-ab.py --engine opencoti --axis all` on the staged
  engine), then the same with `--engine llamacpp` and no `--artifact`. This is
  also the "off means off" control: `XOLLAMA_ENGINE=llamacpp` must load and
  answer the same models.
- **Expected:** compat 8 of 8 on both; tok/s within noise of the baseline.
- **Baseline:** opencoti: compat 8/8, llama3 77.4 tok/s, 4 slots 301 tok/s in
  aggregate, a 70B model that overflows the card 3.1 tok/s. llama.cpp:
  compat 8/8, llama3 78.5 tok/s, 4 slots 591 tok/s in aggregate, the
  overflow model 2.9 tok/s. The four-slot gap is known, not new: stock
  llama.cpp gave 620 tok/s on 2026-09-21 and opencoti 144 before this engine
  line. The throughput axis generates about 45 tokens after a 4.7k-token
  prompt; it is a comparison between engines on one script, not a tok/s to
  quote.

### G4. Chat paths

- **Where:** solidPC, side server as `ollama`.
- **Steps:** `scripts/gates/chat-paths.sh` (starts the server, runs
  `chat-paths.py`): three turns of one conversation on two models over
  `/api/chat`, an OpenAI stream, a tool call and the turn carrying its result.
- **Expected:** every line `PASS`, `kv-reservation refusals: 0`.
- **Baseline:** six turns, the stream, the tool call and its result pass; 0
  refusals.

### G5. Council

The steps, the three tags, how to rebuild them and the expected result per tag
are in [`RELEASE.md`, "The council gate"](RELEASE.md#the-council-gate).
Wrapper: `scripts/gates/council.sh <tag>...`, which runs
`scripts/council-gate.py`.

- **Run the PolyKV tag at least four times, each on a fresh server.** A
  council turn depends on what the builder writes, and one pass proves little:
  on 2026-10-05 the tag failed 4 runs of 7, on two engines, only when the
  builder gave the planner a think level (the schema conversion of a thinking
  member with a format was refused for room; fixed, `llm.GrammarWindow`). The
  gate prints calls, tokens and tok/s per turn: a slow turn with many tokens
  is the council working (40 to 60 tok/s is normal here), a slow turn with few
  is a fault.
- **Baseline:** `gate/council-kv3-384k` three PASS, 5 pools, 0 refusals, 8 /
  33 / 32 s; `gate/council-kv3-nopolykv` three PASS, 0 pools, 9 / 43 / 40 s;
  `omni-council-idle` three PASS through the unpooled turn, 7 / 357 / 7 s.
  On c8 with the fix: the PolyKV tag 4 runs of 4, 5 pools and 0 refusals
  each, convened turns of 55 to 291 s (2,400 to 14,500 tokens), second turns
  of 8 to 197 s; the other two tags three PASS each (51 / 340 s and 198 /
  157 s). Turn times vary with how much the council decides to do and are not
  a criterion.

### G6. Media on Linux

- **Where:** solidPC, side server on the media store
  (`/srv/ml/media-store/models`; the test models are mirrored in
  `/shared/dev/opencoti/.opencoti/models/media`, never downloaded again).
- **Steps:** `scripts/gates/speech-linux.sh` (eight voices over four speech
  models, each mp3 transcribed back by Whisper) and
  `scripts/gates/video-linux.sh` (Wan2.1 and Wan2.2, frames counted by
  `ffprobe`).
- **Expected:** every clip `http=200`, an mp3, and the transcription reads the
  sentence back ("The first ship came in at dawn."); each video `completed`
  with every frame the template asks for.
- **Baseline:** speech 8 of 8; video 33 of 33 frames on both models.

### G7. Vulkan on Linux

- **Where:** solidPC, the Renoir integrated GPU, CUDA hidden
  (`CUDA_VISIBLE_DEVICES=`, `OLLAMA_VULKAN=true`).
- **Steps:** `scripts/gates/vulkan-linux.sh`: four 512-token runs
  (`bench.sh`), then `/api/engine?endpoint=props` and `kv` twice with the
  model loaded, then one more generation.
- **Expected:** every layer offloaded, `props` and `kv` answer 200, the
  generation after them works. `crash lines in the server log` reads 4 on a
  good run: they are the CUDA probes failing with the card hidden, as the
  test intends.
- **Baseline:** 33.5 tok/s (`qwen2.5:1.5b`), 200 on all four polls. The engine
  before (`2610040950001`) answered 502 with the engine dead (bug-3921).

### G8. Windows, the engine beside an installed xOllama

- **Where:** eleven2go (RTX 3090, RX 9070 XT, Radeon integrated GPU; AMD
  Software 26.9.2 or later, see `README.md`). Check the AMD driver version
  first: on 26.8.1 the RX 9070 XT part can hang the display driver and freeze
  the machine, so say so before running it there.
- **Steps:** stage the pinned Windows files in a directory of their own,
  cross-build the tree's `xollama.exe`
  (`CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ GOOS=windows GOARCH=amd64 go build -ldflags '-s -w'`),
  then `scripts/gates/windows-side.ps1` and `scripts/gates/windows-speech.ps1`,
  each sent on stdin
  (`ssh eleven2go 'powershell -NoProfile -ExecutionPolicy Bypass -Command -' < script.ps1`).
  Unload a model (`keep_alive: 0`) before stopping a server, except in part E,
  where the kill is the test.
- **Expected:** A. each device prints a stable id (a PCI ID or `uuid:`); B.
  RTX 3090 on CUDA, four 512-token runs; C. RX 9070 XT on Vulkan pinned by
  name, `props` and `kv` 200, a generation after 120 s idle, the card `OK` and
  no new dump in `C:\Windows\LiveKernelReports`; E. the server killed with the
  model loaded: no engine left within a second or two, card `OK` 40 s later;
  D. the integrated GPU, every layer offloaded; speech: four models
  transcribed back; at the end 22434 and 11434 still listening.
- **Baseline:** 3090 123.4 to 123.8 tok/s; 9070 XT 100.8 cold, 103.1 to 103.3
  warm, polls 200, idle clean; engine gone 848 ms after the kill; integrated
  GPU 5.3 tok/s, 37/37 layers; speech 4 of 4; the Vulkan library says "abi
  ggml 41ba479c24d7 matches this engine"; ten `POST /shutdown` and reopen
  cycles clean.

### G9. Windows, the installer

`RELEASE.md` steps 5 to 7: the artifact check, the install through an
interactive scheduled task, and the full check (version, `PAYLOAD_ID`, 22434
and `/api/xollama`, `/api/xollama/devices`, a 512-token generation on the
GPU, the stock ollama on 11434 untouched).

- **Scripts:** `scripts/gates/windows-install-pre.ps1`, `windows-install.ps1`
  and `windows-install-check.ps1` on stdin; `windows-install-measure.ps1`
  copied to the host and run with `-File`, because it has multi-line blocks
  and stdin runs none of them (it printed three lines and nothing else the
  first time).
- **Baseline** (`v0.35.1-rc.1.xollama`, 2026-10-04, AMD Software 26.9.2): the
  installer exits 0 in 29 s; version and `PAYLOAD_ID` (`4ece3b6d…`) match the
  release; 22434 answers `/api/xollama`; the engines directory holds engine
  `2610041714001` with its CUDA, Vulkan and three media libraries; five
  512-token runs each of `qwen3:8b`: RTX 3090 on CUDA (pinned by PCI ID) 123.2
  cold, 123.4 to 123.6 tok/s warm; RX 9070 XT on Vulkan (pinned by name) 100.6
  cold, 102.9 to 103.0 warm; integrated GPU 5.1 tok/s; 37/37 layers on each;
  all three display devices `OK` afterwards; ollama on 11434 untouched.
  Seen, open: at the load on the integrated GPU, right after the RX 9070 XT
  unload, the free-memory refresh warned "context deadline exceeded" after
  504 µs and the load went on with the old values.

### G10. The image

- **Where:** solidPC for amd64 (`--gpus all`), dietpi5 for arm64 (Raspberry
  Pi 5, 16K pages; the owner's containers there are never touched, the test
  container has its own name, port and model directory). There is no Docker
  on the Mac mini, so the image is not run on macOS; the Mac is covered by
  G11.
- **Steps:** `RELEASE.md` step 7, "The image on dietpi5", on both hosts.
  Before an image exists for the tree, `scripts/gates/pi-image.sh` runs the
  published `:dev` image with only the engine's files replaced.
- **Expected:** the right architecture, `GET /api/xollama` names the version,
  `/usr/lib/ollama/PAYLOAD` names the pinned runtime and engine, a 512-token
  generation on opencoti and one on llama.cpp, a speech clip transcribed
  back.
- **Baseline:** arm64 with the engine replaced: opencoti 9.8 to 9.9 tok/s,
  llama.cpp 10.8 to 10.9 (`qwen2.5:1.5b`, interleaved), speech 5 of 5. The
  candidate's images (`mannixita/xollama:0.35.1-rc.1.xollama`,
  `scripts/gates/image-x86.sh` and `image-pi.sh` with `IMG=` set): amd64 on
  solidPC with the RTX 3090, `llama3`, four 512-token runs: opencoti 85.6 to
  85.9 tok/s, llama.cpp 85.6 to 86.1, speech 4 of 4 transcribed back; arm64
  on the Pi 5: opencoti 10.1 tok/s, llama.cpp 11.0 to 11.1, speech 3 of 3;
  `PAYLOAD` names the fork's runtime and every pinned engine file on both;
  the owner's containers and the stock ollama on the Pi untouched.

### G11. macOS

- **Where:** the Mac mini (`ssh macmini`, Apple silicon), in `~/dev/xollama`
  only. `~/dev/signing` holds secrets: read in place, never copied.
- **Steps:** `sh build-signed.sh` there (it runs
  `scripts/build_darwin.sh build sign app`: Developer ID signed, notarized).
  Then the bundle's own server on the side port
  (`XOLLAMA_HOST=127.0.0.1:22498 dist/xOllama.app/Contents/Resources/xollama serve`;
  the app's first-run dialog cannot be answered over ssh),
  `H=127.0.0.1:22498 scripts/gates/bench.sh <models>` and
  `scripts/gates/speech-mac.sh`.
- **Expected:** `SIGNED-BUILD-OK` and "The staple and validate action
  worked!"; the engine starts through its loader on Metal; tok/s within noise
  of the baseline; every speech clip transcribed back.
- **Baseline:** bundle built, signed and notarized from `4db0bcedb` (7 minutes); the engine through `ape` on Metal ("metal: abi ggml 41ba479c24d7 matches this engine"); `qwen2.5:1.5b` 125.9 to 127.4 tok/s, 29/29 layers; `llama3` 31.3 to 31.7 tok/s, 33/33 layers (four 512-token runs each); speech 7 of 7 answered, 6 transcribed back exactly and the OuteTTS `narrator` voice as "The first shoot came in at dawn."; no crash line in the server log.

### G12. The published artifact

`RELEASE.md` step 5 on what reached the release: `sha256sum -c`, the version
each binary names, both image architectures, the installer naming the fork's
releases endpoint.

- **Baseline** (`v0.35.1-rc.1.xollama`, merge commit `3284571c5`): the release
  run's five jobs succeeded; seven assets, every `sha256sum.txt` line OK; the
  amd64 binary on solidPC and the arm64 binary on the Pi name the version; the
  image is `linux/amd64` and `linux/arm64` on Docker Hub and GHCR; the
  provenance block names engine `2610041714001`. `strings` finds no releases
  endpoint in the compressed installer (expected; the installed app is checked
  in G9).

## Not gated

Said here so nobody takes silence for a pass:

- **CUDA 12** (Maxwell, Pascal, Volta): legacy. It is pinned and shipped
  without a card to run it on, and never holds a release.
- **The NVIDIA payloads of the arm64 image** (Jetson, SBSA): no such hardware.
- **Image and video models on macOS**: untested.
- **The eSpeak data path limit on Windows** (about 260 characters): not
  measured.
- **Long agentic council runs** (the benchmark tasks through a harness): a
  measurement series of their own in `plans/agentic-council-chat.md`, not a
  release gate. G5 is the gate.
