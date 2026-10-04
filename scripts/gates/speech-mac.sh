#!/bin/bash
# Speech models through the dev xollama on the Mac; each mp3 is transcribed back by Whisper.
H=http://127.0.0.1:22498
cd ~/dev/xollama && mkdir -p out
T="The first ship came in at dawn."
say() {
  local body="{\"model\":\"$1\",\"input\":\"$T\"${2:+,\"voice\":\"$2\"}${4:+,\"response_format\":\"$4\"}}"
  local s=$(date +%s)
  local code=$(curl -s -o out/$3 -w '%{http_code}' $H/v1/audio/speech -d "$body")
  local e=$(date +%s)
  local kind=$(file -b out/$3 | cut -c1-44)
  local txt=""
  if [ "$code" = 200 ]; then txt=$(curl -s $H/v1/audio/transcriptions -F model=mannix/whisper:large-v3-turbo -F file=@out/$3 | cut -c1-200); else txt=$(cut -c1-300 out/$3); fi
  echo "$1 voice=${2:-default} http=$code $((e-s))s [$kind] -> $txt"
}
say mannix/kokoro:82m "" kokoro.mp3 mp3
say mannix/kokoro:82m nova kokoro_nova.mp3 mp3
say mannix/supertonic:3 "" supertonic.mp3 mp3
say mannix/supertonic:3-q8_0 "" supertonic_q8.wav wav
say mannix/kittentts:mini-0.8 "" kitten.mp3 mp3
say mannix/outetts:0.3 "" oute.mp3 mp3
say outetts-voices narrator oute_narrator.mp3 mp3
echo SPEECH-DONE
