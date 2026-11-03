#!/bin/bash
# opencoti bug-4329 through xollama, THROUGH THE IMAGE on solidPC's Renoir iGPU
# (the host's own Vulkan drivers are refused, see vulkan-linux.sh):
# --no-mmap --mlock from XOLLAMA_ENGINE_ARGS with more than 1 GiB of weights in
# the Vulkan host buffer (qwen3:4b, num_gpu 4, about 2.2 GiB). Vulkan_Host caps a
# buffer at 1 GiB, so the host context is a multi-buffer: c14 r5 to r7 asserted
# GGML_ASSERT(addr) at llama-mmap.cpp:856; r8 logs "mlock: Vulkan_Host buffer is
# N pieces ..., each locked". xollama never sends either flag itself.
#   IMAGE=<image> LABEL=<arm> [NODE=/dev/dri/renderD129] [VKSTORE=<dir>] scripts/gates/vulkan-mlock.sh
# Pulls qwen3:4b into VKSTORE; remove it afterwards (release step 9). The image
# is not removed: run it after vulkan-linux.sh with KEEP=1, or remove it yourself.
set -u
IMAGE=${IMAGE:?the image under test}; LABEL=${LABEL:?an arm label}
NODE=${NODE:-/dev/dri/renderD129}; VKSTORE=${VKSTORE:-/srv/ml/gates/vkstore}; H=127.0.0.1:22498; C=xollama-vkmlock
ss -ltn | grep -q "$H " && { echo "22498 busy"; exit 1; }
docker run -d --name $C --device "$NODE" --ulimit memlock=-1:-1 -e OLLAMA_DEBUG=1 -e OLLAMA_KEEP_ALIVE=60s \
  -e XOLLAMA_ENGINE_ARGS="--no-mmap --mlock" -p $H:22434 -v "$VKSTORE":/root/.ollama "$IMAGE" >/dev/null || exit 1
trap 'docker rm -f $C >/dev/null 2>&1' EXIT
for i in $(seq 120); do curl -s --max-time 2 http://$H/api/version >/dev/null && break; sleep 0.5; done
echo "== $LABEL image $IMAGE server $(curl -s http://$H/api/version)"
docker exec $C xollama pull qwen3:4b 2>&1 | tail -1 | tr -d '\033' | cut -c1-40
curl -s http://$H/api/generate -d '{"model":"qwen3:4b","prompt":"Write a long story about a lighthouse keeper.","stream":false,"think":false,"options":{"num_predict":256,"seed":7,"num_gpu":4,"num_ctx":4096}}' \
 | python3 -c "import sys,json; d=json.load(sys.stdin); e=d.get('error'); print('  generate:', e if e else '%d tok %.1f tok/s' % (d['eval_count'], d['eval_count']/d['eval_duration']*1e9))"
docker logs $C 2>&1 | grep -a -E "opencoti build|engine args|offloaded|Vulkan_Host|mlock|GGML_ASSERT|llama-server terminated|model placement" | sort -u | cut -c1-240 | head -14
echo VKMLOCK-DONE
