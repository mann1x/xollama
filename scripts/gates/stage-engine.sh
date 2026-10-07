#!/bin/bash
# Stage the opencoti engine THIS TREE pins (llm/engine/pin), with every file
# that travels beside it (GPU and media libraries), sha256-checked by the same
# cmake/opencoti-fetch.cmake the build uses, and print the engine's path.
# Gate scripts take the engine from here, so they always run the engine being
# integrated, never a file name written into the script.
#
#   scripts/gates/stage-engine.sh <x86_64|aarch64|win-x86_64|macos-aarch64> [dir]
#
# Default dir: /srv/ml/gates/engine-<platform>-<engine version>. Downloads are
# cached in /srv/ml/gates/opencoti-cache (CACHE_DIR=...). For Windows, copy the
# directory to the host and pass it to the script (windows-speech.ps1
# -EngineDir).
set -euo pipefail
arch=${1:?usage: stage-engine.sh <x86_64|aarch64|win-x86_64|macos-aarch64> [dir]}
repo=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
pin=$repo/llm/engine/pin
ver=$(awk '$1=="engine" { for (i = 1; i <= NF; i++) if ($i == "version") print $(i+1) }' "$pin/index.txt")
dir=${2:-/srv/ml/gates/engine-$arch-$ver}
cache=${CACHE_DIR:-/srv/ml/gates/opencoti-cache}
mkdir -p "$dir" "$cache"
cmake -DPIN_DIR="$pin" -DARCH="$arch" -DDEST_DIR="$dir" -DCACHE_DIR="$cache" \
    -DMANIFEST="$dir/engine-manifest.txt" -P "$repo/cmake/opencoti-fetch.cmake" >&2
eng=$(awk '$1=="engine" && $3=="bin" { print $4 }' "$dir/engine-manifest.txt")
[ "$(echo "$eng" | grep -c .)" = 1 ] || { echo "stage-engine: expected one engine in $dir/engine-manifest.txt, found: $eng" >&2; exit 1; }
echo "stage-engine: engine $ver ($(awk '$1=="tag" { print $2 }' "$pin/index.txt")) for $arch in $dir" >&2
echo "$dir/$eng"
