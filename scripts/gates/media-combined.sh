#!/bin/bash
# An LLM and its media in one engine process, on the 3090, as `ollama`.
#   STORE=<writable scratch store with llama3 and the media models>
#   [ENG=<engine, default the pin's>] [X=<xollama>] [W=<work dir>] scripts/gates/media-combined.sh
# PASS: one runner and one engine process through chat, speech, transcription
# and an image, the same engine pid after them, speech transcribed back, and
# greedy text identical on the model with media and the plain LLM.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
: "${STORE:?a writable scratch store}"
ENG=${ENG:-$("$here/stage-engine.sh" x86_64)}; X=${X:-$repo/xollama}; W=${W:-/srv/ml/gates/run/media-combined}
H=http://127.0.0.1:22498; mkdir -p "$W"; chmod 777 "$W"
ss -ltn | grep -q '127.0.0.1:22498 ' && { echo "22498 busy"; exit 1; }
fail=0; check() { if [ "$2" = "$3" ]; then echo "  ok   $1 ($2)"; else echo "  FAIL $1: got '$2', want '$3'"; fail=1; fi; }
sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS="$STORE" XOLLAMA_ENGINE=opencoti \
  XOLLAMA_ENGINE_PATH="$ENG" XOLLAMA_HOST=127.0.0.1:22498 OLLAMA_VULKAN=false OLLAMA_DEBUG=1 OLLAMA_KEEP_ALIVE=10m \
  "$X" serve > "$W/serve.log" 2>&1 &
trap 'for p in $(pgrep -f "^$X serve"); do sudo kill "$p"; done; sleep 2' EXIT
for i in $(seq 120); do curl -s --max-time 2 $H/api/version >/dev/null && break; sleep 0.5; done
echo "engine $(basename "$ENG")"; curl -s $H/api/version; echo
show() { curl -s $H/api/show -d "{\"model\":\"$1\"}"; }
python3 - "$W/create.json" <<PY
import json,subprocess,sys
def x(m): return json.loads(subprocess.check_output(["curl","-s","$H/api/show","-d",json.dumps({"model":m})]))["xollama"]
tts,img,stt=x("mannix/kokoro:82m"),x("mannix/flux2-klein:4b"),x("mannix/whisper:large-v3-turbo")
media={**tts["media"],**img["media"],**stt["media"]}
json.dump({"model":"gate-combined","from":"llama3:latest","xollama":{"version":max(tts["version"],img["version"],stt["version"]),"media":media}},open(sys.argv[1],"w"))
PY
curl -s $H/api/create -d @"$W/create.json" | tail -1; echo
P="Write a detailed essay about lighthouses and the people who kept them."
gen() { curl -s --max-time 300 $H/api/generate -d "{\"model\":\"$1\",\"prompt\":\"$P\",\"stream\":false,\"keep_alive\":\"${3:-10m}\",\"options\":{\"temperature\":0,\"seed\":1,\"num_predict\":256,\"num_ctx\":8192}}" > "$W/$2.json"
  python3 -c "import json,sys; d=json.load(open('$W/$2.json')); print('  $2: %d tokens %.1f tok/s'%(d.get('eval_count',0),d.get('eval_count',0)/max(d.get('eval_duration',1),1)*1e9)) if 'response' in d else print('  $2: ERROR',d)"; }
text() { python3 -c "import json; print(json.load(open('$W/$1.json')).get('response',''))" | sha256sum | cut -c1-16; }
runners() { curl -s $H/api/ps | python3 -c "import sys,json; print(len(json.load(sys.stdin)['models']))"; }
engines() { pgrep -u ollama -f opencoti-llamafile | sort -n | tr '\n' ' ' | sed 's/ $//'; }
speech() { curl -s --max-time 180 -o "$W/$1.mp3" -w '%{http_code}' $H/v1/audio/speech -d '{"model":"gate-combined","input":"The first ship came in at dawn."}'; }
hear() { curl -s --max-time 180 $H/v1/audio/transcriptions -F model=gate-combined -F file=@"$W/$1.mp3" | python3 -c "import sys,json; print(json.load(sys.stdin).get('text',''))"; }
image() { curl -s --max-time 280 -o "$W/image.json" -w '%{http_code}' $H/v1/images/generations -d '{"model":"gate-combined","prompt":"a red lighthouse","size":"512x512"}'; }
# Greedy text differs between a first run and a repeat of one prompt, media or
# not (the repeat reuses the prefix cache), so each is compared with its like.
echo "== plain LLM"; gen llama3:latest plain; gen llama3:latest repeat 0; sleep 5
echo "== the model with media"; gen gate-combined chat
check "text beside loaded media = plain" "$(text chat)" "$(text plain)"
check "runners" "$(runners)" 1
pid=$(engines); check "one engine process" "$(echo "$pid" | wc -w)" 1
tr '\0' ' ' < /proc/$pid/cmdline | grep -oE -- '--(tts|stt|diffusion)-model|-m |--model ' | sort -u | tr '\n' ' '; echo
echo "== speech, a default-options call on a loaded num_ctx 8192"
check "speech http" "$(speech s1)" 200
t=$(hear s1); echo "  heard: $t"; check "transcript" "$(echo "$t" | tr 'A-Z' 'a-z' | grep -c 'first ship came in at dawn')" 1
echo "== image while the LLM generates"
gen gate-combined during & g=$!
check "image http" "$(image)" 200; wait $g
python3 -c "import json,base64; d=json.load(open('$W/image.json')); print('  image bytes', len(base64.b64decode(d['data'][0]['b64_json'])))"
check "same engine, not reloaded" "$(engines)" "$pid"
check "runners" "$(runners)" 1
gen gate-combined after
check "repeat after media = plain repeat" "$(text after)" "$(text repeat)"
echo "== a media-only model still has its own engine"
code=$(curl -s --max-time 180 -o "$W/own.mp3" -w '%{http_code}' $H/v1/audio/speech -d '{"model":"mannix/kokoro:82m","input":"The first ship came in at dawn."}')
check "kokoro http" "$code" 200; check "runners" "$(runners)" 2
grep -acE 'SIGSEGV|segmentation fault|terminate called' "$W/serve.log" | sed 's/^/crash lines: /'
[ $fail = 0 ] && echo MEDIA-COMBINED-PASS || echo MEDIA-COMBINED-FAIL
exit $fail
