# Choosing the device a model runs on

## Pinning a model to a backend and devices

A model's `xollama.json` can say where it runs (schema v3):

```json
{ "version": 3, "devices": { "backend": "Vulkan", "ids": ["0000:18:00.0"] } }
```

| field | meaning |
|---|---|
| `backend` | `CUDA`, `Vulkan`, `ROCm` or `CPU`, any case. One backend per model: an engine runs one ggml backend per process. Required when `ids` is set, because the same PCI device is listed under more than one backend — a 3090 is both a CUDA and a Vulkan device. |
| `ids` | PCI IDs (`0000:18:00.0`, or `18:00.0`), the backend's device index, `integrated` or `discrete`. Empty is every device of the backend; several spread the model across them. |

`selectModelDevices` (`server/device_select.go`, `device-select` hook in
`server/sched.go`) narrows the discovered devices to the pin before placement.
**A pin that cannot be met is a refusal, never a fallback**: it names what was
asked, the backends the installation carries and the devices present, and says
so when `OLLAMA_IGPU_ENABLE` is hiding an iGPU. A pin exists to keep a model
off hardware, and quietly serving it elsewhere breaks the one promise it makes.
`num_gpu 0` still means CPU; the pin is not consulted then.

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
what its `dso` rows provide, so Vulkan reached opencoti for the first time with
snapshot `2609242056001`, the first to publish `ggml-vulkan-x86_64.so`.
The current pin, `2610031022001` (b119, rev `a542184f`), carries CUDA for Linux
x86_64 and Windows x86_64; Vulkan loads route to llama.cpp until a snapshot
publishes the Vulkan payload.

A pin can also narrow CUDA by silicon. `cuda-sass 86 120` says the payloads
carry SASS for sm_86 and sm_120f only, so `Pin.CoversCUDA` admits 8.6–8.9 and
12.x and routes 7.5, 8.0, 9.0 and 10.x to llama.cpp, naming why. Without it,
those cards would load on opencoti and run on the CPU. A pin with no
`cuda-sass` row keeps the engine's 7.5 floor alone.

**The CUDA 12 payload.** A pin may also carry a second, CUDA 12 payload for
older cards, on two `#!` lines opencoti's own parsers read as comments:
`#! dso-cuda12 <arch> <path> <sha256>` and `#! cuda12-sass <cc>...`. b208's was
`ggml-cuda-cu12-x86_64.so`, SASS sm_70 only (Tesla V100 / Titan V), driver ≥
570; the current pin (b119) has none, so `Pin.CoversCUDA12` covers nothing and
a 7.0 card goes to llama.cpp. One engine process loads one payload, and the engine takes the
`ggml-cuda` library beside its own executable, so the CUDA 12 payload is
staged as `ggml-cuda.so` beside a second copy of the engine in
`lib/ollama/engines/cuda_v12` (`scripts/docker-assemble.sh`; Linux image only).
Per load, `cudaPayload` in `llm/engine/policy.go` decides:

| The load's CUDA devices | Served by |
|---|---|
| all covered by `cuda-sass` (8.6–8.9, 12.x on b119) | opencoti, CUDA 13 payload |
| all covered only by `cuda12-sass` (7.0 on b208; nothing on b119) | opencoti from `engines/cuda_v12`, CUDA 12 payload |
| some of each | llama.cpp, with the reason logged (one payload per process) |
| any other capability | llama.cpp, as before |

An explicit `XOLLAMA_ENGINE_PATH` is used as given. Guards:
`llm/engine/pin_cuda12_test.go`.

Discovery asks the same payloads. The CUDA listing that replaces llama.cpp's
runs through the main engine and, when `engines/cuda_v12` holds a second
engine, through that one too; the longer list wins, and the free-memory
refresh then asks only that engine (`discover/opencoti_cuda12.go`). Before
this, a V100 with a 570-series driver was listed by neither the CUDA 13
payload (driver 580, no Volta) nor anything else, and was dropped as "a
device the engine that serves it does not list". Guard:
`TestAV100ListedOnlyByTheCUDA12PayloadIsKept`.

Coverage is **derived from the `dso` rows themselves**: a bare `x86_64` label is
CUDA, and `<arch>-vulkan` names Vulkan for that arch. Explicit `accel` rows are
still honoured and only ever add to it. That matters because the published pin
for `2609242056001` states no `accel` rows at all — it lists its payload set in
a header comment — and keying on those rows alone would have made the pin
accelerate nothing while the engine sat there holding a working Vulkan payload.

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
