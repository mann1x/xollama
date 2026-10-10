#!/bin/bash
# G6, video: Wan2.1 and Wan2.2 clips through xollama; frames counted by decoding.
#   MEDIA=<scratch store with both models> [X=<xollama in an assembled rootfs>] [W=] scripts/gates/video-linux.sh
. "$(dirname "$0")/side.sh"
up "${MEDIA:?a writable scratch store of the video models}" serve-video.log
for m in mannix/wan2.1:t2v-1.3b mannix/wan2.2:ti2v-5b; do
  n=$(echo $m | tr '/:.' '___'); s=$(date +%s)
  id=$(curl -s http://$H/v1/videos -d "{\"model\":\"$m\",\"prompt\":\"a red fox running through fresh snow at sunrise\",\"seed\":42}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("id") or d)')
  while :; do
    st=$(curl -s http://$H/v1/videos/$id | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("status"), d.get("error") or "")')
    case "$st" in completed*|failed*|cancel*) break;; esac
    [ $(( $(date +%s) - s )) -gt 1500 ] && { echo timeout; break; }
    sleep 3
  done
  curl -s -o out/$n.mp4 http://$H/v1/videos/$id/content
  echo "$m status=$st wall=$(( $(date +%s) - s ))s frames=$(ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=nb_read_frames -of csv=p=0 out/$n.mp4)"
  curl -s -X DELETE http://$H/v1/videos/$id >/dev/null
done
down; echo VIDEO-DONE
