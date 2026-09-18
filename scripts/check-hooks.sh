#!/usr/bin/env bash
# Reconcile the xollama-hook: markers in the tree against the Registry in
# docs/protocols/UPSTREAM-SYNC.md.
#
# A marker whose id has no Registry row fails, and so does a Registry row with
# no marker in the tree. The first catches a hook someone added without
# recording it; the second catches a hook an upstream merge silently ate —
# which is the one that costs a day, because the code still compiles.
#
# Run it as step 3 of the sync workflow.
set -uo pipefail
cd "$(dirname "$0")/.."

REGISTRY=docs/protocols/UPSTREAM-SYNC.md

markers=$(grep -rno 'xollama-hook: [a-z0-9-]*' \
    --include='*.go' --include='*.ts' --include='*.tsx' . 2>/dev/null \
    | sed 's/.*xollama-hook: //' | sort -u)

# Registry ids are the first backticked cell of each table row, minus the
# "(dep)"/"(no-marker)" annotations used for files that cannot carry a comment.
rows=$(sed -n '/^| Hook ID |/,/^$/p' "$REGISTRY" \
    | grep '^| `' | sed 's/^| `\([a-z0-9-]*\)`.*/\1/' | sort -u)

status=0

while read -r id; do
    [ -z "$id" ] && continue
    if ! grep -qx "$id" <<< "$rows"; then
        echo "FAIL: marker 'xollama-hook: $id' has no row in $REGISTRY"
        grep -rn "xollama-hook: $id" --include='*.go' . | sed 's/^/       /'
        status=1
    fi
done <<< "$markers"

while read -r id; do
    [ -z "$id" ] && continue
    if ! grep -qx "$id" <<< "$markers"; then
        echo "FAIL: Registry row '$id' has no marker in the tree."
        echo "       An upstream merge may have dropped the hook — the tree still builds without it."
        status=1
    fi
done <<< "$rows"

if [ "$status" -eq 0 ]; then
    n=$(grep -c . <<< "$markers")
    echo "ok: $n hook(s), each with a Registry row"
fi
exit $status
