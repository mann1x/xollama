#!/bin/bash
# G7: opencoti's Vulkan library on an AMD or Intel GPU, THROUGH THE IMAGE.
# solidPC's own Vulkan drivers are from 2020 (RADV 20.3.5, amdgpu-pro 20.40)
# and the engine refuses them since c10; the image carries Mesa's drivers
# (Dockerfile.xollama), so the gate passes the GPU's render node into a
# container of the image under test and measures there, as a user in the same
# position would run it.
#   IMAGE=<xollama image> [NODE=/dev/dri/renderD129] [VKSTORE=<dir>] scripts/gates/vulkan-linux.sh
# The container's /root/.ollama is VKSTORE (default /srv/ml/gates/vkstore, its
# own store: the image runs as root). Port 22498 on the host's loopback.
set -u
here=$(cd "$(dirname "$0")" && pwd)
IMAGE=${IMAGE:?the image under test, for example mannixita/xollama:dev}
NODE=${NODE:-/dev/dri/renderD129}; VKSTORE=${VKSTORE:-/srv/ml/gates/vkstore}; H=127.0.0.1:22498; C=xollama-vkgate
[ "$(basename "$(readlink -f /sys/class/drm/$(basename $NODE)/device/driver)")" = nvidia ] && { echo "$NODE is the NVIDIA card; name the AMD or Intel render node"; exit 1; }
ss -ltn | grep -q "$H " && { echo "22498 busy"; exit 1; }
mkdir -p "$VKSTORE"
docker run -d --name $C --device "$NODE" -e OLLAMA_DEBUG=1 -e OLLAMA_KEEP_ALIVE=60s -p $H:22434 -v "$VKSTORE":/root/.ollama "$IMAGE" >/dev/null || exit 1
trap 'docker rm -f $C >/dev/null 2>&1' EXIT
for i in $(seq 120); do curl -s --max-time 2 http://$H/api/version >/dev/null && break; sleep 0.5; done
echo "image $IMAGE, server $(curl -s http://$H/api/version)"
curl -s http://$H/api/xollama/devices | python3 -c 'import json,sys
for d in json.load(sys.stdin)["devices"]: print("  device:", d.get("backend"), d.get("description"), "pci", d.get("pci_id"), "engine", d.get("engine"), "%.1f GiB" % (d.get("total_memory", 0) / 2**30))'
docker exec $C xollama pull qwen2.5:1.5b 2>&1 | tail -1 | tr -d '\033' | cut -c1-40
H=$H "$here/bench.sh" qwen2.5:1.5b
echo "--- /props and /kv with the model on Vulkan (bug-3921)"
for ep in props kv props kv; do printf "  %s: " $ep; curl -s -o /dev/null -w "%{http_code}\n" --max-time 30 "http://$H/api/engine?model=qwen2.5:1.5b&endpoint=$ep"; done
curl -s http://$H/api/generate -d '{"model":"qwen2.5:1.5b","prompt":"Write a detailed essay about canals.","stream":false,"options":{"num_predict":256,"seed":9}}' | python3 -c "import sys,json; d=json.load(sys.stdin); e=d.get('error'); print('  generate after the polls:', e if e else '%d tok %.1f tok/s' % (d['eval_count'], d['eval_count']/d['eval_duration']*1e9))"
docker logs $C 2>&1 | grep -a -E "opencoti build|offloaded|using device Vulkan|model placement" | sort -u | cut -c1-200 | head -5
docker logs $C 2>&1 | grep -a -c -i "llama-server terminated\|runner has unexpectedly\|signal: aborted\|signal: segmentation\|signal: killed\|DeviceLost" | sed 's/^/  crash lines in the server log (want 0): /'
echo VULKAN-DONE
