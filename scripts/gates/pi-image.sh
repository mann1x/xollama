#!/bin/bash
# Engine 2610041714001 on the Pi 5, in the published :dev image with only the
# engine's files replaced. Interleaved 512-token runs (the Pi throttles), then
# speech, each clip transcribed back by Whisper.
cd /root/xollama-arm64
N=xollama-b140; H=http://127.0.0.1:22498
mkdir -p out-b140
docker build -q -t xollama:b140-test ctx-b140 || exit 1
docker rm -f $N >/dev/null 2>&1
docker run -d --name $N -p 127.0.0.1:22498:22434 -e OLLAMA_DEBUG=1 \
  -v /root/xollama-arm64/models:/root/.ollama/models xollama:b140-test >/dev/null || exit 1
ok=0; for i in $(seq 60); do if curl -s --max-time 2 $H/api/version >/dev/null; then ok=1; break; fi; sleep 1; done
[ $ok = 1 ] || { echo "server did not come up"; docker logs $N 2>&1 | tail -20; exit 1; }
curl -s $H/api/version; echo
docker exec $N sh -c 'ls /usr/lib/ollama | grep -E "opencoti|oc-|BUILD_INFO|COPYING|LICENSE"; ls /usr/lib/aarch64-linux-gnu/libatomic* 2>/dev/null || echo "no libatomic"'
curl -s $H/api/tags | python3 -c 'import sys,json; print(" ".join(m["name"] for m in json.load(sys.stdin)["models"]))'
gen() { curl -s $H/api/generate -d "{\"model\":\"$1\",\"prompt\":\"Write a long, detailed essay about the history of the steam engine.\",\"stream\":false,\"options\":{\"num_predict\":512,\"seed\":7,\"temperature\":0}}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print("%s tokens=%d %.1f tok/s" % (sys.argv[1], d.get("eval_count",0), d.get("eval_count",0)/max(d.get("eval_duration",1),1)*1e9) if "eval_count" in d else (sys.argv[1], d))' "$1"; }
gen pi/q15-oc >/dev/null; gen pi/q15-lcpp >/dev/null   # warm both
for r in 1 2 3; do gen pi/q15-oc; gen pi/q15-lcpp; done
docker logs $N 2>&1 | grep -E "using opencoti|falling back" | cut -c1-220 | sort | uniq -c | tail -4
T="The first ship came in at dawn."
say() { # model voice outfile format
  local body="{\"model\":\"$1\",\"input\":\"$T\"${2:+,\"voice\":\"$2\"}${4:+,\"response_format\":\"$4\"}}"
  local s=$(date +%s.%N)
  local code=$(curl -s -o out-b140/$3 -w '%{http_code}' $H/v1/audio/speech -d "$body")
  local e=$(date +%s.%N)
  local txt=$(curl -s $H/v1/audio/transcriptions -F model="$W" -F file=@out-b140/$3 | python3 -c 'import sys,json; print(json.load(sys.stdin).get("text","?").strip())' 2>&1)
  printf '%s voice=%s http=%s %.1fs [%s] -> %s\n' "$1" "${2:-default}" "$code" "$(echo "$e - $s" | bc)" "$(file -b out-b140/$3 | cut -c1-32)" "$txt"
}
tags=$(curl -s $H/api/tags | python3 -c 'import sys,json; print("\n".join(m["name"] for m in json.load(sys.stdin)["models"]))')
W=$(echo "$tags" | grep -i whisper | head -1)
K=$(echo "$tags" | grep -i kokoro | head -1); S=$(echo "$tags" | grep -i supertonic | head -1); C=$(echo "$tags" | grep -i kitten | head -1)
say "$K" "" kokoro.mp3; say "$K" nova kokoro_nova.mp3; say "$S" "" supertonic.mp3; say "$S" "" supertonic.wav wav; say "$C" "" kitten.mp3
docker logs $N 2>&1 | grep -i -E "espeak|dlopen|libatomic|cannot|error" | cut -c1-200 | sort | uniq -c | tail -8
docker rm -f $N >/dev/null
echo PI-DONE
