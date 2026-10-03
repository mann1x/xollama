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
  `audio_speech_voice_files_v1` for extra voices (`tts.voices`, one layer per
  voice, `--tts-voice NAME=<blob>`; never a tar or a directory) (`MediaFeatures`,
  `llm/engine_media.go`; opencoti bug-3880). A missing blob is refused by name
  before launch (`newMediaRunner`). Guards:
  `TestAudioCppSpeechNeedsAnEngineThatReadsBlobsAsTheyAre`,
  `TestAMissingMediaBlobIsRefusedByName`,
  `TestAnUnreadableHealthIsNotReadAsMissingFeatures`.
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
