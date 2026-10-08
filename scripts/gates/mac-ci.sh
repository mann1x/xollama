#!/bin/bash
# G11 on the app the release workflow built. On the Mac mini: put the draft's
# xOllama-darwin.zip and xOllama.dmg (checked against sha256sum.txt) in
# ~/dev/xollama/ci-app, then run this. It unpacks the zip the updater takes and
# runs the bundle's own server on the side port; bench.sh and speech-mac.sh are
# the ones beside it.
cd ~/dev/xollama/ci-app || exit 1
lsof -nP -iTCP:22498 -sTCP:LISTEN >/dev/null && { echo "22498 busy"; exit 1; }
# Always the zip just downloaded, never the app of an earlier draft.
rm -rf xOllama.app
ditto -x -k xOllama-darwin.zip .
spctl -a -vv xOllama.app 2>&1 | head -2
spctl -a -t open --context context:primary-signature -vv xOllama.dmg 2>&1 | head -2
xcrun stapler validate xOllama.app | tail -1; xcrun stapler validate xOllama.dmg | tail -1
codesign --verify --deep --strict xOllama.app && echo "codesign ok"
B=xOllama.app/Contents/Resources
otool -l $B/xollama | grep -A3 LC_BUILD_VERSION | grep -E 'minos|sdk' | tr '\n' ' '; echo
ls $B | grep -E "mlx_metal" | tr '\n' ' '; ls $B/mlx_metal_v4 2>/dev/null | tr '\n' ' '; echo
XOLLAMA_HOST=127.0.0.1:22498 OLLAMA_DEBUG=1 nohup $B/xollama serve > ../ci-serve.log 2>&1 &
SP=$!
for i in $(seq 60); do curl -s --max-time 2 http://127.0.0.1:22498/api/version && break; sleep 0.5; done; echo
cd ~/dev/xollama
H=127.0.0.1:22498 ./bench.sh qwen2.5:1.5b llama3:latest
grep -a -E "using opencoti|falling back" ci-serve.log | cut -c1-220 | sort | uniq -c | tail -3
grep -a -q -E "falling back|no stock llama-server on this platform" ci-serve.log && echo "FAIL: a load did not get opencoti (macOS has no stock llama-server)"
grep -a -E "opencoti build|offloaded [0-9]+/[0-9]+|abi " ci-serve.log | cut -c1-200 | sort -u | head -8
./speech-mac.sh
grep -a -c -i "exit status\|signal: \|unexpectedly" ci-serve.log | sed 's/^/crash lines: /'
for m in qwen2.5:1.5b llama3:latest; do curl -s http://127.0.0.1:22498/api/generate -d "{\"model\":\"$m\",\"keep_alive\":0}" >/dev/null; done
sleep 3; kill $SP
echo CI-GATE-DONE
