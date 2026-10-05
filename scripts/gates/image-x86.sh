#!/bin/bash
# G10 on amd64: the published image on solidPC with the GPU. Stores are mounted
# read-only, so nothing in them changes owner. IMG=<image>, default :dev.
IMG=${IMG:-mannixita/xollama:dev}; N=xollama-image-gate; H=http://127.0.0.1:22498
LLM=/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ollama_models; MEDIA=/srv/ml/media-store/models
ss -ltnp | grep -q '127.0.0.1:22498 ' && { echo "22498 busy"; exit 1; }
docker pull -q $IMG || exit 1
docker image inspect $IMG --format '{{.Architecture}}/{{.Os}} {{.Id}}'
up() { docker rm -f $N >/dev/null 2>&1; docker run -d --name $N --gpus all -p 127.0.0.1:22498:22434 -e OLLAMA_MODELS=/models -e OLLAMA_DEBUG=1 "$@" $IMG >/dev/null || exit 1
  for i in $(seq 60); do curl -s --max-time 2 $H/api/version >/dev/null && return; sleep 0.5; done; echo "server did not come up"; docker logs $N 2>&1 | tail -20; exit 1; }
gen() { for i in 1 2 3 4; do curl -s $H/api/generate -d "{\"model\":\"llama3:latest\",\"prompt\":\"Write a detailed essay about the history of the printing press, its inventors, and its effects on European society.\",\"stream\":false,\"options\":{\"num_predict\":512,\"seed\":$i,\"temperature\":0.7}}" | python3 -c "import sys,json; d=json.load(sys.stdin); e=d.get('error'); print('  $1 run $i', e if e else '%d tok %.1f tok/s load %.1fs'%(d['eval_count'], d['eval_count']/d['eval_duration']*1e9, d['load_duration']/1e9))"; done; }
echo "== opencoti, llama3, 512 tokens"
up -v $LLM:/models:ro
curl -s $H/api/xollama; echo; curl -s $H/api/version; echo
docker exec $N sh -c 'cat /usr/lib/ollama/PAYLOAD' | cut -c1-150
gen opencoti
curl -s $H/api/ps | python3 -c "import sys,json
for m in json.load(sys.stdin)['models']: print('  ps', m['name'], 'vram %.1f of %.1f GB'%(m['size_vram']/1e9, m['size']/1e9))"
docker logs $N 2>&1 | grep -a -E "using opencoti|falling back|using stock" | cut -c1-200 | sort | uniq -c | tail -2
echo "== llama.cpp (XOLLAMA_ENGINE=llamacpp)"
up -v $LLM:/models:ro -e XOLLAMA_ENGINE=llamacpp
gen llamacpp
docker logs $N 2>&1 | grep -a -E "using opencoti|using stock" | cut -c1-160 | sort | uniq -c | tail -2
echo "== speech, each clip transcribed back"
up -v $MEDIA:/models:ro
mkdir -p /srv/ml/xb140/out
for m in mannix/kokoro:82m mannix/kittentts:mini-0.8 mannix/supertonic:3 mannix/outetts:0.3; do f=/srv/ml/xb140/out/img_$(echo $m | tr '/:.' '___').mp3
  code=$(curl -s -o $f -w '%{http_code}' $H/v1/audio/speech -d "{\"model\":\"$m\",\"input\":\"The first ship came in at dawn.\"}")
  echo "  $m http=$code [$(file -b $f | cut -c1-30)] -> $(curl -s $H/v1/audio/transcriptions -F model=mannix/whisper:large-v3-turbo -F file=@$f | cut -c1-120)"; done
docker logs $N 2>&1 | grep -a -c -i "signal: \|unexpectedly\|panic" | sed 's/^/crash lines: /'
docker logs $N 2>&1 | grep -a -i "signal: \|unexpectedly\|panic" | cut -c1-400 | sed 's/^/  crash: /'
docker rm -f $N >/dev/null; echo IMAGE-X86-DONE
