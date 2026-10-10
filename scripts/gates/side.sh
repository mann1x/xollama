# Sourced by the Linux gates: one side server on 22498 through serve-side.sh
# (the engine this tree pins), started and stopped per gate.
#   up <store> <log>   start it on a scratch store and wait for it
#   down               stop it and wait for the port
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd); repo=$(git -C "$here" rev-parse --show-toplevel)
H=127.0.0.1:22498
W=${W:-/srv/ml/gates/run}; mkdir -p "$W/out"; cd "$W" || exit 1
up() {
  ss -ltn | grep -q "$H " && { echo "22498 busy"; exit 1; }
  (MEDIA=$1 setsid nohup "$here/serve-side.sh" > "$2" 2>&1 &)
  for i in $(seq 240); do curl -s --max-time 2 http://$H/api/version >/dev/null && return 0; sleep 0.5; done
  echo "the side server did not start, see $W/$2"; exit 1
}
down() {
  P=$(ss -ltnp | sed -n "s/.*$H .*pid=\([0-9]*\).*/\1/p" | head -1); [ -n "$P" ] && kill "$P"
  for i in $(seq 60); do ss -ltn | grep -q "$H " || return 0; sleep 1; done
}
