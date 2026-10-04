#!/bin/bash
# Speech models through xollama; each mp3 is transcribed back by Whisper.
H=http://127.0.0.1:22498
export XOLLAMA_HOST=127.0.0.1:22498
cd /srv/ml/xb140
(setsid nohup ./serve.sh > serve.log 2>&1 &)
for i in $(seq 40); do curl -s --max-time 2 $H/api/version >/dev/null && break; sleep 0.5; done
T="The first ship came in at dawn."
say() { # model voice outfile
  local body="{\"model\":\"$1\",\"input\":\"$T\"${2:+,\"voice\":\"$2\"}}"
  local s=$(date +%s.%N)
  local code=$(curl -s -o out/$3 -w '%{http_code}' $H/v1/audio/speech -d "$body")
  local e=$(date +%s.%N)
  local kind=$(file -b out/$3 | cut -c1-40)
  local txt=$(curl -s $H/v1/audio/transcriptions -F model=mannix/whisper:large-v3-turbo -F file=@out/$3 | python3 -c 'import sys,json; print(json.load(sys.stdin).get("text","?").strip())' 2>&1)
  printf '%s voice=%s http=%s %.1fs [%s] -> %s\n' "$1" "${2:-default}" "$code" "$(echo "$e - $s" | bc)" "$kind" "$txt"
}
say mannix/kokoro:82m "" kokoro.mp3
say mannix/kokoro:82m nova kokoro_nova.mp3
say mannix/supertonic:3 "" supertonic.mp3
say mannix/supertonic:3-q8_0 "" supertonic_q8.mp3
say mannix/kittentts:mini-0.8 "" kitten.mp3
say mannix/outetts:0.3 "" oute.mp3
say outetts-voices narrator oute_narrator.mp3
say outetts-voices host oute_host.mp3
dpkg -l | grep -c -i espeak
echo SPEECH-DONE
