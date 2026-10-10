#!/bin/bash
# A side server on 22498, as `ollama`, on the opencoti engine THIS TREE pins
# (staged by stage-engine.sh, never a file named here). 3090 via CUDA; the
# Vulkan iGPU hidden (its gate runs in the image: vulkan-linux.sh).
#   MEDIA=<store> [X=<xollama in an assembled rootfs>] [KEEP=<keep alive>] scripts/gates/serve-side.sh
# MEDIA must be a store this server may write: since upstream v0.40.0 it
# creates manifests-v2 in the store it serves, so never a shared store (use a
# scratch store of hard links).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
eng=$("$here/stage-engine.sh" x86_64)
exec sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home \
  OLLAMA_MODELS="${MEDIA:?a writable scratch store}" XOLLAMA_ENGINE=opencoti XOLLAMA_ENGINE_PATH="$eng" \
  XOLLAMA_HOST=127.0.0.1:22498 OLLAMA_VULKAN=false OLLAMA_DEBUG=1 OLLAMA_KEEP_ALIVE="${KEEP:-5m}" "${X:-$repo/xollama}" serve
