# Media endpoints: images, speech, transcription (video later)

**Status:** ACTIVE. Phase 1 (schema v7) and Phase 2 (media-only runner, `hf.co` sourcing, catalog, `xollama media`) closed 2026-10-02. Phase 0 engine measurements wait on the b97 handoff and the M7 build. Phase 3 (routes) next.
**Owner:** xollama; engine work by opencoti.

## 1. What the owner asked for (2026-10-02, condensed)

- opencoti's media engines are done (row M closed, #619, build b97
  `2610021340001`). xollama has to finish the integration.
- xollama should be a **provider for SurfSense**
  (github.com/MODSetter/SurfSense), and **Cerebriline** should use xollama
  for media.
  - Cerebriline uses **image generation now**.
  - Image edit and audio come later on Cerebriline's side, and video once
    opencoti ships it.
- STT, TTS, image generation, image edit and later video are attached either
  **to an existing local model**, or **to a model template** that holds one
  of them or a mix.
- Whether media blobs can be pushed to the ollama registry has to be
  **checked**. Local handling is required either way.
- **Voice models:** all of them are downloaded, since they are small.
  **Images:** FLUX.2 Klein 4B is expected to cover both generation and edit.
- **Video** comes to opencoti in a future release. The design leaves room
  for it.

## 2. What exists today

**opencoti b97: the contract** (handover
`/shared/dev/handover/2026-09-26-opencoti-media-endpoints.md`, design
`opencoti/docs/features/media_engines.md`)

- **Boot modes:** media only (no `-m`), LLM only, or combined.
- **Images** use stable-diffusion.cpp:
  - flags `--diffusion-model|checkpoint|vae|llm|llm-vision|args|device|reserve-mib` (reserve 3072 MiB);
  - `POST /v1/images/generations` (JSON) and `POST /v1/images/edits` (multipart `image[]` + `mask`);
  - the reply is `b64_json` only, as png or jpeg.
- **STT** uses transcribe.cpp (Whisper, Parakeet, Canary, Moonshine, Voxtral, Qwen3-ASR, as GGUF):
  - flags `--stt-model|device|reserve-mib|threads` (reserve 512 MiB);
  - `/v1/audio/transcriptions` and `/v1/audio/translations` (multipart), answering json, text, verbose_json, srt or vtt.
- **TTS** is OuteTTS only:
  - flags `--tts-model`, `--tts-vocoder` (WavTokenizer), `--tts-voices DIR` and `--tts-reserve-mib` (256 MiB);
  - `/v1/audio/speech` answers wav or pcm only, up to 4096 characters.
- **Busy handling:** each engine serves one request at a time, and a second one gets 503 with `Retry-After`.
- **Discovery:**
  - the `features` list on `/props` and `/health`: `images_generate_v1`, `images_edit_v1`, `audio_transcriptions_v1`, `audio_translations_v1`, `audio_speech_v1`;
  - `/props` `media.*`;
  - `/v1/models` with `kind` and `capabilities`.
- **Memory:** the reserves are added to the fit target on the media device.

**xollama: what it has today**

- Upstream's `/v1/audio/transcriptions` shim, which sends the audio to an
  audio LLM's chat path (`middleware/openai.go:1295`, `server/routes.go:2228`).
- Remnants of upstream's MLX image generation, removed in #16615:
  `model.CapabilityImage`, the refusal at `server/routes.go:450`, and
  `cmd/cmd.go:1003,1372`.
- No route sends media requests to an engine.

**What the engine is missing**

1. **FLUX.2 Klein instruction edit.** Klein edits from reference images,
   using a plain Qwen3-4B as `--diffusion-llm` with no mmproj. opencoti drops
   the init image only when `--diffusion-llm-vision` is set
   (`oc_sd_engine.cpp:354`). So an edit with Klein runs as img2img, not as an
   instruction edit. **Needs an opencoti switch.**
2. **TTS other than OuteTTS.** SurfSense's desktop app voices Kokoro-82M,
   Supertonic-3 and KittenTTS mini 0.8 through **audio.cpp**, and all three
   are published as GGUF in `audio-cpp/audio.cpp-gguf`, validated on
   audio.cpp v0.8.2. **The ask is to vendor audio.cpp as a second TTS
   engine**, not to write new ones.
3. **MP3 output.** SurfSense's backend sends no `response_format` and saves
   the reply as `.mp3`. opencoti answers wav or pcm only.
4. **Video.** No route yet. The vendored stable-diffusion.cpp already runs
   Wan, Hunyuan and LTX-2.

## 3. What the clients call (read from source)

**SurfSense** (clone at
`/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ml/surfsense-src`,
HEAD 666bbb07). There are two products.

| Need | SurfSense backend (Docker, LiteLLM) | SurfSense desktop (`surfsense_local`) |
|---|---|---|
| Chat | `ollama_chat` provider at `:22434`: `/api/version`, `/api/tags`, `/api/show` (capabilities, model_info), `/api/chat`. Works today. | "custom" OpenAI-compatible connection: `/v1/models`, `/v1/chat/completions`. Works today. |
| Embeddings | `EMBEDDING_MODEL=litellm://ollama/<m>`, base URL set: `/api/embed`. Works today. | Bundled ONNX only. |
| Image generation | `aimage_generation`: `POST /v1/images/generations {model,prompt[,n]}`, no size or format; reads `b64_json` or `url`. Only through `openai_compatible`, which needs `capabilities_override.supports_image_generation` (API only), because `ollama_chat` forces it off. | `POST {base}/images/generations {model,prompt}`, falling back to `{base}/images`; reads `b64_json` or `url`. Classifies models by `/v1/models` `output_modalities` and `?output_modalities=image`. |
| Image edit | Not called. | `IMAGE_EDIT` type exists, unused; classified by image in and image out. |
| TTS | `TTS_SERVICE=openai/<m>`, `TTS_SERVICE_API_BASE=…/v1`: `POST /v1/audio/speech {model,input,voice}`, **no `response_format`, MP3 assumed**, voices alloy/echo/fable/onyx/nova/shimmer. | Hard-wired to its bundled audio.cpp. Remote TTS is refused. |
| STT | `STT_SERVICE=openai/<m>`: multipart `/v1/audio/transcriptions` (file, model); reads `.text`. | No STT feature. |
| Video | Remotion slides plus TTS, no generative model. | `VIDEO_GEN` type, no consumer. |

**Cerebriline:** its image tools call `/v1/images/generations` and later
`/v1/images/edits`, the OpenAI shapes.

## 4. Design

### 4.1 Attaching media to a model

The model's `xollama.json` config layer gains a `media` section (**schema v7**).
Each component is a blob in the model's manifest, named by role:

```json
"media": {
  "image": {"model": "sha256:…", "vae": "sha256:…", "llm": "sha256:…",
            "llm_vision": null, "args": "--steps 4", "edit": "reference",
            "device": "", "reserve_mib": 0,
            "defaults": {"width": 1024, "height": 1024, "steps": 4,
                         "cfg": 1.0, "output_format": "png"},
            "fixed": []},
  "stt":   {"model": "sha256:…", "threads": 0},
  "tts":   {"engine": "outetts|audiocpp", "model": "sha256:…",
            "vocoder": "sha256:…", "voices": "sha256:…",
            "voice_map": {"alloy": "af_heart"},
            "defaults": {"voice": "af_heart", "response_format": "mp3"}},
  "video": {}
}
```

- **The template owns the engine's settings** (owner, 2026-10-02). Each kind
  carries a `defaults` block. The client almost never sends these: SurfSense
  posts only a prompt, with no size, steps, voice or format. xollama sits
  between client and engine and launches the engine, so the template's
  settings apply at two points:
  1. **At launch**, as the engine's own defaults (`-W -H --steps --cfg-scale
     --sampling-method --flow-shift --video-frames --fps`, via the
     `--diffusion-args` passthrough, and the TTS and STT flags). opencoti
     falls back to these.
  2. **Per request**, where xollama fills every field the client left out
     before forwarding. So the template holds even where an engine's own
     defaults differ, and with a client that sends nothing.

  A client's explicit value wins, except for a field the template marks
  `fixed` (for example a size the model was not trained for).

  | Kind | `defaults` fields |
  |---|---|
  | image | `width`, `height`, `steps`, `cfg`, `sampler`, `scheduler`, `flow_shift`, `seed`, `output_format` (png/jpeg), `strength` (edit) |
  | stt | `language`, `response_format`, `task` (transcribe/translate) |
  | tts | `voice`, `language`, `response_format`, `speed` |
  | video | `width`, `height`, `frames`, `fps`, `steps`, `cfg`, `sampler`, `flow_shift`, `output_format` |

  `xollama show` and `tweak show model` list them. `tweak model`'s Media part
  edits them.
- **On an existing model:** `xollama tweak model qwen3:8b`, then a **Media**
  part that adds, swaps or removes components. The engine boots in combined
  mode: the LLM plus the media engines.
- **As a template:** a model with **no LLM layer** and only a `media`
  section, for example `flux2-klein:4b`, `kokoro:82m`, or `media-kit` (image,
  STT and TTS together). It boots media-only.
- **Source of a component:** a local GGUF path, or
  `hf.co/<repo>:<file>@<rev>`. Either way it is pulled by sha256 into the
  store, through `internal/fsowner`. Voice packs are a tar layer, unpacked
  once into a store-owned directory for `--tts-voices`.
- **Layer media type:** `application/vnd.xollama.media`, with
  `name=image.model|image.vae|…`, as `xollama.json` is named today.
  - Upstream's load switch (`server/images.go:751-850`) has no default case,
    so a stock ollama skips these layers rather than misreading them.
  - A VAE stored as a `model` layer would be read as the LLM. **Never reuse
    `image.model`.**
- **Modelfile verbs** (`IMAGE_MODEL`, `STT_MODEL`, `TTS_MODEL`, …) come later,
  under the modelfile-roundtrip feature, since `parser/` is upstream's.
  `tweak` and `xollama create` from an `xollama.json` cover it first.

### 4.2 The registry question (Phase 0 decides)

- Push and pull do not filter by media type. Whether **registry.ollama.ai**
  accepts an unknown layer media type is unmeasured.
- Phase 0 pushes one test template under `mannix` (owner's go, 2026-10-02)
  and pulls it back from a clean store. The owner deletes it afterwards.
- **If it is refused:** templates are published as a small manifest whose
  components are `hf.co/…@rev` references with sha256. `xollama pull` fetches
  them from Hugging Face. Nothing heavy is pushed.

### 4.3 Launch and scheduling

- `llamaServerConfigForModel` passes the `media` section. opencoti gets
  `--diffusion-*`, `--stt-*` and `--tts-*` with the blob paths. A media-only
  model boots without `-m`.
- **Stock llama.cpp** has no media engines. A model with `media` that lands
  on stock is refused with a clear message, never half-served.
- **Estimate:** each engine's reserve is added to the device it runs on.
  Klein 4B is about 8 GB without offload; the measured Qwen-Image 2.1 setup
  is 10.6 GB.
- **Busy:** opencoti serves one request per engine. xollama queues per model
  and engine (single flight, FIFO, bounded wait). It returns 503 with
  `Retry-After` only when the queue is full or the wait expires. An engine's
  own 503 is retried after its `Retry-After`.
- **Capabilities** in `/api/show` and on the tag: `image_generation`,
  `image_edit`, `audio_speech`, `audio_transcription` (later `video`), taken
  from the config and confirmed by the engine's `features`.

### 4.4 Routes

| Route | Served by | Notes |
|---|---|---|
| `POST /v1/images/generations` | media image engine | Default `response_format=b64_json`. `url` is answered as a `data:` URL. `n>1` runs sequentially. `size` passes through. |
| `POST /v1/images/edits` | image engine | Multipart passes through. Refused with a clear message when the model has no edit (`edit: none`). |
| `POST /v1/audio/speech` | TTS engine | `voice_map` maps OpenAI voice names. **Default format: what the model states, else mp3 if available, else wav.** See §6 Q3. |
| `POST /v1/audio/transcriptions`, `/translations` | STT engine **when the model has `media.stt`**; otherwise upstream's audio-LLM shim, unchanged | Hook in the transcription middleware. |
| `GET /v1/models` | as today, plus `input_modalities` and `output_modalities` | Plus `?output_modalities=` filtering, for SurfSense desktop. |
| `POST /v1/videos`, `GET /v1/videos/{id}`, `GET /v1/videos/{id}/content`, `DELETE` (later) | video engine | OpenAI's async job API. Also proxies sd-server's `/sdcpp/v1/vid_gen` and `/sdcpp/v1/jobs/*`, which SurfSense desktop's bundled sd-server speaks. A job queue, not busy-503. mp4 or webm. Asked of opencoti in #621. |

There are no native `/api/*` media routes in this plan; the OpenAI shapes are
what every client calls.

### 4.5 Off means off

- A model without `media` behaves exactly as upstream: same launch, same
  `/v1/audio/transcriptions` path, same `/v1/models` fields.
- The new `/v1/models` fields appear only on models that have `media`.
- New routes answer 404 when no media-capable model is named.
- Each hook (route block, transcription branch, `/v1/models` fields, show
  capabilities, scheduler reserve, launch flags) is a Registry row.

## 5. Models to test

| Kind | Model | Source | Size | Notes |
|---|---|---|---|---|
| image (gen + edit) | FLUX.2 Klein 4B | `leejet/FLUX.2-klein-4B-GGUF` (or `unsloth/…`), plus Qwen3-4B as the LLM, plus the flux2 VAE | about 2.5 GB at Q4, plus 2.5 GB LLM | 4 steps, 1024 px. Edit needs gap 1. |
| image | Z-Image Turbo | `leejet/Z-Image-Turbo-GGUF` | | Second generation model, for the A/B. |
| STT | Whisper large-v3-turbo, Parakeet | transcribe.cpp GGUF | 0.6 to 1.6 GB | |
| TTS (today) | OuteTTS 0.3 500M + WavTokenizer | `OuteAI/OuteTTS-0.3-500M-GGUF` | about 0.5 GB | The only engine b97 has. |
| TTS (gap 2) | Kokoro-82M, 46 voices, 8 languages | `audio-cpp/audio.cpp-gguf` `kokoro-82m-q8_0.gguf` | 190 MB | SurfSense's default voice. |
| TTS (gap 2) | Supertonic-3, 10 voices, 31 languages | same repo, `supertonic-3-f16.gguf` | 313 MB | 44.1 kHz |
| TTS (gap 2) | KittenTTS mini 0.8, 8 English voices | same repo, `kitten-tts-mini-0.8-orig.gguf` | 302 MB | |
| video (later) | Wan2.1 T2V 1.3B, Wan2.2 TI2V 5B | SurfSense desktop catalog | | When opencoti ships video. |

All voice models are downloaded to `/srv/ml/media/` for the tests.

## 5b. Phases

- **Phase 0: measure (no feature code).**
  - b97 media-only on solidPC: Klein generation, Klein edit (expected to
    show gap 1), Whisper transcription, OuteTTS speech, the reserves against
    measured memory, and the 503 on a second request.
  - The registry push test (§4.2), under `mannix`. The owner deletes the template afterwards.
  - Whether SurfSense's TTS path accepts WAV bytes saved as `.mp3`.
- **Phase 1 — CLOSED 2026-10-02.** Built: `types/xollama/media.go`, `create/xollama_media.go`, `cmd/tweak/media.go`.
  Tested with unit and mutation tests (11 mutations, all killed), then live on solidPC:
  - Kokoro and Whisper were attached to a Qwen3-4B model in a scratch store;
  - 874 MB and 189 MB were uploaded and stored as `media/stt.model` and `media/tts.model`;
  - `show` listed them; a re-run found them already on the server; `rm` collected both blobs.

  Not built: `hf.co` sourcing (Phase 4, with templates). Media-only templates also wait,
  because `create` still needs a FROM. Phase 1 as planned:
  **schema v7 `media`** (components, `defaults`, `fixed`) with validation, `tweak model` Media part,
  `xollama show`, `tweak show model`, sourcing components from a path or
  `hf.co`, and the layers. Unit and mutation tests.
- **Phase 2 — CLOSED 2026-10-02.** Built:
  - **Runner:** `llm/engine_media.go`. A model's media runs in its own media-only opencoti process (no `-m`) until M7 gates a combined boot (#622/#623).
    - The scheduler key is `media:<manifest digest>` (`mediaTwin`, `server/media.go`). Loading goes through one hook in `Scheduler.load`.
    - `MediaArgs` maps the config to flags. `MediaEstimate` adds each engine's reserve (image 3072, STT 512, TTS 256, video 3072 MiB) to the GPU with the most free memory.
    - `WaitUntilRunning` refuses an engine whose `/health` `features` lack a configured kind. `--tts-engine` is never sent for OuteTTS.
    - Video flags (`--video-*`) are provisional and were sent to opencoti in the #624 ack.
  - **Capabilities:** `image_generation`, `image_edit`, `speech`, `transcription` and `video` in `/api/show` (`images.go` hook). Never upstream's `image`, because generate and chat refuse any model carrying it.
  - **Media-only templates:** `create` with no FROM and only `media` (`create.go` hook); `show` skips the GGUF read for them (`GetModelInfo` hook).
  - **`hf.co` sourcing, pulled forward from Phase 4.** The owner asked to re-use ollama's existing Hugging Face fetch.
    - `internal/mediahub` resolves a file with a HEAD that doesn't follow the redirect: `X-Linked-Etag` is the sha256, `X-Linked-Size` the size, `X-Repo-Commit` the revision.
    - `/api/xollama/media/pull` (`server/media_pull.go`) fetches the blob through upstream's `downloadBlob` from `hf.co/v2/<repo>/blobs/sha256:<oid>`. That endpoint serves any LFS file: gguf, safetensors and .bin were all verified.
  - **Catalog and discovery:** `internal/mediahub/catalog.go` and `xollama media list | search KIND [QUERY] | files REPO | create ID [NAME] [--to MODEL]` (`cmd/tweak/mediacmd.go`). Entries that need engine support missing today are marked `opencoti M7`: audio.cpp TTS, Klein `ref` edit, mp4 video.
  - **Live on solidPC, dev engine `0.10.5-c7-2610020719001`:**
    - an OuteTTS + Whisper media-only process was ready in 1.5 s;
    - speech then transcription round-tripped "The quick brown fox jumps over the lazy dog.";
    - `media create whisper-large-v3-turbo` fetched 874 MB from HF in 37 s;
    - `media create outetts-0.3-500m --to qwen3-4b:media` fetched the vocoder and added `speech` to the model's capabilities.
  - **Tests:** unit tests, a live test gated by `XOLLAMA_MEDIA_LIVE_DIR`, and an HF resolve test gated by `XOLLAMA_HF_LIVE` (23 refs). 12 mutations, all killed.
  - **Not built (moved to Phase 3, with the routes):** the per-engine request queue and the refusal on stock at request time. A media runner never starts on stock, because it always launches opencoti.
- **Phase 2 as planned:** media-only engine process, flags, refusal on stock, reserves in the estimate, capabilities, the per-engine queue.
- **Phase 3: routes.** Image generation first, which is **Cerebriline's
  need**. Then edit, speech, transcription, and the `/v1/models`
  modalities. Live tests against b97 and against Cerebriline's image tool.
- **Phase 4: templates.** The `hf.co` sourcing and the catalog are built (Phase 2); what is left is publishing, per Phase 0: `flux2-klein:4b`, `whisper:turbo`, `outetts:0.3`,
  later `kokoro`, `supertonic`, `kittentts`, and a mixed `media-kit`. Pushed
  or published as `hf.co` references, per Phase 0.
- **Phase 5: SurfSense.**
  - `docs/xollama/surfsense.mdx`: chat and embeddings over `ollama_chat`;
    image, TTS and STT over `openai_compatible` and the `*_SERVICE=openai/…`
    variables; the capability override.
  - A first-class `xollama` provider entry in SurfSense only if the owner
    wants one (§6 Q2).
- **Phase 6: video**, when opencoti ships it.

## 6. Decisions (owner, 2026-10-02)

| Question | Decision |
|---|---|
| Registry push test | **Push one test template under `mannix`.** Claude cannot delete it from ollama.com. The owner deletes it, and Claude says when the test is done and the template can go. |
| SurfSense | **Docs first, provider entry later.** `docs/xollama/surfsense.mdx` covers what works through `ollama_chat` and `openai_compatible`. A first-class entry is decided after it has been tested. |
| Media settings | **The template owns them.** Its `media.<kind>.defaults` go to the engine at launch and fill every field a request leaves out. A client value wins unless the field is `fixed` (§4.1). |
| MP3 | **opencoti encodes it (M7, #623).** xollama never encodes audio; it passes through what the engine returns. mp3 becomes the speech default in M7. |
| Model sourcing | **Re-use ollama's `hf.co` fetch** (owner, 2026-10-02), plus a model catalog and discovery (`xollama media`). |
| M7 scope | **Built on the assumption** that M7 brings audio.cpp (`--tts-engine audiocpp`) and mp4/h264 video. Confirmed by opencoti in #624. |

## 7. Asked of opencoti (mails #620 and #621, 2026-10-02); answered in #622

**Answers (#622):**
- STT defaults to `json`. `features` on `/health` is authoritative.
- **No mp3 today:** the engine serves wav and pcm only. The encoder question is back with the owner.
- **Klein edit is img2img today.** The `--diffusion-edit ref` switch and audio.cpp TTS await the owner's word, asked together with M7.
- **Combined boot is not gated** with PolyKV, elastic slots or the rolling window. Keep media in its own process for now.
- Speech engines stay resident from boot.
- **Video is queued as opencoti M7**, right after the b97 handoff, on the shape asked:
  - `/v1/videos` plus `/sdcpp/v1/*`;
  - async jobs;
  - webm, or mp4 if possible;
  - `videos_generate_v1` and `videos_i2v_v1`;
  - Wan2.1 1.3B first.

**What was asked:**

- Gap 1, the Klein reference edit.
- Gap 2, audio.cpp as a TTS engine.
- Gap 3, mp3.
- Gap 4, video (#621):
  - Wan2.1 T2V 1.3B and Wan2.2 TI2V 5B from SurfSense's catalog, with per-model launch defaults, since Studio posts only a prompt;
  - async jobs with progress and cancel;
  - OpenAI `/v1/videos`, plus sd-server's `/sdcpp/v1/*` kept;
  - mp4 or webm with muxed audio;
  - image to video, and the `videos_*_v1` features.
- SurfSense's other needs (#621):
  - prompt-only image requests use the model's own defaults;
  - audio.cpp's request shape (`language`, voice ids) kept;
  - STT defaults to json;
  - TTS warm-up is paid once.
- Whether combined LLM-plus-media boot works under PolyKV and elastic slots.
