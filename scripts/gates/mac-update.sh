#!/bin/bash
# G11, the update path: the installed app updates itself to the published pre-release T=<version without v>
# (pre-releases allowed), a hidden start applies it, then signature, staple and a bench. On the Mac mini.
cd ~/dev/xollama; L=~/Library/Caches/xOllama; G=~/Library/Logs/xOllama/app.log; T=${T:?T=<version, for example 0.40.1-xollama.1>}
curl -sL https://github.com/mann1x/xollama/releases/download/v$T/sha256sum.txt | grep darwin
appid() { ps -axo pid,command | awk '$2=="/Applications/xOllama.app/Contents/MacOS/xOllama"{print $1}'; }
stopapp() { A=$(appid); S=$(lsof -nP -iTCP:22434 -sTCP:LISTEN -t | head -1); [ -n "$A" ] && kill $A; for i in $(seq 20); do [ -n "$A" ] && kill -0 $A 2>/dev/null || break; sleep 1; done; [ -n "$S" ] && kill -0 $S 2>/dev/null && kill $S; sleep 2; }
echo "== 1. the installed app, pre-releases allowed"; stopapp
echo "installed: $(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' /Applications/xOllama.app/Contents/Info.plist)"
# Lines since this moment, by their timestamp: the app rotates its log at start,
# so a line count taken before it is past the end of the new file.
S=$(date +%Y-%m-%dT%H:%M:%S); since() { awk -v s="time=$S" '$1 >= s' $G 2>/dev/null; }
open --env XOLLAMA_UPDATE_PRERELEASE=1 --env OLLAMA_DEBUG=1 /Applications/xOllama.app --args --fast-startup
for i in $(seq 120); do since | grep -a -q "passed verification" && break; sleep 5; done
since | grep -a -E "new update available|new update downloaded|checksum verified|passed verification|error|ERROR" | tail -6 | cut -c1-220
find $L | sed "s#$HOME#~#"
echo "== 2. hidden start applies the update"; stopapp
open --env OLLAMA_DEBUG=1 /Applications/xOllama.app --args hidden
for i in $(seq 90); do v=$(curl -s -m 2 127.0.0.1:22434/api/version); case "$v" in *$T*) break;; esac; sleep 2; done; sleep 8
echo "server: $(curl -s -m 2 127.0.0.1:22434/api/version)"
echo "bundle: $(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' /Applications/xOllama.app/Contents/Info.plist)"
codesign --verify --deep --strict /Applications/xOllama.app && echo "codesign ok"; spctl -a -vv /Applications/xOllama.app 2>&1 | head -2; xcrun stapler validate /Applications/xOllama.app 2>&1 | tail -1
ls /Applications/xOllama.app/Contents/Resources | tr '\n' ' '; echo
grep -a -E "first start after upgrade|post upgrade cleanup|starting Ollama" $G | tail -3 | cut -c1-200
H=127.0.0.1:22434 ./bench.sh qwen2.5:1.5b 2>&1 | grep "run [234]" | cut -c1-90
curl -s 127.0.0.1:22434/api/generate -d '{"model":"qwen2.5:1.5b","keep_alive":0}' >/dev/null
echo MAC-UPDATE-DONE
