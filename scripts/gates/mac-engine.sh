#!/bin/bash
# G11 before an app exists for the tree: the engine THIS TREE pins and a server
# built from this tree, in a copy of the installed app's Resources, on the side
# port. Metal bench, then speech, each clip transcribed back by Whisper.
# On solidPC, with D=dev/xollama/<name> on the Mac mini:
#
#   d=$(dirname "$(scripts/gates/stage-engine.sh macos-aarch64)")
#   (git ls-files -z; git ls-files -z -o --exclude-standard llm/engine/pin) > /srv/ml/gates/files0
#   rsync -a --from0 --files-from=/srv/ml/gates/files0 . macmini:$D/src/
#   rsync -a "$d"/ macmini:$D/stage/
#   ssh macmini D=\~/$D bash \~/$D/src/scripts/gates/mac-engine.sh
#
# The store is the Mac's own xOllama store. mac-ci.sh is the gate on the app
# the release workflow builds.
D=${D:?D=<directory with src/ and stage/>}; H=127.0.0.1:22498; G=$D/src/scripts/gates
cd "$D" || exit 1
lsof -nP -iTCP:22498 -sTCP:LISTEN -t >/dev/null && { echo "22498 busy"; exit 1; }
export PATH=$PATH:/usr/local/go/bin:/opt/homebrew/bin
(cd src && go build -trimpath -ldflags "-X=github.com/ollama/ollama/version.Version=0.0.0-gate" -o ../xollama-gate .) || { echo "build failed"; exit 1; }
eng=$(awk '$1=="engine" && $3=="bin" { print $4 }' stage/engine-manifest.txt)
[ -x "stage/$eng" ] || { echo "no engine in $D/stage"; exit 1; }
rm -rf res; cp -cR /Applications/xOllama.app/Contents/Resources res
rm -rf res/engines; mkdir res/engines; cp stage/* res/engines/; cp xollama-gate res/xollama
ls res/engines | grep -E "opencoti|ape-|ggml-metal|oc-" | tr '\n' ' '; echo
(XOLLAMA_HOST=$H XOLLAMA_ENGINE_PATH=$D/res/engines/$eng OLLAMA_DEBUG=1 nohup ./res/xollama serve > serve.log 2>&1 &)
for i in $(seq 60); do curl -s -m 2 http://$H/api/version && break; sleep 0.5; done; echo
H=$H "$G/bench.sh" qwen2.5:1.5b llama3:latest
grep -a -E "using opencoti|falling back" serve.log | cut -c1-220 | sort | uniq -c | tail -3
grep -a -q -E "falling back|no stock llama-server on this platform" serve.log && echo "FAIL: a load did not get opencoti (macOS has no stock llama-server)"
grep -a -E "opencoti build|offloaded [0-9]+/[0-9]+|abi " serve.log | cut -c1-200 | sort -u | head -8
"$G/speech-mac.sh"
grep -a -c -E "llama-server terminated|runner has unexpectedly|signal: aborted|signal: segmentation|signal: killed" serve.log | sed 's/^/crash lines: /'
for m in qwen2.5:1.5b llama3:latest; do curl -s http://$H/api/generate -d "{\"model\":\"$m\",\"keep_alive\":0}" >/dev/null; done
sleep 3; kill "$(lsof -nP -iTCP:22498 -sTCP:LISTEN -t)"
echo MAC-ENGINE-DONE
