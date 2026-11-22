#!/bin/bash
# Live check of Ollama Cloud accounts (plans/cloud-accounts.md) against real ollama.com.
# Usage: cloud-accounts.sh KEYFILE [DIR]  — KEYFILE holds an ollama.com API key (read, never printed);
# DIR (default /srv/ml/xcloud) holds the binary at DIR/xollama and a HOME at DIR/home, owned by ollama,
# with a server already running there on 127.0.0.1:22999. Spend: well under $0.01 on gpt-oss:20b-cloud.
set -u
D=${2:-/srv/ml/xcloud}; X=$D/xollama; H=$D/home; P=127.0.0.1:22999; U=http://$P
run() { sudo -u ollama env HOME=$H XOLLAMA_HOST=$P "$@"; }
j() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(json.dumps(d)[:600])'; }
echo "== tweak: primary excluded"; run $X tweak server cloud --primary exclude 2>&1 | tail -4
echo "== add with a wrong key"; echo "nope.wrong" | run $X tweak server cloud add bad --api-key 2>&1 | tail -2
echo "== add dummy with its API key (stdin)"; run $X tweak server cloud add dummy --api-key < "$1" 2>&1 | tail -6
echo "== key file mode"; stat -c '%a %U' $H/.ollama/xollama-cloud/*/api_key
echo "== settings hold no key"; grep -c "$(cut -c1-12 "$1")" $H/.ollama/xollama-settings.json
echo "== /api/xollama/cloud"; curl -s $U/api/xollama/cloud | j
echo "== features"; curl -s $U/api/xollama | grep -o cloud_accounts_v1
B='{"model":"gpt-oss:20b-cloud","stream":false,"think":"low","messages":[{"role":"user","content":"Write the numbers from 1 to 300 separated by spaces."}],"options":{"num_predict":1200}}'
echo "== /api/chat via the pool"; curl -s -m 120 -w ' HTTP %{http_code}\n' -d "$B" $U/api/chat | tail -c 160
echo "== /v1/chat/completions"; curl -s -m 120 -w ' HTTP %{http_code}\n' -d '{"model":"gpt-oss:20b-cloud","messages":[{"role":"user","content":"Say ok."}],"max_tokens":60}' $U/v1/chat/completions | tail -c 140
echo "== 5 at once (plan free = 3): status while they run"
for i in 1 2 3 4 5; do (curl -s -m 180 -o /dev/null -w "req$i HTTP %{http_code} %{time_total}s\n" -d "$B" $U/api/chat) & done
sleep 1.5; curl -s $U/api/xollama/cloud | python3 -c 'import json,sys; d=json.load(sys.stdin); print("in_use",d["in_use"],"queued",d["queued"],"parallel_now",d["parallel_now"])'
wait
echo "== balance route"; curl -s $U/api/xollama/cloud/balance | j
echo "== logout, then chat"; run $X tweak server cloud logout dummy 2>&1 | tail -3
curl -s -m 60 -w ' HTTP %{http_code}\n' -d "$B" $U/api/chat | tail -c 200; echo
echo "== /api/xollama/cloud after logout"; curl -s $U/api/xollama/cloud | j
echo "== login again with the key"; run $X tweak server cloud login dummy --api-key < "$1" 2>&1 | head -2
echo "== remove"; run $X tweak server cloud remove dummy -y 2>&1 | tail -3; ls $H/.ollama/xollama-cloud 2>&1 | wc -l
