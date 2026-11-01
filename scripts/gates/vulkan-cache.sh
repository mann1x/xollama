#!/bin/bash
# The engine keeps its Vulkan pipeline cache in the stable folder, as `ollama`, on
# solidPC's Renoir iGPU: its only Vulkan device, refused unless the old driver is allowed.
#   STORE=<writable scratch store with llama3> [ENG=<engine, default the pin's>] [X=<xollama>] [W=<work dir>]
#   scripts/gates/vulkan-cache.sh
# PASS: both engines generate 256 tokens on Vulkan and the cache is in the stable
# folder, not under the payload HOME. Mesa 20 RADV stores only a 32-byte header,
# so a warm boot cannot show here: that is checked on eleven2go at the candidate.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
: "${STORE:?a writable scratch store}"
ENG=${ENG:-$("$here/stage-engine.sh" x86_64)}; X=${X:-$repo/xollama}; W=${W:-/srv/ml/gates/run/vulkan-cache}
H=http://127.0.0.1:22498; HM=/srv/ml/gates/run/vulkan-cache-home; mkdir -p "$W" "$HM"; chmod 777 "$W"; chown ollama "$HM"
sudo -u ollama rm -rf "$HM/.ollama/engines/vulkan-cache"
ss -ltn | grep -q '127.0.0.1:22498 ' && { echo "22498 busy"; exit 1; }
fail=0
sudo -u ollama env HOME="$HM" OLLAMA_MODELS="$STORE" XOLLAMA_ENGINE=opencoti XOLLAMA_ENGINE_PATH="$ENG" \
  XOLLAMA_HOST=127.0.0.1:22498 OLLAMA_VULKAN=true OLLAMA_DEBUG=2 OLLAMA_KEEP_ALIVE=10m \
  OPENCOTI_VK_ALLOW_OLD_DRIVER=1 CUDA_VISIBLE_DEVICES=-1 "$X" serve > "$W/serve.log" 2>&1 &
trap 'for p in $(pgrep -f "^$X serve"); do sudo kill "$p"; done; sleep 2' EXIT
for i in $(seq 120); do curl -s --max-time 2 $H/api/version >/dev/null && break; sleep 0.5; done
for run in 1 2; do
  curl -s --max-time 300 $H/api/chat -d '{"model":"llama3:latest","stream":false,"messages":[{"role":"user","content":"Write a detailed essay about lighthouses."}],"options":{"temperature":0,"num_predict":256,"num_ctx":8192}}' > "$W/chat$run.json"
  python3 -c "import json,time; d=json.load(open('$W/chat$run.json')); print('  run $run:', d.get('eval_count'), 'tokens %.1f tok/s, load %.1f s'%(d.get('eval_count',0)/max(d.get('eval_duration',1),1)*1e9, d.get('load_duration',0)/1e9), d.get('error',''))"
  [ "$(python3 -c "import json; print(json.load(open('$W/chat$run.json')).get('eval_count'))")" = 256 ] || fail=1
  curl -s $H/api/generate -d '{"model":"llama3:latest","keep_alive":0}' >/dev/null
  for i in $(seq 60); do [ "$(curl -s $H/api/ps | grep -c '"name"')" = 0 ] && break; sleep 0.5; done; sleep 3
done
d="$HM/.ollama/engines/vulkan-cache"; ls "$d"/pipeline-*.bin >/dev/null 2>&1 || { echo "  FAIL no pipeline cache in $d"; fail=1; }
[ -z "$(find /srv/ml/xollama-phase2/as-ollama "$HM/.ollama/engines/payload" -path '*/.cache/opencoti/vulkan/*' 2>/dev/null | head -1)" ] || { echo "  FAIL a cache under the payload HOME"; fail=1; }
ls -la "$HM/.ollama/engines/vulkan-cache" | sed 's/^/  /'
[ $fail = 0 ] && echo VULKAN-CACHE-PASS || echo VULKAN-CACHE-FAIL
exit $fail
