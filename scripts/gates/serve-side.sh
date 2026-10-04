#!/bin/bash
# xollama dev against the staged engine on solidPC (3090 via CUDA; Vulkan iGPU hidden).
export OLLAMA_MODELS=/srv/ml/media-store/models
export XOLLAMA_ENGINE=opencoti
export XOLLAMA_ENGINE_PATH=/srv/ml/xb140/stage-x86_64/opencoti-0.10.5-c7-2610041714001
export XOLLAMA_HOST=127.0.0.1:22498
export OLLAMA_VULKAN=false
export OLLAMA_DEBUG=1
exec /srv/dev-disk-by-label-opt/dev/xollama/xollama serve
