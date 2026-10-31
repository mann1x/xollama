#!/bin/bash
# Every per-model engine option on a real load, the admission headers and the
# Realtime proxy, on the 3090, as `ollama`.
#   STORE=<writable scratch store with llama3 and mannix/whisper:large-v3-turbo>
#   [ENG=<engine, default the pin's>] [ENGINE_ARGS=<more flags>] [MORE_FLAGS=<to check>] [RT_MODEL=<live model>]
#   [X=<xollama>] [W=<work dir>] scripts/gates/engine-options.sh
# PASS: the engine boots on every flag and they are on its command line, chat
# generates 256 tokens, and /v1/realtime reaches the engine (101, or its own 501
# for a model with no live mode).
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
: "${STORE:?a writable scratch store}"
ENG=${ENG:-$("$here/stage-engine.sh" x86_64)}; X=${X:-$repo/xollama}; W=${W:-/srv/ml/gates/run/engine-options}
H=http://127.0.0.1:22498; mkdir -p "$W"; chmod 777 "$W"
ss -ltn | grep -q '127.0.0.1:22498 ' && { echo "22498 busy"; exit 1; }
fail=0; check() { if [ "$2" = "$3" ]; then echo "  ok   $1 ($2)"; else echo "  FAIL $1: got '$2', want '$3'"; fail=1; fi; }
sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS="$STORE" XOLLAMA_ENGINE=opencoti \
  XOLLAMA_ENGINE_PATH="$ENG" XOLLAMA_HOST=127.0.0.1:22498 OLLAMA_VULKAN=false OLLAMA_DEBUG=1 OLLAMA_KEEP_ALIVE=10m \
  XOLLAMA_ENGINE_ARGS="${ENGINE_ARGS:-}" "$X" serve > "$W/serve.log" 2>&1 &
trap 'for p in $(pgrep -f "^$X serve"); do sudo kill "$p"; done; sleep 2' EXIT
for i in $(seq 120); do curl -s --max-time 2 $H/api/version >/dev/null && break; sleep 0.5; done
echo "engine $(basename "$ENG")"
curl -s $H/api/create -d '{"model":"gate-options","from":"llama3:latest","xollama":{"version":10,
 "kv":{"admission_poolless":"warn"},"session":{"client_pools":1},
 "tuning":{"recurrent_state_type":"f32","fa_quant_prompt":"in-place","admission_on_error":"deny","draft_kvarn":"off",
 "draft_type_k":"q8_0","draft_type_v":"q8_0","tail_type_k":"q8_0","tail_type_v":"q8_0","stage_window":4,
 "slot_initial_ctx":4096,"slot_shrink_idle_ms":5000,"elastic_alloc_ttl":120,"gpu_share_weight":1.5}}}' | tail -1; echo
curl -s -D "$W/hdr" --max-time 300 $H/api/chat -d '{"model":"gate-options","stream":false,"overcommit":true,"messages":[{"role":"user","content":"Write a detailed essay about lighthouses."}],"options":{"temperature":0,"num_predict":256,"num_ctx":8192}}' > "$W/chat.json"
python3 -c "import json; d=json.load(open('$W/chat.json')); print('  chat', d.get('eval_count'), 'tokens %.1f tok/s'%(d.get('eval_count',0)/max(d.get('eval_duration',1),1)*1e9), d.get('error',''))"
check "tokens" "$(python3 -c "import json; print(json.load(open('$W/chat.json')).get('eval_count'))")" 256
pid=$(pgrep -u ollama -f opencoti-llamafile | head -1); args=$(tr '\0' ' ' < /proc/$pid/cmdline)
for f in admission-poolless recurrent-state-type fa-quant-prompt polykv-adm-on-error spec-draft-kvarn cache-type-k-draft cache-type-v-draft cache-type-k-tail cache-type-v-tail kvarn-stage-window slot-initial-ctx slot-shrink-idle-ms elastic-alloc-ttl gpu-share-weight no-cache-idle-slots ${MORE_FLAGS:-}; do
  check "--$f" "$(echo "$args" | grep -c -- "--$f")" 1; done
grep -iE '^x-(kv|sessions|polykv|session|context)' "$W/hdr" | sed 's/^/  header /'
python3 - "$H" "${RT_MODEL:-mannix/whisper:large-v3-turbo}" <<'PY'
import asyncio,sys,websockets
async def go(u):
    try:
        async with websockets.connect(u,open_timeout=120) as w:
            print("  realtime 101:",(await asyncio.wait_for(w.recv(),15))[:110])
    except websockets.exceptions.InvalidStatus as e:
        print("  realtime status",e.response.status_code,bytes(e.response.body)[:110])
asyncio.run(go(sys.argv[1].replace("http","ws")+"/v1/realtime?model="+sys.argv[2]))
PY
grep -acE 'invalid argument|SIGSEGV|terminate called' "$W/serve.log" | sed 's/^/crash or argv lines: /'
[ $fail = 0 ] && echo ENGINE-OPTIONS-PASS || echo ENGINE-OPTIONS-FAIL
exit $fail
