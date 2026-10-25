#!/bin/bash
# G3: phase2-engine-ab on the engine this tree pins, then on stock llama.cpp
# (the binary's own lib/ollama, no artifact), both as `ollama`.
#   STORE=<scratch store> [OUT=<dir>] [X=<xollama in an assembled rootfs>] [ENGINES="opencoti llamacpp"] scripts/gates/engine-ab.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
eng=$("$here/stage-engine.sh" x86_64)
OUT=${OUT:-/srv/ml/gates/run}; install -d -o ollama "$OUT"; mkdir -p "$OUT/ab"  # the run writes its results as `ollama`
# The script starts <dir>/xollama: give it a directory holding the binary under test.
cp -r "$repo/scripts" "$OUT/ab/" && ln -sfn "${X:-$repo/xollama}" "$OUT/ab/xollama"
cd "$OUT"
for e in ${ENGINES:-opencoti llamacpp}; do
  art=(); [ "$e" = opencoti ] && art=(--artifact "$eng")
  sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_VULKAN=false \
    python3 ab/scripts/phase2-engine-ab.py --engine "$e" --axis all --models "${STORE:?a writable scratch store}" "${art[@]}" --out "$OUT"
done
