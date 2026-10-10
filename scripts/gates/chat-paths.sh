#!/bin/bash
# G4, chat paths (chat-paths.py) on a side server on the engine this tree pins.
#   STORE=<scratch store> [X=<xollama in an assembled rootfs>] [W=] scripts/gates/chat-paths.sh
. "$(dirname "$0")/side.sh"
up "${STORE:?a writable scratch store}" serve-chat.log
python3 "$here/chat-paths.py"
grep -a -c "kv-reservation: REFUSED" serve-chat.log | sed 's/^/kv-reservation refusals: /'
grep -a "opencoti build" serve-chat.log | sort -u | cut -c1-140 | head -1
down
echo "foreign files in the store: $(find "$STORE" ! -user ollama | wc -l)"
echo CHAT-DONE
