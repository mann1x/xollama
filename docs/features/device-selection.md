# Choosing the device a model runs on

## Pinning a model to a backend and devices

A model's `xollama.json` can say where it runs (schema v3):

```json
{ "version": 3, "devices": { "backend": "Vulkan", "ids": ["0000:18:00.0"] } }
```

| field | meaning |
|---|---|
| `backend` | `CUDA`, `Vulkan`, `ROCm` or `CPU`, any case. One backend per model: an engine runs one ggml backend per process. Required when `ids` is set, because the same PCI device is listed under more than one backend — a 3090 is both a CUDA and a Vulkan device. |
| `ids` | PCI IDs (`0000:18:00.0`, or `18:00.0`), device names (`name:AMD Radeon RX 9070 XT`, `#2` after the name for the second device of that name), `integrated` or `discrete`, or the backend's device index. Empty is every device of the backend; several spread the model across them. |

**A pin names a device, not a position.** The PCI ID is the identity to pin by
where discovery has one. Vulkan on Windows gives none, and the index is an
enumeration order: on eleven2go (RTX 3090, RX 9070 XT, Radeon iGPU) the 9070 XT
was `Vulkan1`, `Vulkan2` and `Vulkan1` again across three boots of 2026-10-04,
so a model pinned by index was refused, correctly, after each reorder. There the
pin is the device's name (`types/xollama/device_name.go`): `name:<name>`,
compared without regard to case or runs of spaces, within the pinned backend.
Several devices of one name are all selected by the bare name and told apart by
`#n`, their place among namesakes in the backend's order; with no PCI ID nothing
else distinguishes two identical cards, and that order can change. Both tweak
menus write the PCI ID when there is one and the name otherwise, never the
index (`selectorFor`, `cmd/tweak/devices.go`). An index pin still loads, and
every load on one says at Warn that it is positional. The server's GPU policy
(`xollama tweak server gpu`) keys a GPU the same way; a forced link speed needs
a PCI ID, because that is how the engine reads it. Guards:
`server/device_select_name_test.go`, `types/xollama/device_name_test.go`,
`TestAGPUWithoutAPCIIDIsWrittenByName`.

`selectModelDevices` (`server/device_select.go`, `device-select` hook in
`server/sched.go`) narrows the discovered devices to the pin before placement.
**A pin that cannot be met is a refusal, never a fallback**: it names what was
asked, the backends the installation carries and the devices present, and says
so when `OLLAMA_IGPU_ENABLE` is hiding an iGPU. A pin exists to keep a model
off hardware, and quietly serving it elsewhere breaks the one promise it makes.
`num_gpu 0` still means CPU; the pin is not consulted then.

**A media model is pinned the same way.** Its engine is scheduled as a twin of
the model (`mediaTwin`, `server/media.go`), and the twin carries the model's
`devices`, so the same hook narrows the GPUs before `pickMediaGPU` takes the
one with the most free memory among them. Until 2026-10-05 the twin dropped the
pin and every media engine went to the largest free GPU whatever `tweak model`
said. audio.cpp speech stays on the CPU regardless (`mediaOnCPU`). Guard:
`TestAMediaModelsDevicePinReachesTheScheduler`.

**An unpinned model is never placed on an integrated Vulkan GPU while a
discrete GPU is present.** The iGPU reports host RAM as its memory, so to the
placement code it looks like the largest device on the machine: a model too big
for the 3090 would move to the iGPU wholesale instead of partially offloading on
the card. An iGPU is something a model asks for. With no discrete GPU, the iGPU
is simply the GPU. For the same reason the startup context tier
(`defaultNumCtx`) does not count an integrated Vulkan GPU as VRAM — 63 GiB of
host RAM would otherwise lift every model's default context from 32768 to 262144
on solidPC.

`GET /api/xollama/devices` lists every admitted device with its backend, index,
PCI ID, integrated flag, memory and serving engine, plus the backends the payload
carries. `xollama tweak model --device-backend` builds its menu from it, and
`xollama show` lists `devices.backend` / `devices.ids` with the other settings.


## Integrated GPUs on Vulkan are admitted by default

Upstream hides integrated GPUs unless `OLLAMA_IGPU_ENABLE=1`, admitting only
integrated CUDA devices and an allowlist of ROCm GFX targets by default. This
fork adds Vulkan to that list (`igpu-vulkan` hook, `discover/runner.go`).

The reason is that Vulkan is not a fallback here. opencoti carries its own
Vulkan implementation and is the engine that serves it, so an integrated GPU is
not a degraded option — it is the case the path exists for: a small agentic
model answering at idle power, without waking a discrete card.

Measured on solidPC before the change, against the AMD RADV RENOIR (ACO) iGPU
at PCI `0000:18:00.0`, on the host's Mesa 20.3 stack with the 3090 hidden:

| | |
|---|---|
| model | `qwen3.5:2b` |
| generated | 512 tokens, coherent throughout |
| decode | 19.51 tok/s |
| resident | 2.47 GB on `Vulkan0` |

`OLLAMA_IGPU_ENABLE` still overrides in both directions, and devices on other
backends are unaffected. Under `XOLLAMA_ENGINE=llamacpp` the iGPU is hidden
exactly as upstream hides it — off means off.

<Warning>
  **An integrated GPU reports host RAM, not VRAM.** Discovery reports the
  Renoir iGPU as `total=63.1 GiB` / `available=63.0 GiB`, because its memory
  *is* system memory. Nothing stops the scheduler believing that figure, and
  `llm/llama_server.go` special-cases integrated devices only for CUDA and
  ROCm — not Vulkan. Size a model for the iGPU deliberately; do not read 63 GiB
  as headroom.
</Warning>

## The engine that runs a device is the one that lists it

When opencoti will serve a backend, opencoti enumerates it
(`opencoti-discover` hook, `discover/opencoti.go`). llama.cpp's discovery still
runs first, but for that backend the engine's own list decides which devices
exist, what index each has, and how much memory it reports. PCI ID and the
integrated flag are not something the engine prints, so they are joined from
what llama.cpp and the native probe found — by name first, then by position
where the memory agrees.

The two engines ship different ggml builds, so nothing guarantees they agree —
and the index `GGML_VK_VISIBLE_DEVICES` selects by is only meaningful to the
engine that produced it. On solidPC both builds list the Renoir iGPU **twice**,
once per installed ICD:

| index | listing | driver |
|---|---|---|
| 0 | AMD RADV RENOIR (ACO) | Mesa RADV 20.3 — measured working |
| 1 | Unknown AMD GPU | amdvlk 20.40 (`vulkan-amdgpu-pro`) |

The order follows the ICD enumeration order. Under the service both engines put
RADV at 0; with `VK_ICD_FILENAMES` listing amdvlk first, opencoti put it at 0
and RADV at 1 — so the index has to come from the engine that will run, never
from the other one's list. The overlay keeps one listing per physical device —
the one that joins an identity, else the one whose driver can name the chip —
under the engine's own index. A device llama.cpp sees that the serving engine
does not list is dropped: nothing could run on it. A device only the engine sees
is kept, so a payload without a llama.cpp Vulkan backend still schedules the
iGPU.

| | |
|---|---|
| gate | `engine.Enumerates` — tested matrix ∩ pin coverage, or `XOLLAMA_ENGINE=opencoti` |
| command | `<artifact> --server --list-devices -m /nonexistent.gguf --offline --verbose --gpu nvidia\|vulkan`, one run per backend (`--gpu` picks one) |
| when | once, at bootstrap. Free-memory refreshes keep upstream's path |
| off | `XOLLAMA_ENGINE=llamacpp` runs nothing and changes nothing |
| refused | since c10 the engine refuses a driver it has measured to crash (`ggml_vulkan: <name> (driver <x>) is REFUSED: …`: RADV from Mesa 20 or older, amdgpu-pro 2.0.154 or older). A listing that refused every device lists none and exits 1; that is the engine's answer, so the backend's devices are dropped at Warn and models load elsewhere (`discover/opencoti_refused.go`). `OPENCOTI_VK_ALLOW_OLD_DRIVER=1` keeps the device |

<Note>
  A Vulkan enumeration initialises every installed ICD. On solidPC the NVIDIA
  Vulkan ICD alone costs **8.2 s** to report no devices, against 43 ms for RADV
  alone — so the first discovery after a restart is that much slower. Restrict
  `VK_ICD_FILENAMES` in the service environment if it matters.
</Note>

`llm/engine/opencoti.go`'s `gpuFlag` then maps the devices the scheduler chose
onto the artifact's own selector, so a Vulkan placement launches the engine
with `--gpu vulkan`.

The engine's loader looks for `ggml-vulkan.so` beside itself; the published
file is `ggml-vulkan-x86_64.so`, which it never finds. `cmake/opencoti-fetch.cmake`
stages every payload under the loader's name.

## The engine must carry a payload for the backend

Routing is the tested matrix intersected with what the pinned artifact actually
ships — see `pinUncovered`. A dev snapshot is a bare APE that accelerates only
what the GPU libraries pinned beside it provide (a `cuda`, `vulkan` or `metal`
file row for the platform is the claim), so Vulkan reached opencoti for the first time with
snapshot `2609242056001`, the first to publish `ggml-vulkan-x86_64.so`.
The current pin (opencoti c8, engine `2610042347001`, `llm/engine/pin/index.txt`) carries
CUDA and Vulkan for Linux x86_64 and Windows x86_64; its Vulkan library is the
first that runs an AMD integrated GPU on Windows (measured on eleven2go with
engine `2610040710001`, 2026-10-04, and again with Vulkan library
`2610041656001` on this engine).

A pin can also narrow CUDA by silicon. `sass 86 120` in the `cuda` component's
pin (the dev snapshots before `70cee052`) says the payloads carry SASS for sm_86 and sm_120f only, so
`Pin.CoversCUDA` admits 8.6–8.9 and 12.x and routes 7.5, 8.0, 9.0 and 10.x to
llama.cpp, naming why. The current pin states `75 80 86 89 90 120`. Without it,
those cards would load on opencoti and run on the CPU. A pin with no
`sass` line keeps the engine's 7.5 floor alone.

**The CUDA 12 payload.** A pin may also carry a second, CUDA 12 library for
older cards: the `cuda12` component, with its own `sass` line. b208's was
`ggml-cuda-cu12-x86_64.so`, SASS sm_70 only (Tesla V100 / Titan V), driver >=
570. The current pin carries `ggml-cuda-cu12-x86_64.so` with
`sass 52 61 70` (Maxwell, Pascal, Volta) as a **legacy** payload: the
owner's decision (2026-10-04) is to publish it without a run on such a card,
which neither opencoti nor xollama has, and it never waits for one. Both CUDA
libraries sit beside the one engine under their published names
(`ggml-cuda-x86_64.so`, `ggml-cuda-cu12-x86_64.so`), and one engine process
loads one of them. Left to itself the engine asks the driver and takes CUDA 12
only when every NVIDIA card is older than compute 7.5; `OPENCOTI_CUDA_LEGACY=1`
makes it take CUDA 12 regardless. Per load, `cudaPayload` in
`llm/engine/policy.go` decides:

| The load's CUDA devices | Served by |
|---|---|
| all covered by the `cuda` component's `sass` (7.5-9.0, 12.x on the current pin) | opencoti, CUDA 13 library |
| all covered only by the `cuda12` component's `sass` (5.2-7.0 on the current pin, untested; 7.0 on b208) | opencoti started with `OPENCOTI_CUDA_LEGACY=1` (`LegacyCUDA`), CUDA 12 library |
| some of each | llama.cpp, with the reason logged (one library per process) |
| any other capability | llama.cpp, as before |

An explicit `XOLLAMA_ENGINE_PATH` is used as given, and that engine makes its
own pick. Guards: `llm/engine/pin_cuda_test.go`. Measured on solidPC
(2026-10-04, RTX 3090, engine `2610040837001`): with both libraries beside
it the engine loads `ggml-cuda-x86_64.so`, and with `OPENCOTI_CUDA_LEGACY=1`
it loads `ggml-cuda-cu12-x86_64.so` ("CUDA 12 legacy library loaded"). Until
engine `2610040837001` the CUDA 12 library was staged as `ggml-cuda.so` beside
a second copy of the engine in `lib/ollama/engines/cuda_v12`; the fetch removes
that directory.

Discovery asks the same libraries. The CUDA listing that replaces llama.cpp's
is the engine's own pick and, when the CUDA 12 library is staged beside it,
a second listing with `OPENCOTI_CUDA_LEGACY=1`; the longer list wins, and the
free-memory refresh then asks only for that one
(`discover/opencoti_cuda12.go`). Before
this, a V100 with a 570-series driver was listed by neither the CUDA 13
payload (driver 580, no Volta) nor anything else, and was dropped as "a
device the engine that serves it does not list". Guard:
`TestAV100ListedOnlyByTheCUDA12PayloadIsKept`.

The refresh before a load lists every backend opencoti serves **side by side**,
inside upstream's single 3 s budget (`forkRefresh`,
`discover/refresh_opencoti.go`). Each listing starts the engine and
initialises its devices: on eleven2go CUDA takes 1.1 to 1.7 s and Vulkan, with
three GPUs, 1.4 to 1.9 s (2026-10-08). One after the other they often did not
fit; the second was killed at the deadline, which on Windows reads "exit
status 1" with the engine's output cut after its device lines, and the
refresh left free memory stale for 15 minutes. A listing that runs out of the
budget says so in its Warn ("the refresh budget ran out"). Guards:
`TestTheBackendsAreListedSideBySide`, `TestARefreshThatRanOutOfTimeSaysSo`.
A listing cut because its *caller* stopped waiting is not the engine's fault:
the wait for VRAM recovery after an unload calls the refresh in a loop under
its own deadline, which on eleven2go ended 0.3 s into one and put both
backends into a 15-minute cooldown. A cut with less time left than a listing
needs (`slowListing`, 2.5 s; the slowest measured is 1.9 s) is logged at Debug
and starts no cooldown. Guard: `TestARefreshItsCallerCutShortIsNotTheEngines`.

Coverage is **derived from the `dso` rows themselves**: a bare `x86_64` label is
CUDA, and `<arch>-vulkan` names Vulkan for that arch. Explicit `accel` rows are
still honoured and only ever add to it. That matters because the published pin
for `2609242056001` states no `accel` rows at all — it lists its payload set in
a header comment — and keying on those rows alone would have made the pin
accelerate nothing while the engine sat there holding a working Vulkan payload.

## One GPU, two backends

A 3090 is both a CUDA and a Vulkan device. Upstream's discovery keeps one
entry per GPU and drops the Vulkan one in favour of CUDA or ROCm, so nothing
after it could choose Vulkan for that card: neither the server's GPU policy
(`xollama tweak server gpu <pci> --backend Vulkan`) nor a model pinned to
Vulkan, and one model could not be split over a 3090 and an RX 9070 XT, which
only Vulkan reaches together (`backend-copies` hook,
`discover/backend_copies.go`, `server/backend_copies.go`).

| | |
|---|---|
| discovery | keeps the Vulkan copy of a discrete GPU that CUDA or ROCm also serves, **only** when both entries carry the same PCI ID, and only while opencoti may serve |
| `discover.GPUDevices` | unchanged: one entry per GPU, upstream's choice. Its callers count GPUs and add up their memory (the default context is sized from the sum at startup; a copy would double the 3090) |
| `/api/xollama/devices`, the link probe | every copy (`GPUDevicesAllBackends`): the menus offer each backend a GPU is reachable through |
| a load | gets the copies only when its pin or the `gpu` policy names a backend; `applyGPUPolicy` then keeps one entry per GPU: the chosen backend, else upstream's CUDA/ROCm preference. A copy shares the free memory the scheduler accounted for the card |
| identity | the engine prints each device's PCI ID with `OPENCOTI_LIST_DEVICE_IDS=1` (NVIDIA: the id is the PCI address; AMD on Windows: `id=uuid:… pci=…`). It fills a PCI ID llama.cpp left empty (the RX 9070 XT and the iGPU on Windows) and keeps opencoti's Vulkan listing of a CUDA card as its copy where llama.cpp's Vulkan did not list it |
| `XOLLAMA_ENGINE=llamacpp` | nothing kept; the dedup is upstream's |

A model pinned to Vulkan without device ids now includes the 3090's Vulkan
entry among its candidates: the card is a Vulkan device too.

Measured on eleven2go (2026-10-08, side server from this tree, `qwen3:8b`,
512 tokens): nothing chosen, CUDA0 123.3 tok/s (unchanged); pinned to Vulkan
by the 3090's PCI ID, Vulkan1 106.2; the policy choosing Vulkan for the
3090, unpinned, Vulkan1 106.2; with `--split spread` one model over the
RX 9070 XT and the 3090, both Vulkan, 92.7. Startup still counts three GPUs
(39.9 GiB, default context 32768); the device route lists the 3090 twice and
the 9070 XT and the iGPU with their PCI IDs. Guards:
`discover/backend_copies_test.go`, `server/backend_copies_test.go`, each
verified by removal.

## A model only opencoti can serve

A model that states a KV cache type stock llama.cpp does not know (`kv.k` /
`kv.v`, such as `kvarn3`) is placed only on the GPUs opencoti serves. Without
this, a host with GPUs of two kinds -- an RTX 3090 served by opencoti and an
RX 9070 XT that, on a pin without a Windows Vulkan payload, only llama.cpp
serves -- could place it on the card with more free memory, and the launch then
refused it ("this KV cache configuration needs the opencoti engine"). A
server-wide `XOLLAMA_K_CACHE_TYPE` does not count: on llama.cpp it falls back
to the legacy type. `server/placement_opencoti.go`, registry row
`opencoti-placement`.

The list is filtered in `processPending`, right after the device pin, so the
whole load sees it, not only the placement. `slots.live` and the
single-sequence deny-list ask whether opencoti serves the same list. Filtered
only at the placement call (b22e5649), they saw both cards, answered no, and
the council model launched with one slot's context (`-c 196608`, not
`num_ctx × slots.live` = 393216). Its conversation's owner then booked the
whole window, and the builder was refused for two minutes (eleven2go,
2026-09-29).
