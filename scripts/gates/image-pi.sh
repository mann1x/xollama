#!/bin/bash
# G10 on arm64: the published image on the Pi 5, in its own container, port
# and model directory. The owner's containers are never touched. IMG=<image>.
IMG=${IMG:-mannixita/xollama:dev}; N=xollama-test; H=http://127.0.0.1:22498
cd /root/xollama-arm64 && mkdir -p out-gate
docker pull -q $IMG || exit 1
docker image inspect $IMG --format '{{.Architecture}}/{{.Os}} {{.Id}}'
docker rm -f $N >/dev/null 2>&1
docker run -d --name $N -p 127.0.0.1:22498:22434 -e OLLAMA_DEBUG=1 -v /root/xollama-arm64/models:/root/.ollama/models $IMG >/dev/null || exit 1
ok=0; for i in $(seq 60); do curl -s --max-time 2 $H/api/version >/dev/null && { ok=1; break; }; sleep 1; done
[ $ok = 1 ] || { echo "server did not come up"; docker logs $N 2>&1 | tail -20; exit 1; }
curl -s $H/api/xollama; echo; curl -s $H/api/version; echo
docker exec $N sh -c 'cat /usr/lib/ollama/PAYLOAD' | cut -c1-150
gen() { curl -s $H/api/generate -d "{\"model\":\"$1\",\"prompt\":\"Write a long, detailed essay about the history of the steam engine.\",\"stream\":false,\"options\":{\"num_predict\":512,\"seed\":7,\"temperature\":0}}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print("  %s tokens=%d %.1f tok/s" % (sys.argv[1], d.get("eval_count",0), d.get("eval_count",0)/max(d.get("eval_duration",1),1)*1e9) if "eval_count" in d else (sys.argv[1], d))' "$1"; }
# The Pi throttles when hot, on both engines alike: note the temperature, and
# rerun after it has cooled if the rates fall from one pair to the next.
echo "  before: $(vcgencmd measure_temp 2>/dev/null) $(vcgencmd get_throttled 2>/dev/null)"
gen pi/q15-oc >/dev/null; gen pi/q15-lcpp >/dev/null
for r in 1 2 3; do gen pi/q15-oc; gen pi/q15-lcpp; done
echo "  after: $(vcgencmd measure_temp 2>/dev/null) $(vcgencmd get_throttled 2>/dev/null)"
docker logs $N 2>&1 | grep -E "using opencoti|using stock|falling back" | cut -c1-160 | sed 's/time=[^ ]* //' | sort | uniq -c | tail -4
tags=$(curl -s $H/api/tags | python3 -c 'import sys,json; print("\n".join(m["name"] for m in json.load(sys.stdin)["models"]))')
W=$(echo "$tags" | grep -i whisper | head -1)
for m in $(echo "$tags" | grep -i -E "kokoro|supertonic|kitten"); do f=out-gate/$(echo $m | tr '/:.' '___').mp3
  code=$(curl -s -o $f -w '%{http_code}' $H/v1/audio/speech -d "{\"model\":\"$m\",\"input\":\"The first ship came in at dawn.\"}")
  echo "  $m http=$code [$(file -b $f | cut -c1-30)] -> $(curl -s $H/v1/audio/transcriptions -F model="$W" -F file=@$f | cut -c1-120)"; done
docker logs $N 2>&1 | grep -c -i "signal: \|unexpectedly\|panic" | sed 's/^/crash lines: /'
docker rm -f $N >/dev/null
docker ps --format '{{.Names}}' | tr '\n' ' '; echo; curl -s --max-time 5 http://127.0.0.1:11434/api/version; echo; echo IMAGE-PI-DONE
