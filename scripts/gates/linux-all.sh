#!/bin/bash
# The solidPC gates a pin move needs, in order, on the engine this tree pins:
# G3 engine A/B, G4 decision and chat paths, G6 speech and video, G5 council.
# G7 (Vulkan) runs in the image: vulkan-linux.sh, once an image exists.
#   ROOTFS=<dir> STORE=<chat store> CSTORE=<council store> MSTORE=<media store> [W=<run dir>] scripts/gates/linux-all.sh
# ROOTFS is a payload assembled by scripts/docker-assemble.sh (bin/, lib/ollama
# with the pinned stock runtime): a bare binary finds no llama-server. The tree
# is built into ROOTFS/bin/xollama and that binary is used throughout; the
# engine is the one this tree pins, staged apart. Run as root.
set -u
here=$(cd "$(dirname "$0")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
: "${ROOTFS:?}" "${STORE:?}" "${CSTORE:?}" "${MSTORE:?}"
[ -d "$ROOTFS/lib/ollama" ] || { echo "$ROOTFS has no lib/ollama"; exit 1; }
export W=${W:-/srv/ml/gates/run}; mkdir -p "$W"; export X=$ROOTFS/bin/xollama OUT=$W
(cd "$repo" && go build -o "$X" .) || { echo BUILD-FAILED; exit 1; }
echo "built $(date +%T) from $(git -C "$repo" rev-parse --short HEAD), engine $(awk '$1=="engine"{print $NF}' "$repo/llm/engine/pin/index.txt")"
echo "=== G3 $(date +%T)"; STORE=$STORE "$here/engine-ab.sh" > "$W/g3.log" 2>&1; tail -2 "$W/g3.log"
echo "=== G4 decision $(date +%T)"; STORE=$STORE "$here/decision.sh"
echo "=== G4 chat paths $(date +%T)"; STORE=$STORE "$here/chat-paths.sh"
echo "=== G6 speech $(date +%T)"; MEDIA=$MSTORE W=$W/speech "$here/speech-linux.sh"
echo "=== G6 video $(date +%T)"; MEDIA=$MSTORE "$here/video-linux.sh"
echo "=== G5 council $(date +%T)"; STORE=$CSTORE "$here/council.sh" ${COUNCIL_TAGS:-gate/council-kv3-384k gate/council-kv3-384k gate/council-kv3-384k gate/council-kv3-384k gate/council-kv3-nopolykv omni-council-idle}
echo "foreign files in the stores: $(find "$STORE" "$CSTORE" "$MSTORE" ! -user ollama | wc -l)"
echo LINUX-ALL-DONE
