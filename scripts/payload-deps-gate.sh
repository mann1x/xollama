#!/usr/bin/env bash
# payload-deps-gate.sh — every shipped ELF must be able to resolve its dynamic
# dependencies from inside the payload, or from the short list of libraries the
# host is genuinely expected to provide.
#
# WHY THIS EXISTS, AND WHY THE GLIBC GATE IS NOT ENOUGH. The floor gate checks
# which glibc SYMBOL VERSIONS an object needs. It says nothing about whether the
# object's NEEDED libraries are present at all. Measured, 2026-09-19: a lane
# build linked libggml-cuda.so against libnccl.so.2, because the CUDA container
# ships NCCL and ggml's GGML_CUDA_NCCL defaults ON. The host has no NCCL, so
# dlopen of the CUDA backend failed, llama-server printed one line —
#
#     warning: no usable GPU found, --gpu-layers option will be ignored
#
# — and served the model on CPU. Every result taken that way looks valid and is
# worthless. That is opencoti's bug-2115 failure mode reached by a different
# route, which is the argument for checking it structurally rather than
# remembering to look.
#
# HOST-PROVIDED IS A SHORT, EXPLICIT LIST. libcuda.so.1 is the NVIDIA driver and
# is never shipped. The C/C++ runtime comes from the host too — that is what the
# glibc floor gate is for. Anything else missing is a packaging bug.
#
# Usage:  scripts/payload-deps-gate.sh <payload-dir>
# Exit:   0 = every ELF resolves
#         2 = at least one unresolved dependency (do not ship)
#         3 = precondition failure (no dir / no ELF objects / no ldd)
set -uo pipefail

PAYLOAD="${1:-}"
[ -n "$PAYLOAD" ] || { echo "usage: $0 <payload-dir>" >&2; exit 3; }
[ -d "$PAYLOAD" ] || { echo "PRECONDITION-FAIL: $PAYLOAD is not a directory" >&2; exit 3; }
command -v ldd >/dev/null 2>&1 || { echo "PRECONDITION-FAIL: ldd not found" >&2; exit 3; }

PAYLOAD=$(cd "$PAYLOAD" && pwd -P)   # resolve: build/lib/ollama is a symlink into /usr/local/lib

# Supplied by the host, by design.
HOST_PROVIDED='^(libcuda\.so|libc\.so|libm\.so|libdl\.so|libpthread\.so|librt\.so|libstdc\+\+\.so|libgcc_s\.so|ld-linux|linux-vdso|libresolv\.so|libutil\.so)'

# Every directory in the payload is a search path: the loader gets the same view
# at runtime, because the runner sets LD_LIBRARY_PATH from these same dirs.
SEARCH=$(find -L "$PAYLOAD" -type d -printf '%p:' 2>/dev/null)

mapfile -t ELVES < <(find -L "$PAYLOAD" -type f \( -name '*.so' -o -name '*.so.*' -o -perm -u+x \) -print 2>/dev/null)
[ ${#ELVES[@]} -gt 0 ] || { echo "PRECONDITION-FAIL: no candidate files under $PAYLOAD" >&2; exit 3; }

rc=0
checked=0
for f in "${ELVES[@]}"; do
    [ "$(head -c4 "$f" 2>/dev/null | od -An -tx1 | tr -d ' \n')" = "7f454c46" ] || continue
    checked=$((checked + 1))
    missing=$(LD_LIBRARY_PATH="$SEARCH" ldd "$f" 2>/dev/null | awk '/not found/ {print $1}')
    [ -n "$missing" ] || continue
    while read -r lib; do
        [ -n "$lib" ] || continue
        if [[ "$lib" =~ $HOST_PROVIDED ]]; then
            echo "   note: $(basename "$f") wants host-provided $lib (not shipped, expected)"
            continue
        fi
        echo "UNRESOLVED: $(basename "$f") needs $lib — not in the payload and not host-provided"
        rc=2
    done <<< "$missing"
done

[ "$checked" -gt 0 ] || { echo "PRECONDITION-FAIL: no ELF objects under $PAYLOAD" >&2; exit 3; }

if [ "$rc" = 2 ]; then
    echo "PAYLOAD_DEPS_GATE: FAIL — the runner will dlopen these and fall back to CPU without an error"
    exit 2
fi
echo "PAYLOAD_DEPS_GATE: PASS ($checked ELF objects, all dependencies resolve)"
exit 0
