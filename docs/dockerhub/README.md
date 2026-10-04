<!-- short: Soft fork of Ollama with the opencoti engine, per-model settings and dynamic slots. CUDA, amd64 and arm64 -->

# xollama

**xollama** is a soft fork of [Ollama](https://github.com/ollama/ollama). It
carries fixes upstream has not merged yet (a thinking-token budget,
`/api/tokenize`, parser and tool-call fixes), serves GGUF models on the
[opencoti-llamafile](https://huggingface.co/ManniX-ITA/opencoti-llamafile)
engine where that engine is tested, and lets a model carry its own settings:
KV cache types, concurrent slots, engine, device.

Same API, same clients, same models as Ollama. Two differences to know before
you start: it listens on **22434**, and its listen address is set with
**`XOLLAMA_HOST`**, never `OLLAMA_HOST`.

- Source: https://github.com/mann1x/xollama
- Full Docker guide: [docs/xollama/docker.mdx](https://github.com/mann1x/xollama/blob/main/docs/xollama/docker.mdx)
- Also published as `ghcr.io/mann1x/xollama`, with the same tags.

xollama is not affiliated with or endorsed by Ollama.

## Supported tags

| Tag | Meaning |
|---|---|
| `latest` | The newest full release. Moves only when a release is promoted. |
| `dev` | The newest pre-release, or the newest build of the `dev` branch. Moves often. |
| `<version>` | One build, never moved. A release looks like `0.34.4-xollama.1`; a branch build like `0.34.4-dev.86de2b7d` (upstream version, `dev`, commit). |
| `<version>-amd64`, `<version>-arm64` | One architecture of that build. `<version>`, `dev` and `latest` are a manifest over both: Docker picks yours. |

A build moves `latest` or `dev`, never both. If `latest` does not exist yet, no
full release has been published as an image: use `dev` or a version tag.

**Architectures:** `linux/amd64` and `linux/arm64`. arm64 starts with the 0.35.1 builds: `dev` has it now, `latest` gets it with the first 0.35.1 release. On arm64 the image carries CUDA 12, CUDA 13 and JetPack 5/6 payloads for NVIDIA hardware and runs on the CPU everywhere else, a Raspberry Pi 5 included (about 10 tok/s on a 1.5B model). No ROCm build.

## What is inside

- `/usr/bin/xollama` — server and CLI. It is the entrypoint; the default command is `serve`.
- `/usr/lib/ollama` — stock `llama.cpp` (`llama-server`) with upstream Ollama's
  CUDA v12, CUDA v13 and Vulkan backends, and the opencoti engine with a CUDA 13
  payload plus a CUDA 12 payload for Volta cards.
- `/usr/lib/ollama/PAYLOAD` — which bytes this image carries: xollama version,
  runtime release and sha256, upstream tarballs, engine tag and revision.
- Base: `ubuntu:24.04`. Runs as `root`. Exposes `22434`.

Nothing native is compiled when the image is built: every piece is a published
artifact pinned by sha256 in the repository, and CI checks that the server
answers on the published port before pushing.

## Quick start

### docker run

With an NVIDIA GPU ([set up GPU passthrough](#nvidia) first):

```shell
docker run -d --gpus=all -v xollama:/root/.ollama -p 22434:22434 --name xollama mannixita/xollama
```

CPU only:

```shell
docker run -d -v xollama:/root/.ollama -p 22434:22434 --name xollama mannixita/xollama
```

Until the first full release moves `latest`, add `:dev` to the image name.
Add `--restart unless-stopped` to keep it running across reboots. Then:

```shell
curl http://localhost:22434/api/version
docker exec -it xollama xollama run qwen3.6
```


### docker compose

```yaml
services:
  xollama:
    image: mannixita/xollama:latest
    container_name: xollama
    restart: unless-stopped
    ports:
      - "22434:22434"
    volumes:
      - xollama:/root/.ollama
    environment:
      OLLAMA_KEEP_ALIVE: 10m
      # XOLLAMA_API_KEY: change-me-to-a-long-random-key
      # XOLLAMA_PARALLEL: 2
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]

volumes:
  xollama:
```

```shell
docker compose up -d
docker compose exec xollama xollama run qwen3.6
```

## Parameters

### Ports

| Parameter | Function |
|---|---|
| `-p 22434:22434` | The Ollama-compatible API, including the OpenAI routes under `/v1` and the Anthropic route `/v1/messages`. |
| `-p 127.0.0.1:22434:22434` | The same, reachable from this machine only. |
| `-p 11434:22434` | The same, on the host's 11434, for a client that insists on Ollama's port. Only if no Ollama is using it. |

Inside the container the server binds `XOLLAMA_HOST=0.0.0.0:22434`. To move
it, set `-e XOLLAMA_HOST=0.0.0.0:<port>` and publish that port. Setting
`OLLAMA_HOST` does nothing: it is not read for the listen address.

### Volumes

| Parameter | Function |
|---|---|
| `-v xollama:/root/.ollama` | The server's home: models (`/root/.ollama/models`), the server's identity key, and the stored digest of a local API key. Mount the whole directory. |
| `-v /path/on/host:/root/.ollama` | The same in a host directory, so the models are visible from the host. |

**File ownership.** The container runs as root, but files it creates in the
store are handed to the owner (numeric uid:gid) of the directory they go into,
when that owner is not root. Mount a directory your account owns and the
models it pulls belong to your account on the host.

**Do not share a store with a running Ollama.** One model store with two
writers and one GPU with two schedulers is not supported, whichever ports and
containers they use. Give xollama its own directory, or stop the other server.

### Environment variables

Every `OLLAMA_` variable can also be written `XOLLAMA_`, and that spelling wins
when both are set. The listen address is the exception: `XOLLAMA_HOST` only.

**Server**

| Variable | Default | Function |
|---|---|---|
| `XOLLAMA_HOST` | `0.0.0.0:22434` | Listen address. |
| `OLLAMA_MODELS` | `/root/.ollama/models` | Model store. |
| `OLLAMA_KEEP_ALIVE` | `5m` | How long a model stays loaded after its last request; negative keeps it loaded. |
| `OLLAMA_MAX_LOADED_MODELS` | 3 per GPU (3 on CPU) | Models loaded at once. |
| `OLLAMA_MAX_QUEUE` | `512` | Requests that may wait before the server answers busy. |
| `OLLAMA_LOAD_TIMEOUT` | `5m` | How long a load may stall before it is given up. |
| `OLLAMA_CONTEXT_LENGTH` | 4k / 32k / 256k by VRAM | Context for a model with no `num_ctx` of its own (32k with 24 GB, 256k with 48 GB or more). |
| `OLLAMA_FLASH_ATTENTION` | on where supported | `1` forces on, `0` off. Needed for a quantised KV cache. |
| `OLLAMA_ORIGINS` | local origins | Extra browser origins allowed, comma separated. |
| `OLLAMA_DEBUG` | `0` | `1` debug logging, `2` trace. |
| `OLLAMA_GPU_OVERHEAD` | `0` | VRAM to leave unused per GPU, in bytes. |
| `CUDA_VISIBLE_DEVICES` | all | NVIDIA GPUs the server may use, within those Docker passed in. |

**Security**

| Variable | Default | Function |
|---|---|---|
| `XOLLAMA_API_KEY` | none | Local API key. When set, every endpoint requires it as `Authorization: Bearer <key>` or `x-api-key: <key>`. |

The key guards connections **to this server** only. It is not an ollama.com key,
and `OLLAMA_API_KEY` is not read for it. Over plain HTTP it travels in clear
text, so put a TLS reverse proxy in front of any port other machines use. The
CLI inside the container sends the same variable, so `docker exec` keeps
working.

**Engine**

| Variable | Default | Function |
|---|---|---|
| `XOLLAMA_ENGINE` | `auto` | `auto` routes each load by the tested device matrix; `opencoti` forces the opencoti engine; `llamacpp` forces stock `llama.cpp`, identical to upstream. |
| `XOLLAMA_ENGINE_FALLBACK` | off | Retry a failed opencoti load once on stock `llama.cpp` instead of failing. |
| `XOLLAMA_ENGINE_PATH` | unset | An exact engine artifact to use instead of the bundled one. |
| `XOLLAMA_ENGINE_ARGS` | unset | Extra arguments appended last to the engine command line. Flags that size memory, name the model or port, or change logging are refused. |

**KV cache**

| Variable | Default | Function |
|---|---|---|
| `OLLAMA_KV_CACHE_TYPE` | `f16` | Cache type for keys and values. |
| `XOLLAMA_K_CACHE_TYPE` | as above | Keys only. |
| `XOLLAMA_V_CACHE_TYPE` | as above | Values only. |
| `XOLLAMA_K_CACHE_TYPE_SWA` / `XOLLAMA_V_CACHE_TYPE_SWA` | unset | A sliding-window model's short-window cache (the ring). Set both; needs KVarN on both `K` and `V`. |

**Concurrent requests**

| Variable | Default | Function |
|---|---|---|
| `XOLLAMA_PARALLEL` | `1` | Slots a model loads with on the opencoti engine. |
| `XOLLAMA_MAX_PARALLEL` | `4` | Most requests running at once per model on opencoti. |
| `XOLLAMA_DYNAMIC_SLOTS` | on | Slots grow with demand instead of being reserved; `0` restores a fixed split. |
| `XOLLAMA_SLOTS_TPS_FLOOR` | unset | Per-request decode rate (tokens/s) below which no further slot is admitted. |
| `XOLLAMA_SLOTS_VRAM_RESERVE` | engine default | Free VRAM (MiB) to keep before admitting another slot. |
| `OLLAMA_NUM_PARALLEL` | `1` | Slots per model on **stock `llama.cpp` only**. Not read on opencoti. |

**Council**

| Variable | Default | Function |
|---|---|---|
| `XOLLAMA_COUNCIL_HOSTS` | none | Servers a council role may be sent to: host or host:port, comma separated, `*` for any. |

At startup the server logs every variable with its current value, in the
`server config` line of `docker logs xollama`.

## GPU support

### NVIDIA

A container sees an NVIDIA GPU only when Docker passes it in. On the host:

1. **The NVIDIA driver**, working: `nvidia-smi` lists the GPUs. 580 or newer
   for the CUDA 13 payload, 570 or newer for Volta's CUDA 12 payload.
2. **The [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)**:
   add NVIDIA's package repository as that guide describes, then
   `sudo apt-get install -y nvidia-container-toolkit` (Debian/Ubuntu) or
   `sudo dnf install -y nvidia-container-toolkit` (Fedora/RHEL family).
3. **Docker configured for it**:

   ```shell
   sudo nvidia-ctk runtime configure --runtime=docker
   sudo systemctl restart docker
   ```

Check it; this must list your GPUs:

```shell
docker run --rm --gpus=all ubuntu nvidia-smi
```

Then start xollama with `--gpus=all` (or `--gpus '"device=0,1"'` for some of
them). The image sets `NVIDIA_VISIBLE_DEVICES=all` and
`NVIDIA_DRIVER_CAPABILITIES=compute,utility`. Docker Desktop on Windows (WSL 2)
needs only the NVIDIA Windows driver.

`could not select device driver "" with capabilities: [[gpu]]` means step 2 or
3 is missing.

Which engine serves a load depends on the card. With the currently pinned
engine (opencoti build `2610031615001`):

| The load's GPUs | Served by |
|---|---|
| all compute capability 7.5–9.0 or 12.x (RTX 20xx to 50xx, A-series, L40, H100…) | opencoti, CUDA 13 payload |
| all compute capability 5.2–7.0 (Maxwell, Pascal, Tesla V100, Titan V) | opencoti, CUDA 12 payload: legacy, shipped without a run on such a card. `XOLLAMA_ENGINE=llamacpp` puts them on stock `llama.cpp` |
| a mix of the two groups above | stock `llama.cpp` |
| any other card (10.x) | stock `llama.cpp`, on upstream's CUDA backends |

The engine chosen for each load, and why, is in `docker logs xollama`.

### Vulkan

Upstream's Vulkan backend and the Vulkan loader are in the image, as in
upstream's own image; pass the devices with `--device /dev/dri`.
`OLLAMA_VULKAN=0` turns Vulkan off and `GGML_VK_VISIBLE_DEVICES` selects
devices. The pinned engine carries its own Vulkan payload, so a Vulkan load is
served by opencoti; `XOLLAMA_ENGINE=llamacpp` keeps it on upstream's backend.

## Configuring slots and KV cache

On the opencoti engine a model's KV cache is **one pool**: `num_ctx` × the
slots it loads with. Those slots are the model's own `slots.live` if it sets
one, else `XOLLAMA_PARALLEL`, else 1. `OLLAMA_NUM_PARALLEL` is **not read** on
opencoti. With nothing set, a model with no context of its own on a 24 GB card
loads a 32k × 1 pool.

A request that asks for no particular window is guaranteed the whole pool, so
by default requests to one model take turns, as on Ollama. There are two ways
to run them side by side:

- **Clients book windows.** A `/api/chat` request with `placement.num_ctx` gets
  that window, and up to `XOLLAMA_MAX_PARALLEL` of them share the pool while it
  has room. A model loaded with `num_ctx 262144` serves one 256k conversation,
  two of 128k or four of 64k from the same load. The `X-Context-Window` response
  header says what was granted.
- **The operator sizes the pool.** `XOLLAMA_PARALLEL=4` allocates four contexts
  at load, and four requests that ask for no window each get a whole one. That
  is Ollama's `OLLAMA_NUM_PARALLEL=4`, with the same memory cost.

The OpenAI and Anthropic routes carry no `placement`, so their requests take
the whole pool unless it is sized with `XOLLAMA_PARALLEL`. Do not vary
`options.num_ctx` per request: it sizes the load, and a new value reloads the
model.

Example — a 64k × 2 pool with a `q8_0` cache:

```shell
docker run -d --name xollama --gpus all -p 22434:22434 \
  -v xollama:/root/.ollama \
  -e XOLLAMA_PARALLEL=2 \
  -e OLLAMA_CONTEXT_LENGTH=65536 \
  -e OLLAMA_FLASH_ATTENTION=1 \
  -e OLLAMA_KV_CACHE_TYPE=q8_0 \
  mannixita/xollama:latest
```

### KV cache types

Four settings: keys (`XOLLAMA_K_CACHE_TYPE`), values (`XOLLAMA_V_CACHE_TYPE`)
and, for a sliding-window model such as Gemma-4, the two halves of its
short-window ring (`XOLLAMA_K_CACHE_TYPE_SWA`, `XOLLAMA_V_CACHE_TYPE_SWA`).
`OLLAMA_KV_CACHE_TYPE` sets keys and values together.

**Presets** (keys / values):

| Preset | Dense model | Sliding-window model |
|---|---|---|
| Quality | `q8_0` / `q8_0` (`f16` / `f16` if memory allows) | `q8_0` / `q8_0` |
| Balanced | `q8_0` / `q4_0` | `kvarn4` / `kvarn4`, ring `q8_0` / `q8_0` |
| Max context | `kvarn3` / `kvarn3` or `kvarn2` / `kvarn2` | `kvarn3` / `kvarn3`, ring `q4_0` / `q4_0` |

On a **V100** start with `q8_0` / `q8_0`.

**Values**

| Type | Keys / values | Ring | Flash attention | Notes |
|---|---|---|---|---|
| `f16` `f32` `bf16` | yes | yes | not needed | `bf16` on Vulkan only as `bf16` / `bf16` |
| `q8_0` `q5_1` `q5_0` `q4_1` `q4_0` | yes | yes | needed for values | |
| `q6_0` | yes | yes | needed for values | some mixed pairs are slower on CUDA, see below |
| `iq4_nl` | yes | yes | needed for values | no GPU kernel as values; CPU only |
| `kvarn2` `kvarn3` `kvarn4` `kvarn5` `kvarn6` `kvarn8` | yes | yes | forced on | the engine's compressed cache; there is no `kvarn7` |

**Combinations**

- Two plain types may differ (`q8_0` / `q4_0`); two `kvarn` widths may differ
  (`kvarn4` / `kvarn3`).
- Do **not** mix a `kvarn` width with a plain type: the engine silently
  promotes the plain half to the same `kvarn` width.
- A ring needs `kvarn` on **both** keys and values. Set both ring halves, both
  `kvarn` or both plain. They may differ from each other (`q8_0` / `q4_0`).
- Quantised values need flash attention: set `OLLAMA_FLASH_ATTENTION=1`.
- `q6_0` mixed with another type runs, but some pairs (`q4_0`/`q6_0`,
  `q6_0`/`q8_0`, `q6_0`/`q4_1`) take a slower path on CUDA. Prefer
  `q6_0` / `q6_0`.
- Put `kvarn` and `q6_0` in the `XOLLAMA_*` variables. Keep
  `OLLAMA_KV_CACHE_TYPE` to `f16`, `q8_0` or `q4_0`: it is the fallback when
  stock llama.cpp serves a device the engine does not cover.

Example, balanced for a sliding-window model:

```shell
docker run -d --name xollama --gpus all -p 22434:22434 \
  -v xollama:/root/.ollama \
  -e OLLAMA_FLASH_ATTENTION=1 \
  -e OLLAMA_KV_CACHE_TYPE=q8_0 \
  -e XOLLAMA_K_CACHE_TYPE=kvarn4 -e XOLLAMA_V_CACHE_TYPE=kvarn4 \
  -e XOLLAMA_K_CACHE_TYPE_SWA=q8_0 -e XOLLAMA_V_CACHE_TYPE_SWA=q8_0 \
  mannixita/xollama:dev
```

All the rules:
[KV cache types — values and combinations](https://github.com/mann1x/xollama/blob/main/docs/xollama/kv-cache.mdx#values-and-the-combinations-that-work).

The environment sets server-wide defaults. A model can carry its own KV cache
types, slots, engine and device pin, which win over the environment, set with
`xollama tweak model` inside the container:

```shell
docker exec -it xollama xollama tweak model qwen3.6
docker exec -it xollama xollama tweak model qwen3.6 --kv-k=q8_0 --kv-v=q8_0
docker exec -it xollama xollama tweak model qwen3.6 --slots-live=2
```

The settings are stored in the model, in the volume. Details:
[slots](https://github.com/mann1x/xollama/blob/main/docs/xollama/slots.mdx) ·
[KV cache types](https://github.com/mann1x/xollama/blob/main/docs/xollama/kv-cache.mdx) ·
[tweak](https://github.com/mann1x/xollama/blob/main/docs/xollama/tweak.mdx).

## Updating

Models and settings live in the volume, so updating is pull and recreate:

```shell
docker pull mannixita/xollama:latest
docker rm -f xollama
docker run -d --name xollama --gpus all -p 22434:22434 \
  -v xollama:/root/.ollama --restart unless-stopped mannixita/xollama:latest
```

With Compose: `docker compose pull && docker compose up -d`.

To stay on one build, use a `<version>` tag. `curl http://localhost:22434/api/xollama`
reports the version a running server was built as.

## Where the image comes from

The image is assembled by the
[`docker-release`](https://github.com/mann1x/xollama/blob/main/.github/workflows/docker-release.yaml)
workflow on GitHub-hosted runners, from
[`Dockerfile.xollama`](https://github.com/mann1x/xollama/blob/main/Dockerfile.xollama)
and [`scripts/docker-assemble.sh`](https://github.com/mann1x/xollama/blob/main/scripts/docker-assemble.sh):

| Layer | Source |
|---|---|
| `llama.cpp` CPU runtime | the fork's own runtime release, sha256-pinned |
| CUDA v12, CUDA v13, Vulkan backends | upstream Ollama's release tarballs, sha256-pinned |
| opencoti engine and its CUDA 12 payload | Hugging Face, sha256-pinned |
| `xollama` | a Go build of the same commit (AlmaLinux 8, glibc 2.28) |

To see what an image holds:

```shell
docker run --rm --entrypoint cat mannixita/xollama:latest /usr/lib/ollama/PAYLOAD
```

Details: [docs/features/docker-release.md](https://github.com/mann1x/xollama/blob/main/docs/features/docker-release.md).

## Support and links

- Source and issues: https://github.com/mann1x/xollama
- Documentation: https://github.com/mann1x/xollama/tree/main/docs/xollama
- The engine: https://huggingface.co/ManniX-ITA/opencoti-llamafile
- Upstream Ollama: https://github.com/ollama/ollama

Please report problems with this image to the xollama repository, not to
Ollama.

## License

MIT, unchanged from upstream Ollama — see
[LICENSE](https://github.com/mann1x/xollama/blob/main/LICENSE). Copyright (c)
Ollama, plus xollama's contributors for the changes in the fork. The bundled
engine and libraries carry their own licences; the Go dependencies' licences
are in `/usr/lib/ollama/GO_LICENSE`.
