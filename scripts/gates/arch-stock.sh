#!/bin/bash
# Targeted check for the arch-engine rule (llm/engine/arch.go): a GGUF
# architecture the pinned opencoti lacks (lfm3) is served by stock
# llama-server with the DEFAULT engine, said once in the log; the same model
# pinned to engine=opencoti is refused by the engine (the control). Also:
# repeat_last_n -1 on both engines, LFM2.5 on opencoti (upstream's
# add_bos_token change), and LFM3-Audio speech through /tts.
# The side server sets no XOLLAMA_ENGINE (serve-side.sh forces opencoti).
#   STORE=<scratch store> X=<xollama in an assembled rootfs> [OUT=<dir>] scripts/gates/arch-stock.sh
set -uo pipefail
H=127.0.0.1:22498; X=${X:?an assembled xollama}; STORE=${STORE:?a writable scratch store}
OUT=${OUT:-/srv/ml/gates/run/arch}; mkdir -p "$OUT"; install -d -o ollama "$STORE"
HOMEDIR=/srv/ml/xollama-phase2/as-ollama/home; LOG=$OUT/serve.log
LFM3=hf.co/LiquidAI/LFM3-1.7B-GGUF:Q4_K_M
LFM25=hf.co/LiquidAI/LFM2.5-1.2B-Instruct-GGUF:Q4_K_M
# Speech is granted by the model config, as the library model has it; a raw
# hf.co GGUF has none and is "does not support speech" on upstream too.
AUDIO=lfm3-audio
cli() { sudo -u ollama env HOME=$HOMEDIR XOLLAMA_HOST=$H "$X" "$@"; }

ss -ltn | grep -q "$H " && { echo "22498 busy"; exit 1; }
(setsid nohup sudo -u ollama env HOME=$HOMEDIR OLLAMA_MODELS="$STORE" XOLLAMA_HOST=$H \
  OLLAMA_VULKAN=false OLLAMA_DEBUG=1 OLLAMA_KEEP_ALIVE=5m "$X" serve > "$LOG" 2>&1 &)
for i in $(seq 240); do curl -s --max-time 2 http://$H/api/version >/dev/null && break; sleep 0.5; done
echo "server: $(curl -s http://$H/api/version)"

# 256 tokens, then the engine that serves the model now.
gen() { # model prompt [options-json] [request-json]
  curl -s http://$H/api/generate -d "{\"model\":\"$1\",\"prompt\":\"$2\",\"stream\":false,${4:+$4,}\"options\":{\"num_predict\":256,\"temperature\":0${3:+,$3}}}" |
    python3 -c '
import json,sys
r=json.load(sys.stdin)
if "error" in r: print("  ERROR:", r["error"][:300]); sys.exit(0)
tps=r["eval_count"]/(r["eval_duration"]/1e9) if r.get("eval_duration") else 0
print("  done=%s eval=%d tok/s=%.1f text=%r" % (r.get("done_reason"), r.get("eval_count",0), tps, r["response"][:140]))'
}
engine_now() {
  if pgrep -f "$(dirname "$X")/../lib/ollama/llama-server|lib/ollama/llama-server" >/dev/null; then echo "  engine: stock llama-server"; fi
  if pgrep -f 'opencoti-llamafile' >/dev/null; then echo "  engine: opencoti"; fi
}
unload() { curl -s http://$H/api/generate -d "{\"model\":\"$1\",\"keep_alive\":0}" >/dev/null; sleep 3; }

echo "== 1. LFM3 on the default engine (want: stock llama-server, an answer)"
cli pull "$LFM3" >/dev/null 2>&1 || echo "  pull failed"
# LFM3 is a thinking model: think:false puts the 256 tokens in the response.
gen "$LFM3" "Explain in detail how a bicycle gear system works." "" '"think":false'
engine_now
grep -a "does not serve its architecture" "$LOG" | sed 's/^/  log: /' | cut -c1-260

echo "== 4. repeat_last_n -1 on stock (LFM3)"
gen "$LFM3" "List the planets of the solar system with one fact each." '"repeat_last_n":-1' '"think":false'
unload "$LFM3"

echo "== 2. control: LFM3 pinned engine=opencoti (want: refused by c13)"
cli cp "$LFM3" lfm3-oc >/dev/null 2>&1
cli tweak model lfm3-oc --engine=opencoti -y >/dev/null 2>&1 || echo "  tweak failed"
gen lfm3-oc "Say hello." "" '"think":false'
grep -a -i "unknown model architecture\|lfm3" "$LOG" | grep -a -v "does not serve its architecture" | tail -3 | sed 's/^/  log: /' | cut -c1-260
unload lfm3-oc

echo "== 3. LFM2.5 on opencoti (want: opencoti, a correct answer)"
cli pull "$LFM25" >/dev/null 2>&1 || echo "  pull failed"
gen "$LFM25" "What is the capital of France? Then describe the city in a few paragraphs."
engine_now
echo "== 4. repeat_last_n -1 on opencoti (LFM2.5)"
gen "$LFM25" "List the planets of the solar system with one fact each." '"repeat_last_n":-1'
unload "$LFM25"

echo "== 5. LFM3-Audio speech through /v1/audio/speech (want: audio from stock /tts)"
cli pull "$AUDIO" >/dev/null 2>&1 || echo "  pull failed"
code=$(curl -s -o "$OUT/speech.wav" -w '%{http_code}' http://$H/v1/audio/speech \
  -d "{\"model\":\"$AUDIO\",\"input\":\"Hello from xOllama. This sentence was spoken by LFM3 on the stock engine.\",\"response_format\":\"wav\"}")
echo "  http=$code bytes=$(stat -c %s "$OUT/speech.wav" 2>/dev/null) type=$(file -b "$OUT/speech.wav" | cut -c1-80)"
if [ "$code" = 200 ]; then
  echo "  heard: $(curl -s http://$H/v1/audio/transcriptions -F model="$AUDIO" -F file=@"$OUT/speech.wav" | head -c 200)"
else head -c 300 "$OUT/speech.wav"; echo; fi
engine_now
unload "$AUDIO"

P=$(ss -ltnp | sed -n "s/.*$H .*pid=\([0-9]*\).*/\1/p" | head -1); [ -n "$P" ] && kill "$P"
for i in $(seq 60); do ss -ltn | grep -q "$H " || break; sleep 1; done
# Not crashes: a server stopping its engine, the control's refusal, and
# opencoti declining a device while listing (logged at Warn by design).
echo "crash lines: $(grep -a -i 'exit status\|signal: \|unexpectedly' "$LOG" | grep -a -v -e 'stopped as asked' -e 'device enumeration' -e 'refused every device' -e 'unknown model architecture' | grep -c '')"
echo ARCH-GATE-DONE
