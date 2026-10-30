#!/usr/bin/env bash
# A llama.cpp move must not add a model architecture the routing does not know.
#
# Stock llama.cpp gains an architecture before opencoti does: upstream moves
# LLAMA_CPP_VERSION every sync, and opencoti's llama.cpp is llamafile's plus its
# own patch chain. A model of an architecture the pinned engine lacks must be
# sent to stock llama-server (server/arch_engine.go), and that needs a row in
# llm/engine/arch.go naming the pin feature that ends the detour. The first
# case was lfm3 (llama.cpp #31252, b11963, upstream v0.40.13; c13 lacks it).
#
# Lists the architectures llama.cpp's src/llama-arch.cpp has at the head's
# LLAMA_CPP_VERSION and not at the base's, and fails for each without a row.
# An architecture the pinned engine already serves gets a row with no feature,
# `"<arch>": "",`, once that is checked.
#
# Usage: scripts/check-engine-archs.sh [<base-ref>]    (default origin/main)
#   Compares <base-ref>:LLAMA_CPP_VERSION with the working tree's. Reads
#   llama.cpp from raw.githubusercontent.com; no token needed.

set -euo pipefail

base="${1:-origin/main}"
old=$(git show "$base:LLAMA_CPP_VERSION" | tr -d '[:space:]')
new=$(tr -d '[:space:]' <LLAMA_CPP_VERSION)

if [ "$old" = "$new" ]; then
    echo "check-engine-archs: LLAMA_CPP_VERSION unchanged ($new)"
    exit 0
fi

archs() {
    curl -fsSL "https://raw.githubusercontent.com/ggml-org/llama.cpp/$1/src/llama-arch.cpp" |
        grep -oE '\{ *LLM_ARCH_[A-Z0-9_]+, *"[^"]+" *\}' |
        sed -E 's/.*"([^"]+)".*/\1/' | sort -u
}

old_list=$(archs "$old")
new_list=$(archs "$new")
# An empty list is a fetch or format failure, not "nothing added".
if [ -z "$old_list" ] || [ -z "$new_list" ]; then
    echo "check-engine-archs: could not read the architectures of llama.cpp $old or $new" >&2
    exit 2
fi

added=$(comm -13 <(echo "$old_list") <(echo "$new_list"))
if [ -z "$added" ]; then
    echo "check-engine-archs: llama.cpp $old -> $new adds no architecture"
    exit 0
fi

bad=0
while read -r a; do
    if grep -qE "^[[:space:]]*\"$a\":[[:space:]]*\"[^\"]*\"," llm/engine/arch.go; then
        echo "check-engine-archs: $a: routed ($(grep -oE "\"$a\":[[:space:]]*\"[^\"]*\"" llm/engine/arch.go))"
    else
        echo "check-engine-archs: $a: llama.cpp $new adds it and llm/engine/arch.go has no row" >&2
        bad=1
    fi
done <<<"$added"

if [ "$bad" -ne 0 ]; then
    echo "check-engine-archs: add a row per architecture: \"<arch>\": \"arch_<arch>_v1\" while the pinned opencoti lacks it, \"<arch>\": \"\" once it is checked to serve it" >&2
fi
exit "$bad"
