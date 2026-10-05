#!/bin/bash
# G5: the council gate on a side server. ENGINE=<engine file>, T=<seconds per call>.
# Each tag's server log is kept as serve-council-<stamp>.log; one server per tag.
cd /srv/ml/xc8
M=/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/xollama-council-store
X=/srv/dev-disk-by-label-opt/dev/xollama/xollama
ENGINE=${ENGINE:-/srv/ml/xc8/stage-x86_64/opencoti-llamafile-0.10.5-c8-bare.llamafile}
AS="sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS=$M XOLLAMA_HOST=127.0.0.1:22498"
for t in "$@"; do
  ss -ltnp | grep -q '127.0.0.1:22498 ' && { echo "22498 busy"; exit 1; }
  LOG=serve-council-$(date +%H%M%S).log
  (setsid nohup $AS XOLLAMA_ENGINE=opencoti XOLLAMA_ENGINE_PATH=$ENGINE OLLAMA_VULKAN=false OLLAMA_DEBUG=1 $X serve > $LOG 2>&1 &)
  for i in $(seq 60); do curl -s --max-time 2 http://127.0.0.1:22498/api/version >/dev/null && break; sleep 0.5; done
  python3 -u /srv/dev-disk-by-label-opt/dev/xollama/scripts/council-gate.py --host 127.0.0.1:22498 --timeout ${T:-600} $t | cut -c1-330
  echo "  log $LOG engine $(basename $ENGINE): pools created $(grep -a -c 'polykv: created pool' $LOG), kv refusals $(grep -a -c 'kv-reservation: REFUSED' $LOG)"
  curl -s http://127.0.0.1:22498/api/generate -d "{\"model\":\"$t\",\"keep_alive\":0}" >/dev/null; sleep 3
  P=$(ss -ltnp | sed -n 's/.*127.0.0.1:22498 .*pid=\([0-9]*\).*/\1/p' | head -1); [ -n "$P" ] && kill $P; sleep 4
done
echo COUNCIL-DONE
