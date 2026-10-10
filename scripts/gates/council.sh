#!/bin/bash
# G5: the council gate, one side server per tag, on the engine this tree pins.
#   STORE=<scratch council store> [T=<seconds per call>] [X=<xollama in an assembled rootfs>] [W=] scripts/gates/council.sh <tag>...
. "$(dirname "$0")/side.sh"
: "${STORE:?a writable scratch council store}"
for t in "$@"; do
  LOG=serve-council-$(date +%H%M%S).log
  KEEP=30s up "$STORE" $LOG
  echo "--- $t"; python3 -u "$repo/scripts/council-gate.py" --host $H --timeout ${T:-600} $t | cut -c1-330
  echo "  $LOG: pools $(grep -a -c 'polykv: created pool' $LOG), kv refusals $(grep -a -c 'kv-reservation: REFUSED' $LOG), crashes $(grep -a -c -i 'panic\|signal:' $LOG)"
  down; sleep 3
done
echo COUNCIL-DONE
