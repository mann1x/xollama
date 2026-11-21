#!/usr/bin/env bash
# Cut the Linux release tarballs from a tree scripts/docker-assemble.sh made.
#
# The image and the tarballs are one payload: docker-assemble.sh stages it
# (upstream's GPU tarballs from the pin's `gpu` rows, the fork's CPU runtime,
# the engine and every file beside it, bin/xollama), and this script only
# splits that rootfs into the archives scripts/install.sh unpacks under its
# prefix (/usr/local), each under GitHub's 2 GiB asset cap:
#
#   xollama-linux-<arch>.tar.zst           bin/xollama and lib/ollama: the CPU
#                                          runtime and upstream's base GPU
#                                          libraries (CUDA 12 and 13, Vulkan)
#   xollama-linux-<arch>-opencoti.tar.zst  the engine, every file the pin stages
#                                          beside it, and its prebuilt dlopen
#                                          helper (lib/ollama/engines/payload/.cosmo)
#   xollama-linux-<arch>-<name>.tar.zst    one per further `gpu` row of the pin,
#                                          upstream's ollama-linux-<arch>-<name>
#                                          files: mlx (amd64), jetpack5 and
#                                          jetpack6 (arm64)
#
#   ARCH=<amd64|arm64> scripts/linux-tarballs.sh <assemble-out-dir> <dist-dir>
#
# <assemble-out-dir> must come from docker-assemble.sh run with
# LIST_DIR=<assemble-out-dir>/lists, so each gpu row's file list is known.
# Needs docker (the helper is compiled in AlmaLinux 8, glibc 2.28, the floor
# of the Linux binary) and zstd.

set -euo pipefail

out=${1:?usage: $0 <assemble-out-dir> <dist-dir>}
dist=${2:?usage: $0 <assemble-out-dir> <dist-dir>}
arch=${ARCH:-amd64}
repo=$(git rev-parse --show-toplevel)
case "$arch" in
    amd64) pin="$repo/llama/runtime-pin-linux.txt" ;;
    arm64) pin="$repo/llama/runtime-pin-linux-arm64.txt" ;;
    *) echo "linux-tarballs: ARCH is '$arch', want amd64 or arm64" >&2; exit 1 ;;
esac
rootfs="$out/rootfs"
lists="$out/lists"
manifest="$out/engine-manifest.txt"
cap=2147483648
fail() { echo "linux-tarballs: $*" >&2; exit 1; }

[ -x "$rootfs/bin/xollama" ] || fail "no $rootfs/bin/xollama: run docker-assemble.sh first"
[ -f "$manifest" ] || fail "no $manifest"
mkdir -p "$dist"
work=$(mktemp -d "$out/.tarballs.XXXXXX")
trap 'rm -rf "$work"' EXIT

# --- the dlopen helper, as Dockerfile.xollama builds it --------------------
# The engine compiles it with the system cc on first use, into the HOME
# xollama gives it (lib/ollama/engines/payload, engine.PayloadHome). A host
# without a compiler then fails every GPU load. Ship it built, with the old
# mtime the image uses (opencoti #504): the engine reuses it only while its
# source matches the engine's own.
cosmo="$rootfs/lib/ollama/engines/payload/.cosmo"
mkdir -p "$cosmo"
cp "$repo/scripts/cosmo-dlopen-helper.c" "$cosmo/dlopen-helper.c"
docker run --rm --platform "linux/$arch" -v "$cosmo:/cosmo" -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    almalinux:8 bash -euo pipefail -c '
        dnf install -y -q gcc glibc-devel > /dev/null
        cc -pie -fPIC /cosmo/dlopen-helper.c -o /cosmo/dlopen-helper -Wl,-z,execstack -ldl
        chown "$HOST_UID:$HOST_GID" /cosmo/dlopen-helper
    '
chmod 0644 "$cosmo/dlopen-helper.c"
touch -d 2000-01-01 "$cosmo/dlopen-helper" "$cosmo/dlopen-helper.c"

# --- who owns each file ----------------------------------------------------
# Paths are relative to rootfs. A tar list names directories with a trailing
# slash; only files and links are assigned.
( cd "$rootfs" && find . ! -type d | sed 's|^\./||' | LC_ALL=C sort ) > "$work/all"

{
    while read -r _ _ _ name _; do echo "lib/ollama/$name"; done < "$manifest"
    echo "lib/ollama/engines/payload/.cosmo/dlopen-helper"
    echo "lib/ollama/engines/payload/.cosmo/dlopen-helper.c"
} > "$work/engine"
LC_ALL=C sort -u -o "$work/engine" "$work/engine"

base_asset="ollama-linux-$arch.tar.zst"
addons=()
while read -r _ asset _; do
    [ -f "$lists/$asset.list" ] || fail "no file list for $asset: run docker-assemble.sh with LIST_DIR=$lists"
    [ "$asset" = "$base_asset" ] && continue
    name=${asset#ollama-linux-"$arch"-}; name=${name%.tar.zst}
    [ "$name" != "$asset" ] || fail "pin row $asset is not ollama-linux-$arch-<name>.tar.zst"
    grep -v '/$' "$lists/$asset.list" | LC_ALL=C sort -u > "$work/addon-$name"
    addons+=("$name")
done < <(awk '$1=="gpu"' "$pin")
[ -f "$lists/$base_asset.list" ] || fail "the pin has no $base_asset row"
grep -v '/$' "$lists/$base_asset.list" | LC_ALL=C sort -u > "$work/upstream-base"

# A file in two archives would be overwritten by whichever unpacks last; the
# image unpacks the gpu rows in pin order, so refuse rather than guess.
claimed=("$work/engine" "$work/upstream-base")
for n in "${addons[@]}"; do claimed+=("$work/addon-$n"); done
dups=$(LC_ALL=C sort "${claimed[@]}" | uniq -d | head -5)
[ -z "$dups" ] || fail "files claimed by two archives: $dups"

# Every add-on file must be in the tree, so the archive holds the image's bytes.
for f in "$work/engine" "${addons[@]/#/$work/addon-}"; do
    missing=$(LC_ALL=C comm -23 "$f" "$work/all" | head -3)
    [ -z "$missing" ] || fail "$(basename "$f"): not in the assembled tree: $missing"
done

LC_ALL=C sort -u "$work/engine" "${addons[@]/#/$work/addon-}" > "$work/taken"
LC_ALL=C comm -23 "$work/all" "$work/taken" > "$work/base"
grep -qx 'bin/xollama' "$work/base" || fail "bin/xollama is not in the base archive"

pack() { # list-file archive-name
    local dest="$dist/$2"
    tar -C "$rootfs" --owner=0 --group=0 --numeric-owner -cf - -T "$1" | zstd -q -T0 -12 -o "$dest" -f
    local size; size=$(stat -c %s "$dest")
    [ "$size" -lt "$cap" ] || fail "$2 is $size bytes, over GitHub's 2 GiB asset cap"
    printf '%-40s %6d files %6d MiB\n' "$2" "$(wc -l < "$1")" $((size / 1048576))
}
pack "$work/base" "xollama-linux-$arch.tar.zst"
pack "$work/engine" "xollama-linux-$arch-opencoti.tar.zst"
for n in "${addons[@]}"; do pack "$work/addon-$n" "xollama-linux-$arch-$n.tar.zst"; done
