#!/bin/bash
# Wan2.1 and Wan2.2 clips through xollama; frames counted by decoding.
H=http://127.0.0.1:22498
export XOLLAMA_HOST=127.0.0.1:22498
cd /srv/ml/xb140
(setsid nohup ./serve.sh > serve.log 2>&1 &)
for i in $(seq 40); do curl -s --max-time 2 $H/api/version >/dev/null && break; sleep 0.5; done
./xollama-dev pull mannix/wan2.2:ti2v-5b 2>&1 | tail -1
for m in mannix/wan2.1:t2v-1.3b mannix/wan2.2:ti2v-5b; do
  n=$(echo $m | tr '/:.' '___')
  s=$(date +%s)
  id=$(curl -s $H/v1/videos -d "{\"model\":\"$m\",\"prompt\":\"a red fox running through fresh snow at sunrise\",\"seed\":42}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("id") or d)')
  echo "$m job=$id"
  peak=0
  while :; do
    st=$(curl -s $H/v1/videos/$id | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("status"), d.get("error") or "")')
    u=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits); [ "$u" -gt "$peak" ] && peak=$u
    case "$st" in completed*|failed*|cancel*) break;; esac
    [ $(( $(date +%s) - s )) -gt 1500 ] && { echo timeout; break; }
    sleep 2
  done
  e=$(date +%s)
  curl -s -o out/$n.mp4 $H/v1/videos/$id/content
  echo "$m status=$st wall=$((e-s))s peak=${peak}MiB size=$(stat -c%s out/$n.mp4)"
  ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=codec_name,width,height,nb_read_frames,r_frame_rate,duration -of default=nw=1 out/$n.mp4 | tr '\n' ' '; echo
  curl -s -X DELETE $H/v1/videos/$id >/dev/null
done
grep 'video job .* completed' serve.log
echo VIDEO-DONE
