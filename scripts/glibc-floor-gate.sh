#!/usr/bin/env bash
# glibc-floor-gate.sh — check a built artifact's glibc symbol floor, on any host,
# without rebuilding it.
#
# THE CONTRACT. Everything the Docker build lane produces has to run on
# Debian-11-class hosts (solidPC is Debian 11 / glibc 2.31), so the maximum
# glibc symbol version any shipped ELF requires must be <= GLIBC_2.29. The lane
# image is Rocky 8 (glibc 2.28) precisely to clear that.
#
# WHY IT IS A HARD GATE AND NOT A WARNING. An over-floor object does not fail
# loudly. The loader refuses it, the caller catches the dlopen error, and the
# process carries on without the backend — opencoti's bug-2115 is the worked
# example: "no usable GPU found", ~5 tps, and a window sizer reporting host RAM
# as free VRAM. Nothing in the logs says "wrong glibc". That silence is the
# reason this exists.
#
# NO VERSION DATA IS A FAILURE, NOT A PASS. The naive form of this check —
#     FLOOR=$(strings "$f" | grep -oE 'GLIBC_2\.[0-9]+' | sort -uV | tail -1)
#     MINOR="${FLOOR#GLIBC_2.}"; [ -n "$MINOR" ] && [ "$MINOR" -gt 29 ] && fail
# prints "FLOOR OK" on a zero-byte file, on a text file, on anything `strings`
# finds nothing in. Every real shared object links libc and therefore carries
# GLIBC_* entries; zero entries means the file is not what you think it is, so
# this script exits 3 there and never 0.
#
# READELF IS THE SOURCE OF TRUTH, `strings` IS A CROSS-CHECK. .gnu.version_r is
# the actual dynamic-linker contract. `strings` scans the whole file including
# embedded fatbins and PTX, so it can report a version the loader never requires
# (harmless) and has no structural guarantee of finding one it does (dangerous).
# Both are read and any disagreement is reported rather than silently resolved.
#
# Ported from opencoti's scripts/glibc-floor-gate.sh (#885 / E5), which carries
# the original findings; kept in step deliberately rather than reimplemented.
#
# Usage:  scripts/glibc-floor-gate.sh [--max N] [--quiet] <file> [<file>...]
#         --max N   override the 29 in "GLIBC_2.<=N". Lower it against a
#                   known-good artifact to prove the gate can still FAIL before
#                   trusting any PASS.
# Exit:   0 = every file within the floor
#         2 = FLOOR VIOLATION on at least one file (do not ship)
#         3 = precondition failure (missing / not ELF / no version data / no readelf)
set -u

MAX=29
QUIET=0
FILES=()
while [ $# -gt 0 ]; do
    case "$1" in
        --max)   MAX="$2"; shift 2 ;;
        --quiet) QUIET=1; shift ;;
        -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
        *) FILES+=("$1"); shift ;;
    esac
done

[ ${#FILES[@]} -gt 0 ] || { echo "usage: $0 [--max N] <file>..."; exit 3; }
command -v readelf >/dev/null 2>&1 || { echo "PRECONDITION-FAIL: readelf not found (binutils)"; exit 3; }

# Highest GLIBC_2.<minor> in a stream. Emits the minor number, or nothing.
max_minor() { grep -oE 'GLIBC_2\.[0-9]+' | sed 's/GLIBC_2\.//' | sort -un | tail -1; }

# .gnu.version_r ONLY. `readelf -V` prints THREE sections — .gnu.version,
# .gnu.version_d (versions this object DEFINES) and .gnu.version_r (versions it
# REQUIRES) — and only the last is the loader contract. Parsing all of it inflates
# the floor for any object carrying its own version script: libc.so.6 reads as
# GLIBC_2.30 over the full output and GLIBC_2.3 over the needs section alone.
# That direction fails safe (a false violation, never a false pass) and is a no-op
# for our DSOs, which define no versioned symbols — but the verdict should mean
# what the header says it means.
version_needs() {
    readelf -V "$1" 2>/dev/null | awk '
        /^Version needs section/ { in_needs = 1; next }
        /^Version .* section/    { in_needs = 0 }
        in_needs'
}

rc=0
precond=0
for f in "${FILES[@]}"; do
    printf '== %s\n' "$f"

    if [ ! -f "$f" ]; then
        echo "   PRECONDITION-FAIL: not a regular file"; precond=1; continue
    fi
    if [ ! -s "$f" ]; then
        echo "   PRECONDITION-FAIL: file is EMPTY (0 bytes) — this is exactly what the inline"
        echo "                      lane check reports as 'FLOOR OK'"; precond=1; continue
    fi
    # ELF magic: 0x7F 'E' 'L' 'F'
    if [ "$(head -c4 "$f" | od -An -tx1 | tr -d ' \n')" != "7f454c46" ]; then
        echo "   PRECONDITION-FAIL: not an ELF object (bad magic)"; precond=1; continue
    fi

    ver=$(version_needs "$f")
    r_minor=$(printf '%s' "$ver" | max_minor)
    s_minor=$(strings "$f" 2>/dev/null | max_minor)

    if [ -z "$r_minor" ]; then
        echo "   PRECONDITION-FAIL: ELF carries NO GLIBC_* version requirements."
        echo "                      Every real shared object links libc; zero entries means this"
        echo "                      is not the artifact you think it is. NOT treated as a pass."
        precond=1; continue
    fi

    [ "$QUIET" = 1 ] || {
        echo "   required (readelf .gnu.version_r): $(printf '%s' "$ver" \
             | grep -oE 'GLIBC_2\.[0-9]+' | sort -uV | tr '\n' ' ')"
        for extra in GLIBCXX CXXABI; do
            e=$(printf '%s' "$ver" | grep -oE "${extra}_[0-9.]+" | sort -uV | tail -1)
            [ -n "$e" ] && echo "   advisory ($extra, separate contract): max $e"
        done
    }

    if [ -n "$s_minor" ] && [ "$s_minor" != "$r_minor" ]; then
        echo "   NOTE: strings-max (2.$s_minor) != readelf-max (2.$r_minor). readelf is"
        echo "         authoritative (it is the loader's actual requirement list); strings also"
        echo "         scans embedded fatbin/PTX/rodata. Verdict uses readelf."
    fi

    if [ "$r_minor" -gt "$MAX" ]; then
        echo "   FLOOR VIOLATION: GLIBC_2.$r_minor > GLIBC_2.$MAX — DO NOT SHIP"
        echo "                    (this DSO will not dlopen on Debian-11-class hosts, and"
        echo "                     llamafile will fall back to CPU without an error — bug-2115)"
        rc=2
    else
        echo "   FLOOR OK: GLIBC_2.$r_minor <= GLIBC_2.$MAX"
    fi
done

[ "$precond" = 1 ] && { echo "GLIBC_FLOOR_GATE: PRECONDITION-FAIL"; exit 3; }
[ "$rc" = 2 ] && { echo "GLIBC_FLOOR_GATE: FAIL (floor violation)"; exit 2; }
echo "GLIBC_FLOOR_GATE: PASS (max <= GLIBC_2.$MAX, ${#FILES[@]} file(s))"
exit 0
