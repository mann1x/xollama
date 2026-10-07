#!/bin/bash
# G4, decision models: /v1/systemone on a side server whose lib/ollama is the
# fork's pinned stock runtime (llama/runtime-pin-linux.txt, it carries the Clef
# head) and whose engine is the pinned opencoti. nimble must stay on opencoti,
# a Clef model must be served by stock while the pin has no clef_score_v1.
# Run as root; the server runs as `ollama`. Models: nimble, clef-flash.
D=/srv/ml/xc9/decision                       # stock/bin/xollama + stock/lib/ollama, req*.json
M=/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ollama_models
H=127.0.0.1:22497
cd $D || exit 1
ss -ltnp | grep -q "$H " && { echo "22497 busy"; exit 1; }
(setsid nohup sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS=$M XOLLAMA_ENGINE=opencoti XOLLAMA_ENGINE_PATH=/srv/ml/xc9/stage-x86_64/opencoti-llamafile-0.10.5-c9-bare.llamafile XOLLAMA_HOST=$H OLLAMA_VULKAN=false OLLAMA_DEBUG=1 $D/stock/bin/xollama serve > serve-gate.log 2>&1 &)
for i in $(seq 60); do curl -s --max-time 2 http://$H/api/version >/dev/null && break; sleep 0.5; done
ask() { # ask <model> <request file> <text the answer must hold>
  out=$(sed "s/\"model\":\"[A-Za-z-]*\"/\"model\":\"$1\"/" $2 | curl -s -m 900 -w ' http=%{http_code}' http://$H/v1/systemone -H 'Content-Type: application/json' -d @-)
  case "$out" in *"$3"*"http=200") echo "PASS $1 $2: $(echo "$out" | cut -c1-200)";; *) echo "FAIL $1 $2: $(echo "$out" | cut -c1-300)";; esac
}
for m in nimble clef-flash; do
  curl -s http://$H/api/tags | python3 -c "import json,sys; c=[x.get('capabilities') for x in json.load(sys.stdin)['models'] if x['name']=='$m:latest']; print(('PASS' if c and all('decision' in k for k in c) else 'FAIL'),'$m listed with',c)"
  ask $m req.json '"choice":"bug"'
  ask $m req-multi.json '"type":"noul"'
done
ask clef-flash req-image.json '"choice":"red"'
echo "engines: opencoti loads $(grep -a -c 'opencoti build' serve-gate.log), stock loads $(grep -a 'starting llama-server' serve-gate.log | grep -c 'lib/ollama/llama-server')"
grep -a -c "Clef decision model is served by stock" serve-gate.log | sed 's/^/clef routed to stock, lines: /'
P=$(ss -ltnp | sed -n "s/.*$H .*pid=\([0-9]*\).*/\1/p" | head -1); [ -n "$P" ] && kill $P
echo DECISION-DONE
