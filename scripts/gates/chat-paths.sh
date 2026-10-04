#!/bin/bash
cd /srv/ml/xb140
M=/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ollama_models
ss -ltnp | grep -q '127.0.0.1:22498 ' && { echo "22498 busy"; exit 1; }
(setsid nohup sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS=$M XOLLAMA_ENGINE=opencoti XOLLAMA_ENGINE_PATH=/srv/ml/xb140/stage-x86_64/opencoti-0.10.5-c7-2610041714001 XOLLAMA_HOST=127.0.0.1:22498 OLLAMA_VULKAN=false OLLAMA_DEBUG=1 /srv/dev-disk-by-label-opt/dev/xollama/xollama serve > serve-chat.log 2>&1 &)
for i in $(seq 60); do curl -s --max-time 2 http://127.0.0.1:22498/api/version >/dev/null && break; sleep 0.5; done
python3 /srv/dev-disk-by-label-opt/dev/xollama/scripts/gates/chat-paths.py omni-council-idle:latest
grep -a -c "kv-reservation: REFUSED" serve-chat.log | sed 's/^/kv-reservation refusals: /'
grep -a -E "opencoti build" serve-chat.log | sort -u | cut -c1-140 | head -2
P=$(ss -ltnp | sed -n 's/.*127.0.0.1:22498 .*pid=\([0-9]*\).*/\1/p' | head -1); [ -n "$P" ] && kill $P
find $M/metadata ! -user ollama -o ! -perm -644 2>/dev/null | head -3 | sed 's/^/foreign metadata: /'
echo CHAT-DONE
