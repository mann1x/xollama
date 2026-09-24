# Choosing the device a model runs on

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
backends are unaffected.

<Warning>
  **An integrated GPU reports host RAM, not VRAM.** Discovery reports the
  Renoir iGPU as `total=63.1 GiB` / `available=63.0 GiB`, because its memory
  *is* system memory. Nothing stops the scheduler believing that figure, and
  `llm/llama_server.go` special-cases integrated devices only for CUDA and
  ROCm — not Vulkan. Size a model for the iGPU deliberately; do not read 63 GiB
  as headroom.
</Warning>

## What the engine is told

`llm/engine/opencoti.go`'s `gpuFlag` maps the devices the scheduler chose onto
the artifact's own selector, so a Vulkan placement launches the engine with
`--gpu vulkan`. `discover/runner.go` enumerates through the llama.cpp payload,
which therefore needs its `vulkan` backend present for a Vulkan device to be
seen at all (`OLLAMA_VULKAN`, on by default, gates it).

## The engine must carry a payload for the backend

Routing is the tested matrix intersected with what the pinned artifact actually
ships — see `pinUncovered`. A dev snapshot is a bare APE that accelerates only
what its `dso` rows provide, so Vulkan reached opencoti for the first time with
snapshot `2609242056001`, the first to publish `ggml-vulkan-x86_64.so`.

Coverage is **derived from the `dso` rows themselves**: a bare `x86_64` label is
CUDA, and `<arch>-vulkan` names Vulkan for that arch. Explicit `accel` rows are
still honoured and only ever add to it. That matters because the published pin
for `2609242056001` states no `accel` rows at all — it lists its payload set in
a header comment — and keying on those rows alone would have made the pin
accelerate nothing while the engine sat there holding a working Vulkan payload.
