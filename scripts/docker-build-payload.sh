#!/usr/bin/env bash
# docker-build-payload.sh — build the native payload in a pinned-glibc container.
#
# WHY. solidPC (Debian 11, GCC 10.2.1) cannot build the tree at the current
# llama.cpp pin: -DGGML_CPU_ALL_VARIANTS=ON compiles a sapphirerapids variant
# that needs AMX, and AMX needs GCC >= 11 (bug-012). The container carries
# GCC 12 on a Rocky 8 base, so the compiler is new enough to build the tree and
# the base is old enough (glibc 2.28) that the result still runs on Debian 11.
# Modelled on opencoti's release lane; see docs/protocols/DOCKER-BUILD-LANE.md.
#
# The build does NOT reuse build/. It uses build-docker/ so the container's
# toolchain never collides with a native CMakeCache, then syncs the payload into
# build/lib/ollama where the runtime looks for it.
#
# No GPU is needed to COMPILE CUDA, so this never passes --gpus. That is also
# why --arch must be explicit: the tree defaults CMAKE_CUDA_ARCHITECTURES to
# "native", which detects nothing in a container and silently builds for the
# wrong thing.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$REPO_ROOT"

# Default is local. bs2 is opencoti's release host: their c8 clean-room build
# and its gates own that machine, and a 32-thread build of ours landing on it
# would corrupt both their timings and any of ours taken there. Pass --host bs2
# only when you know it is free.
HOST=local
IMAGE=xollama/build:cuda13.3-glibc2.28
BACKENDS=cuda_v13
# Explicit, not "native" — see the header. Turing through Blackwell.
ARCH="75;80;86;89;90;120"
JOBS=""
CLEAN=0
MAX_FLOOR=29
GATE=1
NCCL=0
BUILD_DIR=build-docker
REMOTE_ROOT=/srv/ml/xollama-lane
LOCAL_CACHE=${XOLLAMA_LANE_CACHE:-/srv/ml/xollama-lane/ccache}

usage() { cat <<'USAGE'

usage: scripts/docker-build-payload.sh [options]
  --host local|bs2     build host (default: local; bs2 is opencoti's release host)
  --backends LIST      OLLAMA_LLAMA_BACKENDS (default: cuda_v13; "" for CPU only)
  --arch LIST          CMAKE_CUDA_ARCHITECTURES (default: 75;80;86;89;90;120)
  --jobs N             parallel jobs (default: nproc on the build host)
  --clean              wipe the build dir before configuring
  --max-floor N        glibc floor ceiling for the gate (default: 29)
  --no-gate            skip BOTH gates: glibc floor and payload deps (you will be asked why)
  --nccl               link libnccl (multi-GPU all-reduce). OFF by default: the
                       CUDA image ships NCCL, the target hosts do not, and a
                       payload that needs a 252 MB .so nobody has dlopens to
                       nothing and silently runs on CPU. See the deps gate.
  --image TAG          lane image tag (default: xollama/build:cuda13.3-glibc2.28)
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --host)      HOST="$2"; shift 2 ;;
        --backends)  BACKENDS="$2"; shift 2 ;;
        --arch)      ARCH="$2"; shift 2 ;;
        --jobs)      JOBS="$2"; shift 2 ;;
        --clean)     CLEAN=1; shift ;;
        --max-floor) MAX_FLOOR="$2"; shift 2 ;;
        --no-gate)   GATE=0; shift ;;
        --nccl)      NCCL=1; shift ;;
        --image)     IMAGE="$2"; shift 2 ;;
        -h|--help)   usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done

case "$HOST" in local|bs2) ;; *) echo "--host must be local or bs2" >&2; exit 2 ;; esac

say() { printf '\n== %s\n' "$*"; }

# The command run INSIDE the container, identical on both hosts. /src is the
# tree, /ccache is the persistent object cache. CCACHE_DIR is set explicitly
# rather than trusted to land somewhere: EL8's ccache 3.7.7 predates XDG and
# defaults to /root/.ccache, which is the container overlay and dies with --rm
# (opencoti bug-2381 — a mount that looked persistent and was not).
container_script() {
    cat <<CONTAINER
set -euo pipefail
# Own subdir: ccache shards its cache as 0-9/a-f at the root, which would sit
# directly beside go-build/ and go-mod/ and make the mount unreadable.
export CCACHE_DIR=/ccache/ccache
export CCACHE_MAXSIZE=${CCACHE_MAXSIZE:-20G}
# ggml sets this with set(ENV{...}) at CONFIGURE time, which does not survive
# into the build, so set it here where the compiles actually happen.
export CCACHE_SLOPPINESS=time_macros,include_file_mtime,include_file_ctime
# NOT /usr/lib64/ccache: those symlinks are EL8's ccache 3.7.7, and putting them
# on PATH made cmake record /usr/lib64/ccache/cc as THE compiler while ggml's
# RULE_LAUNCH_COMPILE also prefixed ccache -- every TU ran ccache twice. ggml
# wires the launcher itself (GGML_CCACHE, on by default), so the compilers here
# stay real and ccache resolves to /usr/local/bin/ccache 4.x.
export GOCACHE=/ccache/go-build
export GOMODCACHE=/ccache/go-mod
# VCS stamping is ON and must stay reproducible across hosts: the image carries
# `git config --global --add safe.directory /src` (the container is root and the
# tree is not) and the bs2 lane rsyncs .git deliberately, so the revision lands
# in the binary wherever it was built.
cd /src
echo "toolchain: \$(gcc --version | head -1) | \$(cmake --version | head -1) | \$(go version)"
if [ "$CLEAN" = 1 ]; then rm -rf "$BUILD_DIR"; fi
cmake -B "$BUILD_DIR" . \\
    ${BACKENDS:+-DOLLAMA_LLAMA_BACKENDS="$BACKENDS"} \\
    -DGGML_CUDA_NCCL=$([ "$NCCL" = 1 ] && echo ON || echo OFF) \\
    -DCMAKE_CUDA_ARCHITECTURES="$ARCH"
cmake --build "$BUILD_DIR" --parallel ${JOBS:-\$(nproc)}
CONTAINER
}

ensure_image_local() {
    if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
        say "building lane image $IMAGE (first run: pulls a ~12 GB CUDA base)"
        docker build -t "$IMAGE" "$REPO_ROOT/vendors/docker/xollama-build"
    fi
}

run_local() {
    ensure_image_local
    # Outside the repo on purpose: a Go module cache inside the tree pollutes
    # `gofmt -l .`, lands in git, and gets rsynced to the remote build host.
    mkdir -p "$LOCAL_CACHE"
    say "building in $IMAGE on this host"
    container_script | docker run --rm -i \
        -v "$REPO_ROOT":/src \
        -v "$LOCAL_CACHE":/ccache \
        -w /src "$IMAGE" bash -s
}

run_bs2() {
    say "syncing tree to bs2:$REMOTE_ROOT/src"
    ssh bs2 "mkdir -p $REMOTE_ROOT/src $REMOTE_ROOT/ccache"
    # .git IS synced, on purpose. Without it this host would stamp no VCS
    # revision into the Go binary while a local build stamped one, so the same
    # script and image would produce different binaries depending on where it
    # ran. ~131 MB once, then incremental.
    rsync -a --delete \
        --exclude 'build/' --exclude "$BUILD_DIR/" \
        --exclude 'node_modules/' --exclude '.cache/' \
        "$REPO_ROOT"/ "bs2:$REMOTE_ROOT/src/"
    rsync -a "$REPO_ROOT/vendors/docker/xollama-build/" "bs2:$REMOTE_ROOT/image/"
    ssh bs2 "docker image inspect '$IMAGE' >/dev/null 2>&1 || docker build -t '$IMAGE' $REMOTE_ROOT/image"
    say "building in $IMAGE on bs2"
    container_script | ssh bs2 "docker run --rm -i \
        -v $REMOTE_ROOT/src:/src \
        -v $REMOTE_ROOT/ccache:/ccache \
        -w /src '$IMAGE' bash -s"
    say "bringing the payload back"
    mkdir -p "$REPO_ROOT/$BUILD_DIR/lib"
    rsync -a "bs2:$REMOTE_ROOT/src/$BUILD_DIR/lib/ollama/" "$REPO_ROOT/$BUILD_DIR/lib/ollama/"
    rsync -a "bs2:$REMOTE_ROOT/src/xollama" "$REPO_ROOT/xollama.lane" 2>/dev/null || true
}

case "$HOST" in
    local) run_local ;;
    bs2)   run_bs2 ;;
esac

PAYLOAD="$REPO_ROOT/$BUILD_DIR/lib/ollama"
[ -d "$PAYLOAD" ] || { echo "no payload at $PAYLOAD — build produced nothing" >&2; exit 1; }

if [ "$GATE" = 1 ]; then
    say "glibc floor gate (<= GLIBC_2.$MAX_FLOOR)"
    mapfile -t ELVES < <(find "$PAYLOAD" -type f \( -name '*.so' -o -name '*.so.*' -o -perm -u+x \) \
                         -exec sh -c 'head -c4 "$1" | od -An -tx1 | tr -d " \n" | grep -q 7f454c46' _ {} \; -print)
    [ ${#ELVES[@]} -gt 0 ] || { echo "gate found no ELF objects under $PAYLOAD" >&2; exit 3; }
    "$REPO_ROOT/scripts/glibc-floor-gate.sh" --quiet --max "$MAX_FLOOR" "${ELVES[@]}"

    # The floor gate checks which glibc symbol versions an object NEEDS. It is
    # blind to a NEEDED library that is absent entirely -- which is how a
    # libnccl-linked libggml-cuda.so reached a promoted payload on 2026-09-19
    # and turned every run into a silent CPU run.
    say "payload dependency gate"
    "$REPO_ROOT/scripts/payload-deps-gate.sh" "$PAYLOAD"
fi

say "payload built at $BUILD_DIR/lib/ollama"
cat <<DONE
Nothing has been copied into build/lib/ollama yet — that is deliberate, so a
failed gate can never replace a working payload. When you are satisfied:

    rsync -a --delete $BUILD_DIR/lib/ollama/ build/lib/ollama/

DONE
