#!/bin/bash
# opencoti Vulkan library on the Renoir iGPU: CUDA hidden, 512 tokens, 4 runs.
cd /srv/ml/xb140
export OLLAMA_MODELS=/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ollama_models
export XOLLAMA_ENGINE=opencoti XOLLAMA_ENGINE_PATH=/srv/ml/xb140/stage-x86_64/opencoti-0.10.5-c7-2610041714001
export XOLLAMA_HOST=127.0.0.1:22498 OLLAMA_VULKAN=true CUDA_VISIBLE_DEVICES= OLLAMA_DEBUG=1
(setsid nohup sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home OLLAMA_MODELS=$OLLAMA_MODELS XOLLAMA_ENGINE=$XOLLAMA_ENGINE XOLLAMA_ENGINE_PATH=$XOLLAMA_ENGINE_PATH XOLLAMA_HOST=$XOLLAMA_HOST OLLAMA_VULKAN=true CUDA_VISIBLE_DEVICES= OLLAMA_DEBUG=1 /srv/dev-disk-by-label-opt/dev/xollama/xollama serve > serve-vulkan.log 2>&1 &)
for i in $(seq 60); do curl -s --max-time 2 http://$XOLLAMA_HOST/api/version >/dev/null && break; sleep 0.5; done
grep -E "inference compute|opencoti device enumeration" serve-vulkan.log | cut -c1-260
H=$XOLLAMA_HOST /srv/ml/mac-copy/bench.sh qwen2.5:1.5b
grep -E "using opencoti|falling back|--gpu" serve-vulkan.log | cut -c1-300 | tail -3
grep -a -i -E "vulkan.*(loaded|library)|ggml_vulkan: Found|offloaded" serve-vulkan.log | cut -c1-200 | tail -5
echo "--- /props and /kv with the model on Vulkan (bug-3921)"
for ep in props kv props kv; do printf "  %s: " $ep; curl -s -o /dev/null -w "%{http_code}\n" --max-time 30 "http://$XOLLAMA_HOST/api/engine?model=qwen2.5:1.5b&endpoint=$ep"; done
curl -s http://$XOLLAMA_HOST/api/generate -d '{"model":"qwen2.5:1.5b","prompt":"Write a detailed essay about canals.","stream":false,"options":{"num_predict":256,"seed":9}}' | python3 -c "import sys,json; d=json.load(sys.stdin); e=d.get('error'); print('  generate after the polls:', e if e else '%d tok %.1f tok/s load %.1fs'%(d['eval_count'], d['eval_count']/d['eval_duration']*1e9, d['load_duration']/1e9))"
grep -a -c -i "exit status\|signal: segmentation\|runner has unexpectedly" ${SERVELOG:-serve-vulkan.log} | sed 's/^/  crash lines in the server log: /'
P=$(ss -ltnp | sed -n 's/.*127.0.0.1:22498 .*pid=\([0-9]*\).*/\1/p' | head -1); [ -n "$P" ] && kill $P
echo VULKAN-DONE
