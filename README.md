# xollama

A soft fork of [ollama](https://github.com/ollama/ollama) that carries the
fixes upstream has not merged, and serves GGUF models on the
[opencoti-llamafile](https://huggingface.co/ManniX-ITA/opencoti-llamafile)
engine.

> **Status.** The current release is
> [`v0.40.10-xollama`](https://github.com/mann1x/xollama/releases/latest):
> upstream ollama `v0.40.10` with the carried patches
> ([`docs/protocols/CARRIED-PATCHES.md`](docs/protocols/CARRIED-PATCHES.md))
> and the opencoti c13 engine (`llm/engine/pin/`). `main` is the release,
> `dev` is where work lands. With `XOLLAMA_ENGINE=llamacpp` and no xollama
> flags the launch is byte-identical to upstream, which is what keeps an A/B
> honest. Measurements are in [`docs/evaluations/`](docs/evaluations/) and the
> designs in [`docs/features/`](docs/features/).

## Install

| Platform | Download from [Releases](https://github.com/mann1x/xollama/releases/latest) |
|---|---|
| Windows | `xOllamaSetup.exe` (unsigned: SmartScreen warns on first run; check it against `sha256sum.txt`) |
| macOS, Apple silicon | `xOllama.dmg` (signed and notarized) |
| Linux, with a runtime | the [Docker image](#docker), amd64 and arm64 |
| Linux, binary only | `xollama-linux-amd64`, `xollama-linux-arm64` (glibc 2.28 or newer; no runtime, for a host that already has one) |

The server listens on **22434**. Then:

```sh
xollama run qwen3.6
```

## What it adds

| | |
|---|---|
| **Images, speech, transcription and video** | Media models on OpenAI's routes, alone or beside a chat model: [`docs/xollama/media.mdx`](docs/xollama/media.mdx) |
| **Council chat** | One model name answered by a planner, researchers, critics and a synthesizer: [`docs/xollama/council.mdx`](docs/xollama/council.mdx) |
| **Think budget** | A bound on how long a reasoning model thinks: [`docs/xollama/think-budget.mdx`](docs/xollama/think-budget.mdx) |
| **Drafters** | Speculative decoding with a model's own draft head or a Gemma 4 assistant drafter: [`docs/xollama/gemma4-drafter.mdx`](docs/xollama/gemma4-drafter.mdx) |
| **Ternary Bonsai 2 27B** | A 27B model in 6 to 8.6 GB that stock Ollama cannot load: [`docs/xollama/bonsai.mdx`](docs/xollama/bonsai.mdx) |
| **Tool-call progress** | A streamed chat reports the tool call a model is still writing: [`docs/xollama/tool-call-progress.mdx`](docs/xollama/tool-call-progress.mdx) |
| **`/api/tokenize`** | Token counts from the model's own tokenizer: [`docs/xollama/tokenize.mdx`](docs/xollama/tokenize.mdx) |
| **Per-model settings** | `xollama tweak model`: [`docs/xollama/tweak.mdx`](docs/xollama/tweak.mdx), every flag in [`docs/xollama/tweak-reference.mdx`](docs/xollama/tweak-reference.mdx) |

Everything the fork adds is documented under [`docs/xollama/`](docs/xollama/),
starting at [`docs/xollama/index.mdx`](docs/xollama/index.mdx).

## Image generation, speech and video

```sh
xollama pull mannix/flux2-klein:4b
curl -s http://localhost:22434/v1/images/generations \
  -d '{"model": "mannix/flux2-klein:4b", "prompt": "a lighthouse at dusk"}' \
  | python3 -c 'import sys,json,base64; open("lighthouse.png","wb").write(base64.b64decode(json.load(sys.stdin)["data"][0]["b64_json"]))'
```

Any OpenAI client works with the base URL `http://localhost:22434/v1`. The
same goes for speech (`/v1/audio/speech`), transcription
(`/v1/audio/transcriptions`), image edits (`/v1/images/edits`) and video
(`/v1/videos`):

| To get | Run |
|---|---|
| An image model | `xollama pull mannix/flux2-klein:4b` or `mannix/z-image:turbo` |
| Transcription | `xollama pull mannix/whisper:large-v3-turbo` |
| A voice | `xollama pull mannix/kokoro:82m` (also `mannix/supertonic:3`, `mannix/kittentts:mini-0.8`, `mannix/outetts:0.3`) |
| Video | `xollama pull mannix/wan2.1:t2v-1.3b` or `mannix/wan2.2:ti2v-5b` (a 24 GB card) |
| A chat model that also draws and speaks | `xollama media create flux2-klein-4b --to qwen3:8b`, then `xollama media create kokoro-82m --to qwen3:8b` |
| Images, transcription and speech under one name | `xollama media create media-kit kit` |

The step-by-step guide, with an example of each kind and how to use your own
files, is [`docs/xollama/media.mdx`](docs/xollama/media.mdx).

## Models

Models made for xOllama are in the
[mannix library on ollama.com](https://ollama.com/mannix):

- **Media**: [flux2-klein](https://ollama.com/mannix/flux2-klein),
  [z-image](https://ollama.com/mannix/z-image),
  [whisper](https://ollama.com/mannix/whisper),
  [kokoro](https://ollama.com/mannix/kokoro),
  [supertonic](https://ollama.com/mannix/supertonic),
  [kittentts](https://ollama.com/mannix/kittentts),
  [outetts](https://ollama.com/mannix/outetts),
  [wan2.1](https://ollama.com/mannix/wan2.1),
  [wan2.2](https://ollama.com/mannix/wan2.2).
- **[Ternary Bonsai 2 27B](https://ollama.com/mannix/Bonsai-2-27B)**:
  `xollama run mannix/bonsai-2-27b`. PrismML's ternary model, 5.95 GB for the
  language weights, with an optional draft head (1.25× to 1.5× measured) and
  vision tower. It needs `v0.40.3-xollama` or later and does not run on stock
  Ollama. See [`docs/xollama/bonsai.mdx`](docs/xollama/bonsai.mdx).
- **Gemma 4 drafters**: one per size on Hugging Face
  (`ManniX-ITA/gemma-4-<E2B|E4B|12B|26B-A4B|31B>-it-assistant-GGUF`).
  `xollama tweak model MODEL --drafter=auto` attaches the right one to any
  Gemma 4 model, fine-tunes and merges included: 93 to 95 tok/s became 136 to
  141 on coding prompts on an RTX 3090. See
  [`docs/xollama/gemma4-drafter.mdx`](docs/xollama/gemma4-drafter.mdx).
- **Coding models and merges**:
  [gemma4-98e-v7-coder](https://ollama.com/mannix/gemma4-98e-v7-coder),
  [omnimerge-v6](https://ollama.com/mannix/omnimerge-v6),
  [ornith-1.5-27b-a3b-coderx](https://ollama.com/mannix/ornith-1.5-27b-a3b-coderx),
  [qwen3.6-27b-a3b-coderx](https://ollama.com/mannix/qwen3.6-27b-a3b-coderx),
  [JackOD-9B-Coder](https://ollama.com/mannix/JackOD-9B-Coder).

The list with what each is: [`docs/xollama/models.mdx`](docs/xollama/models.mdx).

## Why

Good fixes sit in upstream PRs for months. Some of them are not cosmetic:

- **[#17566](https://github.com/ollama/ollama/pull/17566) — a thinking token
  budget.** Open since 2026-08-04. Without it there is no way to bound a
  reasoning model, and on a long agentic run the difference is a turn that
  finishes versus one that grinds to the token cap.
- **`/api/tokenize` and `/api/detokenize`.** ollama exposes no way to count
  tokens against the model actually loaded, so every client guesses. The
  oldest PR for it ([#12030](https://github.com/ollama/ollama/pull/12030)) has
  been open since 2025-08-22 — over a year — under a queue of "merge please"
  comments. The plumbing already exists internally; only the routes are
  missing.
- More parser and tool-call correctness fixes, listed in
  [`docs/protocols/CARRIED-PATCHES.md`](docs/protocols/CARRIED-PATCHES.md).

xollama is where those live in the meantime. Every one of them stays an open
PR upstream, and each is **retired from this fork the day upstream takes it**.

## Drop-in

Same API, same models directory, same clients. The binary is renamed and the
listen address moves to **22434** so an accidental collision with a stock
install is not possible — which is not the same as supporting the two side by
side: one model store with two writers and one GPU with two schedulers are
still one of each. See [`docs/features/rebrand.md`](docs/features/rebrand.md)
and [`docs/xollama/default-port.mdx`](docs/xollama/default-port.mdx).

## Docker

An image for amd64 and arm64 (a Raspberry Pi 5 runs it on the CPU) with the
CUDA v12, CUDA v13 and Vulkan backends and the opencoti engine is published
to Docker Hub and GHCR. `:latest` is the newest
full release, `:dev` the newest pre-release or `dev` build, and every build
also has its own version tag.

With an NVIDIA GPU:

```sh
docker run -d --gpus=all -v xollama:/root/.ollama -p 22434:22434 --name xollama mannixita/xollama
```

CPU only:

```sh
docker run -d -v xollama:/root/.ollama -p 22434:22434 --name xollama mannixita/xollama
```

The same image is `ghcr.io/mann1x/xollama`.

**NVIDIA GPU passthrough.** `--gpus=all` needs the NVIDIA driver on the host
and the [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
registered with Docker. On Debian or Ubuntu, once NVIDIA's apt repository is
added as that guide describes:

```sh
sudo apt-get install -y nvidia-container-toolkit
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
docker run --rm --gpus=all ubuntu nvidia-smi   # must list your GPUs
```

The container runs as root and keeps its models in `/root/.ollama/models`;
mount `/root/.ollama` (a named volume as above, or a host directory) so they
survive the container. The image listens on `0.0.0.0:22434` via
`XOLLAMA_HOST`; `OLLAMA_HOST` is not read. Then:

```sh
curl http://localhost:22434/api/version
docker exec -it xollama xollama run qwen3.6
```

Ports, volumes, file ownership, GPU routing, every environment variable,
slots and KV cache settings, Compose and updating:
[`docs/xollama/docker.mdx`](docs/xollama/docker.mdx).

## The engine

ollama already runs GGUF models as a `llama-server` subprocess, so the
engine is one binary path and one argv behind an HTTP API. xollama routes
that to opencoti-llamafile — upstream llamafile plus a patch series aimed at
multi-session agentic serving on a fixed VRAM budget: KV quantization tiers,
a rolling-KV window, shared KV pools, DCA long context, MTP speculative
decode.

Routing is by *tested* platform and backend — CUDA, Vulkan and CPU go to
opencoti, as does Metal on Apple silicon; ROCm, Intel Macs and anything
unvalidated stay on stock `llama-server`; MLX models keep ollama's MLX path. `XOLLAMA_ENGINE=opencoti|llamacpp|auto`
overrides it, which also makes an honest A/B possible against vanilla.

Design: [`docs/features/engine-opencoti-llamafile.md`](docs/features/engine-opencoti-llamafile.md).

### AMD Radeon on Windows: use AMD Software 26.9.2 or later for Vulkan

On a Radeon RX 9070 XT under Windows 11, AMD Software 26.8.1 (display driver
32.0.31041.1004) loses the card once a model has been loaded through Vulkan
and left idle: within a minute or two Windows logs a display driver timeout
(live kernel dump 0x141 in `amdkmdag.sys`), the runner dies on its next
request, and the card can stay disabled until a reboot. It is a driver fault,
not xollama's or the engine's: a small standalone Vulkan program with no llama.cpp
code reproduces it by keeping several GiB of host-visible video memory mapped,
which is what llama.cpp's Vulkan backend does by default, and stock
`llama-server` fails the same way.

AMD Software 26.9.2 (display driver 32.0.32015.2008) ran the same tests
clean, so that is the minimum for Vulkan on that card. The driver that matters
is the graphics card's own: the chipset package updates only the integrated
GPU. If you cannot update, `GGML_VK_DISABLE_HOST_VISIBLE_VIDMEM=1` in the
server's environment avoided the fault at a cost of about 6% in tokens per
second. Measured on one machine and one card (2026-10-04); other Radeon cards
on 26.8.1 were not tested.

## Settings that belong to the model

Which cache type a model tolerates, whether flash attention helps or breaks it,
how many conversations it should serve at once — none of that changes when you
move the model to another machine, and all of it changes when you swap the
model. ollama's settings for these are environment variables, which are
server-wide by construction.

xollama gives the model its own say, in a layer a stock ollama carries through
`push` and `pull` and ignores, so a model configured here still runs there:

```sh
xollama tweak model qwen3.6:latest          # walk every setting
xollama tweak model qwen3.6:latest --dca    # just this feature
xollama tweak model qwen3.6:latest --dca=on # no questions at all
```

[`docs/xollama/model-settings.mdx`](docs/xollama/model-settings.mdx) is the
field list, [`docs/xollama/tweak.mdx`](docs/xollama/tweak.mdx) is the command.

## Staying close to upstream

This fork is meant to be re-syncable indefinitely, so it has rules about what
may be changed and where:
[`docs/protocols/UPSTREAM-SYNC.md`](docs/protocols/UPSTREAM-SYNC.md).
`main` carries the full upstream history, so every future
`git merge upstream/main` has a real merge-base.

## Upstream

For what ollama is and how to use it, see
[ollama/ollama](https://github.com/ollama/ollama) and
[ollama.com](https://ollama.com). xollama is not affiliated with or endorsed
by Ollama.

## Licence

MIT, unchanged from upstream — see [`LICENSE`](LICENSE). Copyright (c) Ollama,
plus xollama's contributors for the changes in this fork.
