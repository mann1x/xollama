#!/bin/bash
# G4, decision models: /v1/systemone on a side server on the engine this tree
# pins. nimble and a Clef model (clef-flash, which also lists `vision`) must
# both be served by opencoti (the pin declares clef_score_v1), none by stock.
#   STORE=<scratch store with nimble and clef-flash> [X=<xollama in an assembled rootfs>] [W=] scripts/gates/decision.sh
. "$(dirname "$0")/side.sh"
D=$here/decision
up "${STORE:?a writable scratch store}" serve-decision.log
ask() { # ask <model> <request file> <text the answer must hold>
  out=$(sed "s/\"model\":\"[A-Za-z-]*\"/\"model\":\"$1\"/" "$D/$2" | curl -s -m 900 -w ' http=%{http_code}' http://$H/v1/systemone -H 'Content-Type: application/json' -d @-)
  case "$out" in *"$3"*"http=200") echo "PASS $1 $2: $(echo "$out" | cut -c1-200)";; *) echo "FAIL $1 $2: $(echo "$out" | cut -c1-300)";; esac
}
for m in nimble clef-flash; do
  curl -s http://$H/api/tags | python3 -c "import json,sys; c=[x.get('capabilities') for x in json.load(sys.stdin)['models'] if x['name']=='$m:latest']; print(('PASS' if c and all('decision' in k for k in c) else 'FAIL'),'$m listed with',c)"
  ask $m req.json '"choice":"bug"'
  ask $m req-multi.json '"type":"noul"'
done
ask clef-flash req-image.json '"choice":"red"'
echo "engines: opencoti loads $(grep -a -c 'opencoti build' serve-decision.log), stock loads $(grep -a 'starting llama-server' serve-decision.log | grep -c 'lib/ollama/llama-server') (want 0)"
grep -a -c "Clef decision model is served by stock" serve-decision.log | sed 's/^/clef routed to stock (want 0), lines: /'
down; echo DECISION-DONE
