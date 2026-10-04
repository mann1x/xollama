#!/bin/bash
cd /srv/ml/xb140
M=/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/xollama-council-store
X=/srv/dev-disk-by-label-opt/dev/xollama/xollama; LOG=serve-council3.log
ss -ltnp | grep -q '127.0.0.1:22498 ' && { echo "22498 busy"; exit 1; }
AS="sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS=$M XOLLAMA_HOST=127.0.0.1:22498"
(setsid nohup $AS XOLLAMA_ENGINE=opencoti XOLLAMA_ENGINE_PATH=/srv/ml/xb140/stage-x86_64/opencoti-0.10.5-c7-2610041714001 OLLAMA_VULKAN=false OLLAMA_DEBUG=1 $X serve > $LOG 2>&1 &)
for i in $(seq 60); do curl -s --max-time 2 http://127.0.0.1:22498/api/version >/dev/null && break; sleep 0.5; done
for t in "$@"; do
  n0=$(wc -l < $LOG)
  python3 -u /srv/dev-disk-by-label-opt/dev/xollama/scripts/council-gate.py --host 127.0.0.1:22498 --timeout ${T:-600} $t | cut -c1-330
  tail -n +$n0 $LOG | grep -a -c "polykv: created pool" | sed "s/^/  pools created: /"
  tail -n +$n0 $LOG | grep -a -c "kv-reservation: REFUSED" | sed "s/^/  kv refusals: /"
  curl -s http://127.0.0.1:22498/api/generate -d "{\"model\":\"$t\",\"keep_alive\":0}" >/dev/null; sleep 4
done
P=$(ss -ltnp | sed -n 's/.*127.0.0.1:22498 .*pid=\([0-9]*\).*/\1/p' | head -1); [ -n "$P" ] && kill $P
echo COUNCIL3-DONE
