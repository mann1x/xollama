#!/bin/bash
# Sound effects through xollama on the pinned engine, as `ollama`, on the 3090.
#   STORE=<writable scratch store with llama3> SFXGGUF=<stable-audio-3-small-sfx-q8_0.gguf>
#   [ENG=<engine, default the pin's>] [X=<xollama>] [W=<work dir>] scripts/gates/sfx.sh
# PASS: the catalog entry builds from a mirror; its clip is real audio read from
# the WAV itself (44.1 kHz stereo, the asked length, not silence), the same for
# a seed and different for another; mp3, pcm and json have their shapes; the
# X-OC headers reach the client; a length past the ceiling is the engine's 400;
# two requests at once both answer; and beside an LLM one runner and one engine
# process serve a clip while the text stays the plain LLM's and decode holds.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
: "${STORE:?a writable scratch store}"; : "${SFXGGUF:?the Q8_0 package}"
ENG=${ENG:-$("$here/stage-engine.sh" x86_64)}; X=${X:-$repo/xollama}; W=${W:-/srv/ml/gates/run/sfx}
H=http://127.0.0.1:22497; M=stable-audio-3-small-sfx-q8_0
mkdir -p "$W"; chmod 777 "$W"
ss -ltn | grep -q '127.0.0.1:22497 ' && { echo "22497 busy"; exit 1; }
fail=0; check() { if [ "$2" = "$3" ]; then echo "  ok   $1 ($2)"; else echo "  FAIL $1: got '$2', want '$3'"; fail=1; fi; }
asu() { sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS="$STORE" XOLLAMA_ENGINE=opencoti \
  XOLLAMA_ENGINE_PATH="$ENG" XOLLAMA_HOST=127.0.0.1:22497 OLLAMA_VULKAN=false OLLAMA_DEBUG=1 OLLAMA_KEEP_ALIVE=10m "$@"; }
asu "$X" serve > "$W/serve.log" 2>&1 &
trap 'for p in $(pgrep -f "^$X serve"); do sudo kill "$p"; done; sleep 2' EXIT
for i in $(seq 120); do curl -s --max-time 2 $H/api/version >/dev/null && break; sleep 0.5; done
echo "engine $(basename "$ENG")"; curl -s $H/api/version; echo

echo "== the catalog entry, from a mirror"
mirror=$W/mirror/audio-cpp/audio.cpp-gguf/Stable-Audio-3-Small-SFX-GGUF
sudo -u ollama mkdir -p "$mirror"; sudo -u ollama ln -sf "$SFXGGUF" "$mirror/$(basename "$SFXGGUF")"
asu "$X" media create "$M" --dir "$W/mirror" 2>&1 | tail -2
caps=$(curl -s $H/api/show -d "{\"model\":\"$M\"}" | python3 -c "import sys,json; print(' '.join(json.load(sys.stdin).get('capabilities',[])))")
check "capability" "$caps" sound_effects
[ "$caps" = sound_effects ] || { echo "FAIL: no model to measure"; exit 1; }

clip() { # name, json fields
  curl -s --max-time 200 -D "$W/$1.h" -o "$W/$1.out" -w '%{http_code}' $H/v1/audio/generations -d "{\"model\":\"${MODEL:-$M}\",\"prompt\":\"a short wooden click\",$2}"; }
sha() { sha256sum "$W/$1.out" | cut -c1-16; }
hdr() { tr -d '\r' < "$W/$1.h" | awk -v h="$2:" 'tolower($1)==tolower(h) {print $2}'; }
wavcheck() { python3 - "$W/$1.out" "$2" <<'PY'
import sys,wave,array,math
w=wave.open(sys.argv[1]); want=round(float(sys.argv[2])*44100)
r,c,b,n=w.getframerate(),w.getnchannels(),w.getsampwidth(),w.getnframes()
s=array.array('h',w.readframes(n)); rms=math.sqrt(sum(x*x for x in s)/max(len(s),1))
peak=max(abs(x) for x in s); zc=sum(1 for i in range(2,len(s),2) if (s[i]<0)!=(s[i-2]<0))
print(f"{r} {c} {b*8} {'exact' if n==want else n} {'sound' if rms>30 and peak>300 and zc>10 else 'SILENT'}")
print(f"  rms {rms:.0f} peak {peak} crossings {zc}",file=sys.stderr)
PY
}

echo "== a clip on its own engine"
t0=$(date +%s.%N); check "first clip http" "$(clip a '"seconds":2,"seed":42,"response_format":"wav"')" 200
t1=$(date +%s.%N); check "same seed http" "$(clip b '"seconds":2,"seed":42,"response_format":"wav"')" 200
t2=$(date +%s.%N); printf '  load + clip %.1f s, loaded clip %.1f s\n' "$(echo "$t1-$t0"|bc)" "$(echo "$t2-$t1"|bc)"
check "the WAV" "$(wavcheck a 2)" "44100 2 16 exact sound"
head -c4 "$W/a.out" | grep -q RIFF || { echo "FAIL: no clip to measure: $(head -c 300 "$W/a.out")"; exit 1; }
check "same seed, same clip" "$(sha b)" "$(sha a)"
clip c '"seconds":2,"seed":7,"response_format":"wav"' >/dev/null
[ "$(sha c)" != "$(sha a)" ] && echo "  ok   another seed, another clip" || { echo "  FAIL another seed gave the same clip"; fail=1; }
check "X-OC-Seed" "$(hdr a X-OC-Seed)" 42
check "X-OC-Sample-Rate" "$(hdr a X-OC-Sample-Rate)" 44100
check "X-OC-Channels" "$(hdr a X-OC-Channels)" 2
check "X-OC-Seconds" "$(python3 -c "print(float('$(hdr a X-OC-Seconds)'))")" 2.0
clip d '"seconds":1' >/dev/null; [ -n "$(hdr d X-OC-Seed)" ] && echo "  ok   a seed not asked for comes back ($(hdr d X-OC-Seed))" || { echo "  FAIL no seed came back"; fail=1; }
check "default format" "$(hdr d Content-Type)" audio/mpeg

echo "== formats and refusals"
clip m '"seconds":1,"seed":1,"response_format":"mp3"' >/dev/null
check "mp3 frame sync" "$(python3 -c "b=open('$W/m.out','rb').read(); print(int(b[:3]==b'ID3' or (b[0]==255 and b[1]&0xE0==0xE0)))")" 1
clip p '"seconds":1,"seed":1,"response_format":"pcm"' >/dev/null
check "pcm bytes" "$(stat -c %s "$W/p.out")" 176400
clip j '"seconds":1,"seed":1,"response_format":"json"' >/dev/null
check "json round trip" "$(python3 -c "import json,base64; d=json.load(open('$W/j.out')); print(d['sample_rate'],d['channels'],int(len(base64.b64decode(d['b64_json']))>1000))")" "44100 2 1"
check "past the ceiling" "$(clip r '"seconds":999')" 400
echo "== two at once: the queue, not a 503"
(clip q1 '"seconds":1,"seed":2,"response_format":"wav"' > "$W/q1.code") & (clip q2 '"seconds":1,"seed":3,"response_format":"wav"' > "$W/q2.code") & wait %2 %3 2>/dev/null; sleep 0.2
check "both answered" "$(cat "$W/q1.code") $(cat "$W/q2.code")" "200 200"
cmd=$(pgrep -u ollama -f opencoti-llamafile | head -1 | xargs -I{} sh -c "tr '\0' ' ' < /proc/{}/cmdline")
echo "$cmd" | grep -oE -- '--(sfx-[a-z-]+|gpu [a-z]+)' | sort -u | tr '\n' ' '; echo
curl -s $H/api/ps | python3 -c "import sys,json; [print('  ',m['name'],'size',m['size'],'vram',m['size_vram']) for m in json.load(sys.stdin)['models']]"
curl -s $H/api/generate -d "{\"model\":\"$M\",\"keep_alive\":0}" >/dev/null; asu "$X" stop "$M" >/dev/null 2>&1; sleep 3

echo "== beside an LLM, one process"
python3 - "$W/create.json" "$H" "$M" <<'PY'
import json,subprocess,sys
x=json.loads(subprocess.check_output(["curl","-s",sys.argv[2]+"/api/show","-d",json.dumps({"model":sys.argv[3]})]))["xollama"]
json.dump({"model":"gate-sfx","from":"llama3:latest","xollama":x},open(sys.argv[1],"w"))
PY
curl -s $H/api/create -d @"$W/create.json" | tail -1; echo
P="Write a detailed essay about lighthouses and the people who kept them."
gen() { curl -s --max-time 300 $H/api/generate -d "{\"model\":\"$1\",\"prompt\":\"$P\",\"stream\":false,\"keep_alive\":\"${3:-10m}\",\"options\":{\"temperature\":0,\"seed\":1,\"num_predict\":256,\"num_ctx\":8192}}" > "$W/$2.json"
  python3 -c "import json; d=json.load(open('$W/$2.json')); print('  $2: %d tokens %.1f tok/s'%(d.get('eval_count',0),d.get('eval_count',0)/max(d.get('eval_duration',1),1)*1e9)) if 'response' in d else print('  $2: ERROR',d)"; }
text() { python3 -c "import json; print(json.load(open('$W/$1.json')).get('response',''))" | sha256sum | cut -c1-16; }
rate() { python3 -c "import json; d=json.load(open('$W/$1.json')); print(d.get('eval_count',0)/max(d.get('eval_duration',1),1)*1e9)"; }
runners() { curl -s $H/api/ps | python3 -c "import sys,json; print(len(json.load(sys.stdin)['models']))"; }
engines() { pgrep -u ollama -f opencoti-llamafile | sort -n | tr '\n' ' ' | sed 's/ $//'; }
gen llama3:latest plain; sleep 3; curl -s $H/api/generate -d '{"model":"llama3:latest","keep_alive":0}' >/dev/null; sleep 5
gen gate-sfx chat
check "text beside loaded sound effects = plain" "$(text chat)" "$(text plain)"
check "runners" "$(runners)" 1
pid=$(engines); check "one engine process" "$(echo "$pid" | wc -w)" 1
gen gate-sfx idle
MODEL=gate-sfx
gen gate-sfx during & g=$!; sleep 0.3
check "clip during decode http" "$(clip g '"seconds":8,"seed":42,"response_format":"wav"')" 200; wait $g
check "the combined WAV" "$(wavcheck g 8)" "44100 2 16 exact sound"
check "same engine, not reloaded" "$(engines)" "$pid"; check "runners" "$(runners)" 1
python3 -c "i,d=$(rate idle),$(rate during); print('  decode %.1f idle, %.1f beside a clip, %.0f %%'%(i,d,d/i*100)); raise SystemExit(0 if d/i>=0.9 else 1)" || { echo "  FAIL decode below 90 %"; fail=1; }
echo "crashes: $(grep -c 'signal: \|SIGSEGV\|panic' "$W/serve.log")"
[ $fail = 0 ] && echo "PASS" || echo "FAIL"; exit $fail
