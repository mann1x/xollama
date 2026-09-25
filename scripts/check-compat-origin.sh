#!/usr/bin/env bash
# llama.cpp comes from the fork. Fail any commit in a range that changes what
# llama-server is built from unless it came from there.
#
# The ruling (2026-09-25): mann1x/ollama SUPPLIES llama.cpp. xollama's
# LLAMA_CPP_VERSION, llama/server and llama/compat must stay identical to the
# fork's; a change lands in the fork first and reaches xollama by sha, as a
# --no-ff merge (docs/protocols/FORK-SYNC.md). Twice it went the other way --
# a836824b wrote 005 here and faabb1ca rewrote llama/compat/README.md here --
# and each time the fork's runtime stopped matching xollama's inputs digest.
#
# A commit in RANGE that touches those paths passes when:
#   - it is not a merge, and it is reachable from a fork or upstream ref
#     (it was written there and merged in); or
#   - it is a merge, and its tree for those paths equals one of its parents'
#     (resolving a conflict to either side brings nothing new; a merge that
#     invents content is flagged exactly like a direct commit).
#
# Usage: scripts/check-compat-origin.sh <base>..<head>
#   Fork and upstream refs must already be fetched; the workflow fetches them
#   into refs/remotes/fork/* and refs/remotes/upstream/*. Locally, `git fetch
#   fork` and `git fetch upstream` do the same.

set -euo pipefail

range="${1:?usage: $0 <base>..<head>}"
paths=(LLAMA_CPP_VERSION llama/server llama/compat)

# Not refs/tags: xollama's own release tags contain every xollama commit, so
# they would wave the very thing this checks through.
mapfile -t sources < <(git for-each-ref --format='%(refname)' refs/remotes/fork refs/remotes/upstream)
if [ -z "$(git for-each-ref --format=x refs/remotes/fork | head -1)" ]; then
    echo "check-compat-origin: no refs/remotes/fork/* -- fetch the fork first" >&2
    exit 2
fi

subtree() { git ls-tree -r "$1" -- "${paths[@]}" | sha256sum | cut -c1-64; }

from_a_source() {
    # --contains over every source ref at once; any hit is enough.
    [ -n "$(git for-each-ref --contains "$1" --format=x "${sources[@]}" | head -1)" ]
}

bad=0
checked=0
while read -r c; do
    [ -n "$c" ] || continue
    parents=$(git rev-list --parents -n1 "$c" | cut -d' ' -f2-)
    if [ "$(wc -w <<<"$parents")" -gt 1 ]; then
        mine=$(subtree "$c")
        # Same as the first parent: the merge brought nothing into these paths.
        [ "$(subtree "${parents%% *}")" = "$mine" ] && continue
        checked=$((checked+1))
        # File by file: a merge may combine the two sides (the fork's 004 next
        # to this tree's 005), but every file it leaves must be one a parent
        # already had, byte for byte. A conflict resolved by hand into new text
        # is content written here.
        written=""
        while read -r _ _ blob path; do
            ok=no
            for p in $parents; do
                [ "$(git rev-parse -q --verify "$p:$path" 2>/dev/null)" = "$blob" ] && { ok=yes; break; }
            done
            [ "$ok" = yes ] || written="$written $path"
        done < <(git ls-tree -r "$c" -- "${paths[@]}")
        if [ -n "$written" ]; then
            echo "::error::$(git log -1 --format='%h %s' "$c") -- this merge leaves${written} in a state neither parent had: content was written in the merge, not taken from the fork"
            bad=$((bad+1))
        fi
        continue
    fi
    [ -n "$(git diff --name-only "$c^" "$c" -- "${paths[@]}")" ] || continue
    checked=$((checked+1))
    if ! from_a_source "$c"; then
        echo "::error::$(git log -1 --format='%h %s' "$c") -- changes $(git diff --name-only "$c^" "$c" -- "${paths[@]}" | paste -sd' ') but is on no mann1x/ollama or ollama/ollama ref. llama.cpp comes from the fork: land it there and merge the fork's sha (docs/protocols/FORK-SYNC.md)."
        bad=$((bad+1))
    fi
done < <(git rev-list "$range")

echo "check-compat-origin: $checked commit(s) in $range touch the llama.cpp inputs; $bad not from the fork or upstream"
[ "$bad" -eq 0 ]
