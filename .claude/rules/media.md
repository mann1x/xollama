---
paths:
  - server/media*.go
  - llm/engine_media.go
  - internal/mediahub/**
  - types/xollama/media.go
  - create/xollama_media.go
  - cmd/tweak/media*.go
  - docs/xollama/media.mdx
  - plans/media-integration.md
---

# Media models (`media` hook)

- **`GET /v1/realtime`** is `RealtimeHandler` (`server/media_realtime.go`):
  `ReverseProxy` carries the websocket to the engine. Never gate it on
  `audio_realtime_v1` in `/health`, which a whisper engine does not list: the
  engine answers 501 itself. Gate `scripts/gates/engine-options.sh`.
- **An LLM and its media share one process** (owner, 2026-10-31):
  `combinedMedia` (`server/media_combined.go`) for a GGUF model that generates;
  `llamaServerConfigForModel` fills `MediaArgs` and `MediaEstimate`,
  `mediaArgsFor` (`llm/engine_media_combined.go`) adds them on opencoti only,
  `predictVRAM` prices them, and `mediaRunner` schedules the model itself and
  checks the engine's `/health` features. A media call never reloads a running
  LLM (`isMediaCall` in `needsReload`). It stays one runner in the shared pool,
  never counted under a media cap. Stock, MLX, decision and embedding models
  keep the twin, and a missing blob keeps the LLM loading. `opencoti` gated this
  in M7.6; never say otherwise from an old comment. A repeated prompt's greedy
  text differs from its first run, media or not: compare like with like. Gate:
  `scripts/gates/media-combined.sh`.
- A media-only model is served by its own opencoti process
  (`llm/engine_media.go`), scheduled as `media:<digest>` from `server/media.go`;
  its components are `application/vnd.xollama.media` layers by digest, written
  by `create/xollama_media.go` and typed in `types/xollama/media.go`.
- **Every component goes to the engine as its blob path, as it is.** The
  engine tells a file's format by its contents, never its name; xollama never
  renames, links, copies or unpacks a component to suit an engine we control
  (owner, 2026-10-03, after a hard-link workaround for audio.cpp). An engine
  that cannot do this is refused at load by a `/health` feature it lacks:
  `audio_speech_content_format_v1` for `engine: audiocpp`,
  `audio_speech_audiocpp_v1` too (advertised only while the audio.cpp sidecar
  is loaded), `media_codec_v1` when the template's own default format needs
  the codec sidecar (`needsCodec`: anything but wav/pcm speech and avi/webm
  video; a template that states no format needs nothing),
  `audio_speech_voice_files_v1` for extra voices (`tts.voices`, one layer per
  voice, `--tts-voice NAME=<blob>`; never a tar or a directory),
  `audio_vad_v1` for a transcriber's detector (`stt.vad`, its own layer,
  `--stt-vad-model <blob>`; opencoti c12) (`MediaFeatures`,
  `llm/engine_media.go`; opencoti bug-3880). A missing blob is refused by name
  before launch (`newMediaRunner`). Guards:
  `TestAudioCppSpeechNeedsAnEngineThatReadsBlobsAsTheyAre`,
  `TestAMissingMediaBlobIsRefusedByName`,
  `TestAnUnreadableHealthIsNotReadAsMissingFeatures`,
  `TestTheSTTDetectorIsAComponentAndRaisesTheFloor`.
- **A voice the engine refuses at boot is refused by `Validate` first**
  (`TTSMedia.validate`, `types/xollama/media.go`; opencoti #679): a name in
  `reservedVoiceNames` (OuteTTS's `default` and the OpenAI names it maps to
  it), and any `tts.voices` on `engine: audiocpp`, which serves only its
  model's built-in voices. Guard: `TestEachVoiceIsItsOwnLayerNamedByItsVoice`.
- **`engine: audiocpp` speech is always on the CPU** (`mediaOnCPU`,
  `llm/engine_media.go`): audio.cpp has no GPU backend in the engine, so its
  `tts.device` is never read. Placed on a GPU it books memory it never uses and
  is launched with a `--gpu` backend the engine may not have (solidPC, b117,
  2026-10-03). Another engine beside it still decides for itself. Guard:
  `TestAudioCppSpeechIsNeverPlacedOnAGPU`. So is a Whistle transcriber off
  macOS (`whistleOnCPU` reads the GGUF's `general.architecture`; opencoti c12
  keeps it on the CPU, Metal excepted; an unreadable model keeps its stated
  device). Guard: `TestAWhistleModelIsPlacedOnTheCPUOffMacOS`.
- **A media model's device pin travels on its twin** (`mediaTwin`,
  `server/media.go`): the scheduler runs `selectModelDevices` on the twin, and
  a twin without `Devices` is an unpinned model, so `tweak model
  --device-backend/--devices` was silently ignored and every media engine took
  the GPU with the most free memory (found 2026-10-05, validating c9). Anything
  new in `xollama.Config` that the scheduler reads must be copied there too.
  Guard: `TestAMediaModelsDevicePinReachesTheScheduler`.
- **A video job whose engine exited is a failed job, never a missing one**
  (`videoJob.lose`, `server/media_video.go`): it lets the engine's slot go but
  stays in the table for `videoKeep` with `status: failed`,
  `error.code: engine_exited`; content is a 409, delete works. Before, the
  watcher ended it and a polling client saw 502 then 404 for ever (the c9
  engine crashes on the RX 9070 XT and the Renoir iGPU, 2026-10-05). Guard:
  `TestAVideoWhoseEngineExitedIsAFailedJobNotAMissingOne`; live:
  `/srv/ml/xc9/media/lost-check.sh`.
- **A media engine that exited is unloaded as its last request ends**
  (`mediaEngineGone`, `server/media_gone.go`, one hook line in
  `processCompleted`): left to its keep-alive it stayed in `/api/ps` as loaded
  and its memory stayed booked. Media keys only, by design: a text runner that
  exited is upstream's to find on the next request, and widening this would
  change stock llama.cpp. Guard: `TestAMediaEngineThatExitedIsUnloadedAtOnce`
  (both halves: the media key goes, the text key stays).
- **A failed load reports the engine's last `tailLines` (4) lines**, joined
  with ` | ` (`tailWriter.last`), never only the last: the engine states the
  cause first and its advice last. Guard:
  `TestAFailedLoadReportsTheCauseNotOnlyTheLastLine`.
- **Each media kind has its own cap** (`server/load_caps.go`, three marked
  lines in `processPending`): `XOLLAMA_MAX_LOADED_IMAGE_MODELS`, `_SPEECH_`,
  `_TRANSCRIPTION_`, `_VIDEO_`, and `XOLLAMA_MAX_LOADED_DECISION_MODELS` for
  decision models (no upstream setting exists), default 1 (owner, 2026-10-31).
  Decision models run on stock llama.cpp, so `XOLLAMA_ENGINE=llamacpp` keeps them
  in the pool (`decisionLimit`), off means off; media never runs there. Capped engines
  are outside `OLLAMA_MAX_LOADED_MODELS`, and at the cap only an engine of that
  kind is unloaded; `0` is the shared pool. An engine counts under every kind
  it serves and stays in the pool while any of them is 0. Only a twin counts: a
  model's LLM stays in the pool. Memory eviction ignores the caps. A busy victim
  blocks `processPending`, as upstream's limit does, so a held video job stalls
  other loads. Guards: `TestAMediaLoadUnloadsOnlyItsOwnKindAtTheCap`,
  `TestAnEngineOfSeveralKindsNeedsRoomUnderEach`,
  `TestMediaEnginesAreOutsideTheSharedPool`,
  `TestACapOfZeroPutsTheKindBackInTheSharedPool`,
  `TestAnLLMWithMediaKeepsItsPlaceInTheSharedPool`, and through
  `processPending` (each fails with the hook removed)
  `TestTheSchedulerKeepsAnLLMWhenAnEngineLoadsAtTheLimit`,
  `TestTheSchedulerUnloadsTheSameKindAtItsCap`; decision
  `TestADecisionModelHasItsOwnCap`,
  `TestAStockEngineKeepsDecisionModelsInTheSharedPool`,
  `TestTheDecisionCapChangesNothingAtZero`.
- **The catalog** (`internal/mediahub/catalog.go`) feeds `xollama media list`
  and `media create`. Entries of one family share a builder (`supertonic(file)`,
  `wan21(model, encoder)`, `wan22(model)`, `whistle(vad)`) so a quant tag differs from its
  sibling only in the file. `Published: true` is set only for a template that
  is on ollama.com, and the table in `docs/xollama/media.mdx` lists the same set.
- A video template's clip (`wanDefaults`) and `ReserveMiB` (`wan21Reserve`,
  `wan22Reserve`) are measured peaks, cited in the constant's comment; change
  one only against a new measurement. The template clip is the one that fits a
  24 GB card; a larger request is refused with a 400, never scaled.
- Wan2.2 takes Wan2.2's own VAE, never Wan2.1's.
