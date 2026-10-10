#!/bin/bash
# G10 before an image exists for the tree: the engine THIS TREE pins, on the
# Pi 5, in a published image with only the engine's files replaced.
# Interleaved 512-token runs (the Pi throttles), then speech, each clip
# transcribed back by Whisper. On solidPC:
#
#   d=$(dirname "$(scripts/gates/stage-engine.sh aarch64)")
#   ssh root@dietpi5.local mkdir -p /root/xollama-arm64/ctx-$TAG
#   scp -r "$d" root@dietpi5.local:/root/xollama-arm64/ctx-$TAG/engine
#   ssh root@dietpi5.local TAG=$TAG BASE=<image> bash -s < scripts/gates/pi-image.sh
#
# TAG names the context, the test image and the output directory; BASE is the
# image the engine is put into (default: the published :dev).
TAG=${TAG:?TAG=<name of ctx-<TAG> in /root/xollama-arm64>}
BASE=${BASE:-ghcr.io/mann1x/xollama:dev}
cd /root/xollama-arm64
N=xollama-$TAG; H=http://127.0.0.1:22498; O=out-$TAG; I=xollama:$TAG-test
[ -d ctx-$TAG/engine ] || { echo "ctx-$TAG/engine is missing"; exit 1; }
mkdir -p $O
cat > ctx-$TAG/Dockerfile <<EOD
FROM $BASE
RUN rm -f /usr/lib/ollama/opencoti-* /usr/lib/ollama/oc-*.so /usr/lib/ollama/ggml-cuda-sbsa-* /usr/lib/ollama/BUILD_INFO*.md /usr/lib/ollama/engine-manifest.txt
COPY engine/ /usr/lib/ollama/
RUN chmod 755 /usr/lib/ollama/opencoti-llamafile-*
EOD
docker build -q -t $I ctx-$TAG || exit 1
docker rm -f $N >/dev/null 2>&1
echo "before: temp=$(vcgencmd measure_temp 2>/dev/null | cut -d= -f2) $(vcgencmd get_throttled 2>/dev/null)"
docker run -d --name $N -p 127.0.0.1:22498:22434 -e OLLAMA_DEBUG=1 \
  -v /root/xollama-arm64/models:/root/.ollama/models $I >/dev/null || exit 1
ok=0; for i in $(seq 60); do if curl -s --max-time 2 $H/api/version >/dev/null; then ok=1; break; fi; sleep 1; done
[ $ok = 1 ] || { echo "server did not come up"; docker logs $N 2>&1 | tail -20; exit 1; }
curl -s $H/api/version; echo
docker exec $N sh -c 'ls /usr/lib/ollama | grep -E "opencoti|oc-|ggml-cuda-sbsa|BUILD_INFO"'
curl -s $H/api/tags | python3 -c 'import sys,json; print(" ".join(m["name"] for m in json.load(sys.stdin)["models"]))'
gen() { curl -s $H/api/generate -d "{\"model\":\"$1\",\"prompt\":\"Write a long, detailed essay about the history of the steam engine.\",\"stream\":false,\"options\":{\"num_predict\":512,\"seed\":7,\"temperature\":0}}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print("%s tokens=%d %.1f tok/s" % (sys.argv[1], d.get("eval_count",0), d.get("eval_count",0)/max(d.get("eval_duration",1),1)*1e9) if "eval_count" in d else (sys.argv[1], d))' "$1"; }
gen pi/q15-oc >/dev/null; gen pi/q15-lcpp >/dev/null   # warm both
for r in 1 2 3; do gen pi/q15-oc; gen pi/q15-lcpp; done
docker logs $N 2>&1 | grep -E "using opencoti|falling back" | cut -c1-220 | sort | uniq -c | tail -4
T="The first ship came in at dawn."
say() { # model voice outfile format
  local body="{\"model\":\"$1\",\"input\":\"$T\"${2:+,\"voice\":\"$2\"}${4:+,\"response_format\":\"$4\"}}"
  local s=$(date +%s.%N)
  local code=$(curl -s -o $O/$3 -w '%{http_code}' $H/v1/audio/speech -d "$body")
  local e=$(date +%s.%N)
  local txt=$(curl -s $H/v1/audio/transcriptions -F model="$W" -F file=@$O/$3 | python3 -c 'import sys,json; print(json.load(sys.stdin).get("text","?").strip())' 2>&1)
  printf '%s voice=%s http=%s %.1fs [%s] -> %s\n' "$1" "${2:-default}" "$code" "$(echo "$e - $s" | bc)" "$(file -b $O/$3 | cut -c1-32)" "$txt"
}
tags=$(curl -s $H/api/tags | python3 -c 'import sys,json; print("\n".join(m["name"] for m in json.load(sys.stdin)["models"]))')
W=$(echo "$tags" | grep -i whisper | head -1)
K=$(echo "$tags" | grep -i kokoro | head -1); S=$(echo "$tags" | grep -i supertonic | head -1); C=$(echo "$tags" | grep -i kitten | head -1)
say "$K" "" kokoro.mp3; say "$K" nova kokoro_nova.mp3; say "$S" "" supertonic.mp3; say "$S" "" supertonic.wav wav; say "$C" "" kitten.mp3
docker logs $N 2>&1 | grep -E "opencoti build|sbsa|opencoti device enumeration" | cut -c1-220 | sort -u | head -6
echo "crash lines: $(docker logs $N 2>&1 | grep -c -E "llama-server terminated|runner has unexpectedly|signal: aborted|signal: segmentation|signal: killed")"
echo "after: temp=$(vcgencmd measure_temp 2>/dev/null | cut -d= -f2) $(vcgencmd get_throttled 2>/dev/null)"
docker rm -f $N >/dev/null
docker ps --format '{{.Names}}' | tr '\n' ' '; echo
echo PI-DONE
