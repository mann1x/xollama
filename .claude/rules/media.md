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
  voice, `--tts-voice NAME=<blob>`; never a tar or a directory) (`MediaFeatures`,
  `llm/engine_media.go`; opencoti bug-3880). A missing blob is refused by name
  before launch (`newMediaRunner`). Guards:
  `TestAudioCppSpeechNeedsAnEngineThatReadsBlobsAsTheyAre`,
  `TestAMissingMediaBlobIsRefusedByName`,
  `TestAnUnreadableHealthIsNotReadAsMissingFeatures`.
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
  `TestAudioCppSpeechIsNeverPlacedOnAGPU`.
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
- **The catalog** (`internal/mediahub/catalog.go`) feeds `xollama media list`
  and `media create`. Entries of one family share a builder (`supertonic(file)`,
  `wan21(model, encoder)`, `wan22(model)`) so a quant tag differs from its
  sibling only in the file. `Published: true` is set only for a template that
  is on ollama.com, and the table in `docs/xollama/media.mdx` lists the same set.
- A video template's clip (`wanDefaults`) and `ReserveMiB` (`wan21Reserve`,
  `wan22Reserve`) are measured peaks, cited in the constant's comment; change
  one only against a new measurement. The template clip is the one that fits a
  24 GB card; a larger request is refused with a 400, never scaled.
- Wan2.2 takes Wan2.2's own VAE, never Wan2.1's.
