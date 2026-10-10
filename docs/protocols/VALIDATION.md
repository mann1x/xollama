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

**The pin on opencoti c11 r2** (2026-10-10, engine `2610101328001`, cuda
`2610100758001`, vulkan `2610100032004`, macos `2610100032005`, sbsa
`2610100758003` new; before `v0.40.1-rc.3.xollama`; working directory
`/srv/ml/xc11/r2`). Every component but media moved, so G1, G3 to G8, G10,
G11. G1: gofmt silent, lint 0, 43 hooks, compat-origin 0 foreign, `go test`
of the packages the change touches ok. G3: compat 8/8, `llama3` 76.7 to 77.6
tok/s, four slots 302.8, Gemma 4 right, overflow 3.73. G4: decision scores as
on c10, 0 stock loads; chat paths all pass. G5: PolyKV 4/4, 5 pools, 0
refusals; no-PolyKV and idle pass. G6: speech 8/8, both video models 33/33.
G8: 3090 123.0 cold, 124.7 to 124.9; 9070 XT 100.2 cold, 102.8, 103.6 after
the idle; engine gone 831 ms after the kill; integrated 5.2; speech 4/4. G10
(engine replaced in `:dev` on the Pi): 10.0 against 10.9 to 11.1, speech 5/5.
G11 (`mac-engine.sh`): `qwen2.5:1.5b` 123.4 to 127.2, `llama3` 30.8 to 31.8,
speech 7/7 answered. The drafter case of bug-260: dropped on both boots, 54.2
to 54.9 tok/s. On `:dev` of `5cd88a68f` (run 38058625153): G7 38.8 to 39.0
tok/s on the integrated GPU through the image, 29/29 layers, polls 200, 0
crash lines; G10 amd64 opencoti 85.4 to 85.8, llama.cpp 85.7 to 86.2, speech
4/4; G10 arm64 10.0 to 10.1 against 11.0, speech 3/3.

**`v0.40.1-rc.2.xollama` and `v0.40.1-xollama`** (2026-10-10, engine c10 r3
unchanged; Go-only delta: the `drafter` and `prune-guard` hooks). G1 to G3
before the candidate (see the entry of 2026-10-09). G9, the update path on
eleven2go: `xOllamaUpdate.exe` rc.1 to rc.2 and rc.2 to the release, exit 0
both times, payload id `10ffa638…` unchanged; 3090 CUDA 123.7 to 123.9 tok/s,
9070 XT Vulkan 103.1, integrated 5.4, 37/37 layers; the release 122.4 and
123.6 on the 3090. G12: 8 files match `sha256sum.txt` on both; the candidate's
image is amd64 + arm64 and `:latest` did not move. **Not covered by a gate
until now:** a drafter at depth on a card the model nearly fills (bug-260,
`docs/features/gemma4-drafter.md`); the check is `/srv/ml/xc10/drafter/fit16.sh`.

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

**On upstream v0.40.0** (2026-10-07, candidate `v0.40.0-rc.1.xollama`, tree
`457f6ed1b`, still opencoti c9; working directory `/srv/ml/xc9/sync40`, scratch
stores of hard links, since this version writes `manifests-v2` and converted
model copies into the store it serves). G2: 26 checks pass, 12 skipped. G3:
opencoti compat 8/8, llama3 76.6 tok/s, 4 slots 302, overflow 3.8; llama.cpp
b11351 compat 8/8, 78.3, 616, 2.9. G5: the PolyKV tag 4 runs of 4 (5 pools, 0
refusals each, convened 161 to 294 s), the no-PolyKV tag and the idle tag pass
(the idle tag 9 room refusals in its 16k window, answered; earlier runs of that
tag ranged from 0 to several hundred). G9: full installer exit 0 in 38 s over
`.4`, update installer exit 0 in 4 s with a new server; RTX 3090 118.8 cold,
120.9 to 122.3 warm; RX 9070 XT 102.2, 102.4 to 102.5; integrated GPU 5.0;
16 alternating Vulkan loads on the installed server 0 failed; displays `OK`.
G10 (`:dev` of the tree): amd64 opencoti 85.6 to 85.9, llama.cpp 85.9 to 86.0,
speech 4/4; arm64 9.9 to 10.0 against 10.8 to 11.0, speech 3/3. G11 (the
workflow's app): `qwen2.5:1.5b` 128.4, `llama3` 32.1, the MLX model
`qwen3.5:2b-nvfp4` 77.0 on the pinned MLX libraries, speech 7/7 answered. G12 on
the published pre-release (merge `e311046b5`): every checksum OK, payload id
as tested, amd64 and arm64 binaries name the version, the image on both
registries for both architectures, `:dev` moved and `:latest` not; the installed
`.4` app on the Mac mini updated itself to it and served 129.5 tok/s.

**On opencoti c10** (2026-10-07, engine `2610072136001`, the release bytes;
every library the dev snapshot 2 file; working directory `/srv/ml/xc10`, the
same scratch stores). G3: compat 8/8, llama3 77.26 tok/s, 4 slots 299.7,
overflow 3.27 (the axis spans 3.13 to 4.01 on two engines back to back:
noise). G4: all chat paths pass, 0 refusals; `/v1/systemone` nimble and
clef-flash both on opencoti (stock loads 0). G5: the PolyKV tag 4 runs of 4,
5 pools and 0 refusals each (convened 156 to 172 s); the no-PolyKV tag PASS
(219 / 303 s). G6: speech 8/8 with a root-owned `/tmp/audiocpp-gguf` put there
first (bug-3955), video 33/33 frames on Wan2.1 and Wan2.2. G7: **solidPC's
Renoir iGPU is refused by the engine since c10** (RADV 20.3.5 and amdgpu-pro
2.0.154, "loses the device or crashes inside the driver"): the gate now checks
that a refused device is not placed on (`discover/opencoti_refused.go`) and the
load runs elsewhere; Vulkan throughput is measured by G8 on the RX 9070 XT. G8:
RTX 3090 123.3 to 123.8 tok/s; RX 9070 XT 100.4 cold, 102.9 to 103.0 warm,
103.6 after 120 s idle, polls 200, engine gone 1047 ms after the kill;
integrated GPU 5.1; speech 4/4; bug-3954 at the engine (both cards on Vulkan,
`-ts 1,1`, `scripts/gates/windows-twogpu-engine.ps1`): fresh and after a
request identical, where the c10-dev 1 engine differs from token 196. G11 (dev
build, release engine): `qwen2.5:1.5b` 129.2 to 129.6, `llama3` 32.1 to 32.4,
speech 7/7 answered; Clef 27B on Metal (bug 0.942, image red 0.994). G10 (`:dev` of `e8d56490e`, run 37690699916): amd64 opencoti 85.2 to 85.6,
llama.cpp 85.7 to 86.4, speech 4/4 (one Warn, "free-memory refresh found
nothing", during the speech phase: transient, not a crash); arm64 on the Pi
with the rebuilt CPU runtime 9.8 to 9.9 against 10.8 to 10.9, speech 3/3.

**On upstream v0.40.1 and opencoti c10 r3** (2026-10-09, engine
`2610090401001` = the c10 release engine + 0594/0595, every library c10's
file; tree `8d6f3fb76` + the pin, then the council fix; working directory
`/srv/ml/xc10/r3`, the same scratch stores). G1: gofmt silent, lint 0, 41
hooks, compat-origin 0 foreign; `go test ./...` fails only in the MLX
packages, on this host's environment (bug-256: with `LD_LIBRARY_PATH` at the
local MLX they pass but upstream's Metal-only
`TestSetWiredLimitRejectsOversizeWithoutChangingLimit`). Security check:
govulncheck found 4 reachable `x/net` advisories, fixed by `x/net` 0.60.0 (0
reachable after); Dependabot 99, none on a fork line. G3: compat 8/8, llama3
77.47 tok/s, 4 slots 301.3, overflow 3.75; gemma4 tool call and thinking. G4:
all chat paths pass, 0 refusals; `/v1/systemone` scores identical to c10 r1 to
the digit. G5 before the fix: the PolyKV tag and the idle tag each failed one
second turn on the `continue` route, repeating the first answer (bug-255);
after it, all six runs PASS (the PolyKV tag 4 of 4, 5 pools and 0 refusals
each), `continue` taken 4 times and right each time. G6: speech 8/8, video
33/33 on Wan2.1 and Wan2.2. G7: the Renoir iGPU refused, the load on the CPU
(36 tok/s), never on the refused GPU. G8 (side server, kill test with the
model on the RTX 3090, never on the RX 9070 XT): 3090 123.4 to 124.1 tok/s;
9070 XT 98.7 cold, 102.3 to 102.5 warm, 103.2 after 120 s idle, polls 200,
card `OK`, no dump; engine gone 1071 ms after the kill; integrated GPU 5.0,
37/37 layers; speech 4/4. On the candidate `v0.40.1-rc.1.xollama` (merge
`49677a133`, payload id `10ffa638…`): G2 20 pass, 4 skipping, the 4 native
legs without a runner cancelled. G9: the full installer over `.1` exits 0 in
98 s (the new MLX fetched, `MLX_ID` `31c743bd…`), and the small one over it
exits 0 in 4 s. On the installed server: 3090 123.4 to 123.9, 9070 XT 102.6 to
103.2, iGPU 5.2. G10: amd64 opencoti 85.4 to 85.9, llama.cpp 85.8 to 86.0,
speech 4/4; arm64 9.9 to 10.3 against 11.0, speech 3/3. G11 (the workflow's
app): `qwen2.5:1.5b` 127.8 to 128.4, `llama3` 32.1 to 32.3, speech 7/7. G12:
every checksum OK, both binaries and both image architectures right.

**Before `v0.40.1-rc.2.xollama`** (2026-10-09, tree `85de6b296`: the drafter
settings and the prune guard over rc.1; engine and pins unchanged; working
directory `/srv/ml/xc10/drafter`). The change touches `llm/` (one launch
argument, only for a model that turned drafting off), `server/`, `manifest/`
and `cmd/`, so G1, G3, G4 and, on the candidate, G2 and G12. G1: gofmt silent,
lint 0, 43 hooks, compat-origin 0 foreign, `go test ./...` 70 packages ok and
the one upstream Metal-only MLX test failing as before. Security check: no
delta (Dependabot 99, govulncheck 0 reachable). G3 on a quiet host (load 5 to
7 from other tenants): opencoti compat 8/8, llama3 76.52 tok/s, 4 slots 289.8,
overflow 3.35; llama.cpp compat 8/8, 78.61, 618.2, 2.38. The four-slot figure
on opencoti is 3.8 % under the morning's 301.3 with the host not idle, and the
stock control did not move down, so it is read as noise, not carried forward
as a new baseline. A first run of the same axes gave 70.6 on stock and 76.2 on
opencoti while a container (`byparr`) held the load average near 80; those
numbers are discarded. G4: every chat path passes, 0 refusals. The drafter
itself, live as `ollama` (`live1.out` to `live4.out`): fetch from the hub,
attach, replace, detach, six rewrites with the weights intact, 0 foreign
files; tok/s in `docs/features/gemma4-drafter.md`. G8 was not run before the
candidate: eleven2go was held by opencoti's c11 gates; the Windows check is
the candidate's install (G9 steps) once the host is free.

**`v0.40.0-rc.2.xollama`** (2026-10-08, PR #14, release run 37698039727,
image run 37699889866, payload id `d413785f…`). G9 on eleven2go over the
installed rc.1: `xOllamaSetup.exe` exit 0 in 31 s, the MLX runtime already
there under the pinned `MLX_ID` `c0ea38f2…` kept across the install (2,642
files, 1,969 MB; the download itself ran in the release job, `/MLX=always`,
1,338 MB, `MLX_ID` checked); version, `PAYLOAD_ID`, 22434 and `/api/xollama`
right; five 512-token runs of `qwen3:8b` each: RTX 3090 123.0 cold, 123.3 to
123.5 warm; RX 9070 XT 100.3 cold, 102.9 to 103.0 warm; integrated 5.0;
displays `OK`; ollama on 11434 untouched. `xOllamaUpdate.exe` over it exit 0
in 4 s, a new server pid, version, `PAYLOAD_ID` and `MLX_ID` unchanged, 121.9
tok/s after. One Warn before the integrated GPU's load ("free-memory refresh
found nothing", Vulkan; the load placed right), as in G10. G12: every
`sha256sum.txt` line OK (the Windows installers hashed on eleven2go), amd64
and arm64 (on the Pi) name the version; the image is amd64 and arm64 on
Docker Hub and GHCR, `:dev` moved to it, `:latest` did not.

**`v0.40.0-xollama`** (2026-10-08, PR #15 from rc.2's tree, `tree matches`,
merge `4e4534103`, release run 37701523918): every `sha256sum.txt` line OK,
payload id `d413785f…` (the candidate's), amd64 names the version. Short check
on eleven2go: `xOllamaUpdate.exe` over rc.2 exit 0 in 4 s; version,
`PAYLOAD_ID`, `MLX_ID`, a new server on 22434 and `/api/xollama` right; RTX
3090 123.1 and 123.6 tok/s over 512 tokens; ollama on 11434 untouched.
Promoted 2026-10-08 (Discord announce run succeeded).

**`v0.40.0-xollama.1`** (2026-10-08, re-release, PR #16, merge `4d695c111`,
release run 37731569137, image run 37732870439; payload id `d413785f…`,
unchanged). G1 on `73d226dd3` clean; G2 4 pass, 4 skipping. G9 on eleven2go
(rebooted): `xOllamaUpdate.exe` over the installed `v0.40.0-xollama` exit 0 in
120 s, of which the new MLX runtime (`ollama-windows-amd64-mlx-reldir.zip`,
1,338 MB, `MLX_ID` `b715921b…`, 2,642 files) about 115 s; `xOllamaSetup.exe`
over it exit 0 in 28 s, MLX kept; version, `PAYLOAD_ID`, 22434,
`/api/xollama` right; RTX 3090 123.1 cold, 123.3 to 123.5 warm; RX 9070 XT
102.7 cold, 102.9 to 103.0 warm; integrated 5.7; displays `OK`; ollama on
11434 untouched; no refresh Warn: 17 refreshes, CUDA and Vulkan side by side
in 1.3 / 1.5 s each, none failed (bug-249); `qwen3.5:0.8b` on MLX on the
installed server 66.7 / 67.8 tok/s with no cuDNN installed. G11 on the
release's own app: codesign and notarization OK, no `llama-server` or
`llama-quantize` in the bundle, `qwen2.5:1.5b` 127.3 to 129.6 and `llama3`
32.3 to 32.5 tok/s over 512 tokens, speech 7 of 7, every load on opencoti
through Metal, `XOLLAMA_ENGINE=llamacpp` refused with the reason. G12: every
checksum OK, amd64 and arm64 (Pi) name the version, image amd64+arm64 on both
registries, `:dev` moved. Promoted 2026-10-08, Discord announced.

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
- **Decision models:** `scripts/gates/decision.sh`, on a side server whose
  `lib/ollama` is the fork's pinned stock runtime and whose engine is the pinned
  opencoti. `nimble` is listed with the single capability `decision`, and
  `clef-flash` with `decision` and `vision` (upstream v0.40.0; before it,
  `decision` alone); `/v1/systemone` answers a `choice`, a `noul` with a `score`, and
  for `clef-flash` a question about an image. Expected: every line `PASS`;
  `nimble` loads on opencoti; a Clef model loads on stock for as long as the pin
  has no `clef_score_v1`, and on opencoti from the pin that declares it.
  Baseline (c9, 2026-10-05): `nimble` on opencoti "bug" 0.9793, noul 0.9989,
  score 0.8306 (stock 0.9778, 0.9989, 0.8245); `clef-flash` on stock "bug"
  0.8509, noul 0.9317, score 1.0620, the red image "red" 0.9855. Without the
  routing a Clef model fails on opencoti c9 at load, "wrong number of tensors;
  expected 549, got 427", and every call is a 500: that shipped in
  `v0.35.1-xollama.2`.

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

### G7. Vulkan on Linux, through the image

- **Where:** solidPC, the Renoir integrated GPU, in a container of the image
  under test with the GPU's render node passed in (`/dev/dri/renderD129`).
  Since 2026-10-10: the host's own Vulkan drivers (RADV 20.3.5, amdgpu-pro
  20.40) are refused by the engine since c10, so on the host this gate could
  only show the refusal. The image carries Mesa's drivers
  (`mesa-vulkan-drivers`, `Dockerfile.xollama`), and the container is how a
  user in that position runs it.
- **Steps:** `IMAGE=<image> scripts/gates/vulkan-linux.sh`: the device list,
  four 512-token runs (`bench.sh`), `/api/engine?endpoint=props` and `kv`
  twice with the model loaded, one more generation. The container has its own
  store (`VKSTORE`, default `/srv/ml/gates/vkstore`) and is removed at the end.
- **Expected:** the GPU listed with backend Vulkan and engine opencoti, every
  layer offloaded, `props` and `kv` answer 200, the generation after them
  works, `crash lines in the server log (want 0): 0`. With no device passed
  the list is empty: llvmpipe is never a GPU.
- **Baseline:** 38.3 to 38.9 tok/s (`qwen2.5:1.5b`, Mesa 25.2.8 RADV, 29/29
  layers), 200 on all four polls, 38.9 after them; measured 2026-10-10 on the
  published `:dev` image of `cb6b910b0` (engine c10 r3 `2610090401001`), 37.8
  on the first run. On the host before the refusal: 33.5 tok/s.

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
- **The update path** (added 2026-10-05, part of every G9 run from
  `v0.35.1-xollama.1` on): after the full install, the same release's
  `xOllamaUpdate.exe` is run over it with `scripts/gates/windows-update.ps1`
  (copied to the host, `-File`, `-Exe <path>`). Expected: exit 0, the version
  unchanged, a new server on 22434. An update built for another payload must
  exit 7 with "built against a different inference engine" and leave the
  running server alone.
- **Failed, `v0.35.1-xollama`, 2026-10-05:** its `xOllamaUpdate.exe` exits 1,
  "Runtime error (at 1:107): Internal error: An attempt was made to expand
  the "app" constant before it was initialized." The payload check stood in
  `InitializeSetup`, where `{app}` does not exist; the file had never been run
  by a gate, the candidates were installed with `xOllamaSetup.exe` only. The
  release was not promoted. Fixed in `app/xollama.iss` (`PayloadRefusal`, asked
  from `PrepareToInstall`), guard `app/updater/installer_script_test.go`.
  Measured on eleven2go with installers compiled there from both scripts
  (Inno Setup 6.7.3): the released script reproduces the error; the fixed one
  with a wrong payload id exits 7 and the server keeps its pid; the fixed one
  exits 0 in 4 s and `xollama --version` goes from `0.35.1-rc.2.xollama` to
  `0.35.1-xollama`, a new server on 22434.
- **Failed again, `v0.35.1-xollama.1`, 2026-10-05:** the full installer
  passed (exit 0 in 30 s, version, `PAYLOAD_ID` `9702e4d6…`, 22434), and the
  small one no longer crashed but refused: exit 7, "built against a different
  inference engine", over the older install and over its own full install.
  `payloadId` in `scripts/build_windows.ps1` wrote its progress line with
  `Write-Output`, so it returned that line and the id, and the update
  installer was compiled against the two joined by a space. Not promoted.
  Fixed (`Write-Host`, and `requirePayloadId` stops a build whose id is not a
  bare sha256). The first bug had hidden this one.
- **In CI since then:** the release workflow's Windows job runs
  `xOllamaSetup.exe` and then `xOllamaUpdate.exe` on the runner before
  anything is attached (step "the installers install"): both must exit 0, the
  installed `PAYLOAD_ID` must equal `payload-id.txt` and the version must be
  the release's. G9 on eleven2go still runs both; CI is the earlier net.
- **Released, `v0.35.1-xollama.2`, 2026-10-05** (merge `fbf40c889`, payload
  id `9702e4d6…`, opencoti c8): the small installer over the installed `.1`
  exits 0 in 4 s; the full installer exits 0 in 30 s; the small installer over
  that exits 0 in 4 s with a new server pid; version, `PAYLOAD_ID` and
  `/api/xollama` right after each. Five 512-token runs each of `qwen3:8b`:
  RTX 3090 on CUDA 123.2 cold, 123.2 to 123.5 tok/s warm; RX 9070 XT on Vulkan
  102.6 cold, 102.9 to 103.2 warm; integrated GPU 5.2; speech 4 of 4
  transcribed back; all three display devices `OK`; ollama on 11434 untouched.
- **Fixed in `v0.35.1-xollama.4` (opencoti c9, patch 0560):** the bug below
  shipped in `.2`. Measured on c9 with the same 14 stale files in place,
  34 alternating loads on the installed server (`inst-vk.ps1`, the tags
  created by the script itself): 0 failed on the `.3` draft, on the `.3`
  release, on the `.4` draft and on the `.4` release. Keep the run in G9: it is
  the only one that starts the engine in the logged-in user's session.
- **Known bug of `.2` (owner, 2026-10-05: promote and record it):** on the
  INSTALLED server (started by the tray app, Windows session 1) a Vulkan
  engine start fails now and then with "fatal error: --gpu vulkan was
  explicitly requested but Vulkan is not usable on this system", exit 256
  after about 2 s: 5 of about 33 starts while alternating the RX 9070 XT and
  the integrated GPU; the next start works, displays stay `OK`. A side server
  started over ssh (session 0): 0 of 32 on c8 and 0 of 32 on engine
  `2610041714001`; the engine's own device listing: 0 of 76. So not a c8
  regression as far as measured. Cause, measured by opencoti (mail #799,
  which withdraws #798): the engine's runtime opens
  `C:\ProgramData\cosmo\sig\<pid>.pid` at start; a stale file with the same
  pid left by an ELEVATED process cannot be reopened by a normal user, and
  the process then crashes at exit, so the GPU probe child returns no device
  count. eleven2go had 471 such files, 405 owned by Administrators. Nothing
  to do with the driver or Vulkan layers. An ssh session on eleven2go is
  elevated, which is why the side server never failed: a Windows test of
  this must run in the logged-in user's session. Released c8 fails 8 and 9
  of 40 probes there; opencoti's fixed dev engine 0 of 120. Workaround:
  delete the dead processes' files under that directory from an elevated
  shell. Fix: patch 0560-win-sigfile-exit, in c9. When c9 is pinned, run
  about 33 alternating Vulkan starts on the installed server. Scripts of the chase:
  `C:\Users\ManniX\xollama-c8\gates` on eleven2go, logs
  `/srv/ml/xc8/rerel-win-*.log`.
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
  published `:dev` image with only the engine's files replaced
  (`TAG=`, `BASE=`; the engine is the tree's, from `stage-engine.sh aarch64`).
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
- **The stores must be writable since upstream v0.40.0.** A store this version
  cannot write lists no models and every request is a 404 (`mkdir
  /models/manifests-v2: read-only file system`); stock v0.40.0 does the same.
  `image-x86.sh` took the shared stores read-only and failed its speech half on
  2026-10-07; it now takes `LLM=` and `MEDIA=`, scratch stores of hard links
  mounted writable (0 files of another owner afterwards).

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

## The first release

`v0.35.1-xollama.2`, promoted 2026-10-05, is the baseline release: G1 to G12
were all run on its tree or on the candidate tree it differs from only in the
installer script and the build script. Beside the rows above, on `.2` itself:
artifact, six checksums OK, both image architectures on Docker Hub and GHCR;
amd64 image on solidPC, `llama3`: opencoti 85.3 to 85.7 tok/s, llama.cpp 85.8
to 86.2, speech 4 of 4; arm64 image on the Pi 5 (47 °C at the start, 75 °C at
the end): opencoti 9.9 to 10.0 tok/s, llama.cpp 10.9 to 11.0, speech 3 of 3,
the owner's containers up; the Mac mini, signed and notarized:
`qwen2.5:1.5b` 128.6 to 128.9 tok/s, `llama3` 31.9 to 32.0, speech 7 of 7
answered, no crash line. `v0.35.1-xollama` and `v0.35.1-xollama.1` stay
pre-releases with a broken update installer (G9).

`v0.35.1-xollama.4`, promoted 2026-10-05 (merge `37ba94ec3`, payload id
`3ac8a57c…`, opencoti c9): the delta gates of a pin move and of a macOS app
change, on the draft and again on the published files. G3 to G7 on c9 as in
`llm/engine/pin/index.txt`; G4's decision check 7 of 7. G8/G9 on eleven2go:
small installer over `.2` refused (exit 7), over `.3` applied, full installer
and small one over it exit 0; RTX 3090 123.5 to 123.8 tok/s, RX 9070 XT 102.9
to 103.2, integrated GPU 5.0 to 5.1, speech 4 of 4, Vulkan loads as above.
G10 on the published tag: amd64 85.6 tok/s on opencoti, 85.2 to 86.3 on
llama.cpp, speech 4 of 4, `nimble` on opencoti and `clef-flash` on stock in
the image, no crash line; Pi 5 (48.8 °C to 76.3 °C) 9.8 to 10.0 tok/s on
opencoti, 10.8 to 11.0 on llama.cpp, speech 3 of 3, the owner's containers up.
G11: signed and notarized from `f433d0bc4`, `qwen2.5:1.5b` 128.6 to 128.9
tok/s, `llama3` 31.7 to 31.9, no crash line; **and the update path, new in
this release:** the installed `.3` app found `.4`, verified
`xOllama-darwin.zip` against `sha256sum.txt`, applied it on a hidden start,
passes `spctl` and `stapler`, 127 tok/s; its stage, backup and marker are in
`~/Library/Caches/xOllama`, its logs in `~/Library/Logs/xOllama`, and a stock
Ollama's download, backup and marker planted in `~/Library/Caches/ollama`
survive its start (`mac-update-34.sh` on the Mac mini). `v0.35.1-xollama.3`
stays a pre-release: it is the build that would delete a stock Ollama's staged
update on macOS.

## Not gated

Said here so nobody takes silence for a pass:

- **CUDA 12** (Maxwell, Pascal, Volta): legacy. It is pinned and shipped
  without a card to run it on, and never holds a release.
- **The NVIDIA payloads of the arm64 image** (Jetson, SBSA): no such hardware.
  That includes the engine's own arm64 CUDA library (`sbsa`, DGX Spark and
  Jetson Thor, pinned since c11): built blind, never run by opencoti or here.
  It never holds a release; the notes say so and name `XOLLAMA_ENGINE=llamacpp`.
- **Image and video models on macOS**: untested.
- **The eSpeak data path limit on Windows** (about 260 characters): not
  measured.
- **Long agentic council runs** (the benchmark tasks through a harness): a
  measurement series of their own in `plans/agentic-council-chat.md`, not a
  release gate. G5 is the gate.
