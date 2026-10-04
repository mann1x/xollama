#!/bin/bash
# N runs of 512 tokens against a running xollama; prints tok/s per run.
H=${H:-127.0.0.1:22434}
for m in "$@"; do
  for i in 1 2 3 4; do
    curl -s http://$H/api/generate -d "{\"model\":\"$m\",\"prompt\":\"Write a detailed essay about the history of the printing press, its inventors, and its effects on European society.\",\"stream\":false,\"options\":{\"num_predict\":512,\"seed\":$i,\"temperature\":0.7}}" \
    | python3 -c "import sys,json; d=json.load(sys.stdin); e=d.get('error'); print('$m run $i', e if e else 'tokens %d  %.1f tok/s  prompt %.0f tok/s  load %.1fs'%(d['eval_count'], d['eval_count']/d['eval_duration']*1e9, d['prompt_eval_count']/max(d['prompt_eval_duration'],1)*1e9, d['load_duration']/1e9))"
  done
  curl -s http://$H/api/ps | python3 -c "import sys,json
for m in json.load(sys.stdin)['models']: print('  ps', m['name'], 'vram %.1f GB of %.1f GB'%(m['size_vram']/1e9, m['size']/1e9), 'ctx', m.get('context_length'))"
done
