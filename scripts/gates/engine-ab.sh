#!/bin/bash
# G3: phase2-engine-ab on the engine this tree pins, as `ollama`.
#   STORE=<scratch store> [OUT=<dir>] [X=<xollama in an assembled rootfs>] scripts/gates/engine-ab.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
eng=$("$here/stage-engine.sh" x86_64)
OUT=${OUT:-/srv/ml/gates/run}; mkdir -p "$OUT/ab"
# The script starts <dir>/xollama: give it a directory holding the binary under test.
cp -r "$repo/scripts" "$OUT/ab/" && ln -sfn "${X:-$repo/xollama}" "$OUT/ab/xollama"
cd "$OUT"
exec sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_VULKAN=false \
  python3 ab/scripts/phase2-engine-ab.py --engine opencoti --axis all --models "${STORE:?a writable scratch store}" --artifact "$eng" --out "$OUT"
