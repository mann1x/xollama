# Whisper word timestamps

The OpenAI-compatible `POST /v1/audio/transcriptions` route can use a
whisper.cpp companion for `response_format=verbose_json` with
`timestamp_granularities[]=word`. This supplies actual Whisper token
alignment intervals as `words[].start` and `words[].end`, in seconds.
It does not distribute segment durations across words.

## Build and use

On Linux, install Go (the version in go.mod), CMake, a C/C++ compiler, Git
and ffmpeg, then run:

```sh
bash scripts/build-whisper-timestamps.sh
build/whisper-timestamps/xollama serve
```

The executable discovers `whisper-cli` beside itself. To use another
installation, set `XOLLAMA_WHISPER_CLI` to its absolute path. The build
script pins whisper.cpp v1.8.3 at
`2eeeba56e9edd762b4b38467bab96c2517163158`.

Use an xollama media template whose `media.stt.model` is a legacy
whisper.cpp `ggml-*.bin` blob, for example the existing
`mannix/whisper:large-v3-turbo` template. The companion receives that
template's actual model blob, language, prompt, temperature and translation
task after template defaults and fixed fields have been applied.

```sh
curl http://localhost:22434/v1/audio/transcriptions \
  -F model=mannix/whisper:large-v3-turbo \
  -F file=@speech.wav \
  -F response_format=verbose_json \
  -F 'timestamp_granularities[]=word'
```

Add a second `timestamp_granularities[]=segment` field to include segments;
these are the companion's word-sized segments. Omitting the word field
keeps the existing opencoti path, including its original segment grouping.
Installations without the companion also keep their existing behavior.
GGUF/transcribe.cpp models stay on the original backend.

## Runtime behavior

The companion is currently CPU-only, runs one request at a time with the
existing bounded media queue, and reloads the Whisper model for each word
timestamp request. ffmpeg converts the upload to 16 kHz mono PCM WAV in a
private temporary directory. Request cancellation or a ten-minute timeout
terminates the child process, and temporary audio/output files are removed
on return. Missing token alignment causes an error rather than guessed
word intervals. Streaming word timestamp requests are rejected.

Word boundaries use whisper.cpp's `--split-on-word` behavior. Token
start/end estimates come from its token timestamp implementation; these
remain model estimates, not manually verified forced alignment. Punctuation
and multi-token words retain the text of the whole backend word segment.
Translation timestamps align translated output and should be interpreted
accordingly. No quality guarantee is made for languages without whitespace
word boundaries.

## Validation

```sh
go test ./server ./internal/whispertimestamps ./middleware ./openai
XOLLAMA_WHISPER_CLI=/absolute/path/to/whisper-cli \
XOLLAMA_WHISPER_WORD_MODEL=/absolute/path/to/ggml-tiny.en.bin \
XOLLAMA_WHISPER_WORD_AUDIO=/absolute/path/to/whisper.cpp/samples/jfk.wav \
  go test ./server -run '^TestWhisperWordTimestampsLive$' -count=1 -v
```

The live test goes through the transcription HTTP handler and model store,
executes ffmpeg and whisper.cpp, and checks nonempty monotonic word
intervals within the audio duration. Proxy tests cover omitted, segment,
word and repeated word/segment fields. Conversion tests verify subword
merging, punctuation, UTF-8 text, milliseconds-to-seconds conversion,
special-token filtering, silence representation and missing alignment.

## Why a companion

xollama's pinned opencoti engine already forwards the requested word
granularity to transcribe.cpp and serializes its word results. However,
its Whisper backend only advertises segment timing and the server
downgrades a word request to segment timing. The pinned engine is a
separately published binary, not inference source compiled in this tree.
The companion provides a buildable path without replacing that pin or
changing requests which do not ask for words.
