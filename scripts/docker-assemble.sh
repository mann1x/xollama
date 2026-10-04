#!/usr/bin/env bash
# Assemble the xollama container image's build context from pinned artifacts.
#
# Nothing native is compiled. Every native piece already exists as a published,
# sha256-pinned artifact (llama/runtime-pin-linux.txt, or
# llama/runtime-pin-linux-arm64.txt for arm64, and llm/engine/pin.txt);
# only the Go binary is built. This is the same script CI runs
# (.github/workflows/docker-release.yaml) and the one used to validate it
# locally, so the two cannot drift. Design: plans/docker-image.md.
#
#   scripts/docker-assemble.sh <out-dir>
#
# Environment:
#   VERSION       version baked into the binary (required)
#   GO_TOOLCHAIN  exact Go release to build with, e.g. go1.26.8 (required; CI's
#                 plan job picks the newest patch on go.mod's line)
#   ASSET_DIR     where downloads are kept and reused (default <out-dir>/.assets).
#                 Keep it off tmpfs: the upstream tarballs are 2.6 GB.
#   ARCH          amd64 (default) or arm64. The Go binary is built in a
#                 container of that architecture: natively on a runner of it,
#                 under qemu elsewhere.
#   SKIP_GO=1     reuse <out-dir>/rootfs/bin/xollama (local iteration only)
#   ALLOW_UPSTREAM_RUNTIME=1
#                 local testing only, refused in CI: when the pin has no
#                 runtime rows (the fork has not published one for this
#                 architecture yet), keep upstream's CPU llama-server instead
#                 of failing. lib/ollama/PAYLOAD says so. Never published.
#
# Result: <out-dir>/rootfs/{bin/xollama,lib/ollama/...} plus the Dockerfile,
# ready for `docker build <out-dir>`.

set -euo pipefail

out=${1:?usage: $0 <out-dir>}
: "${VERSION:?set VERSION}"
: "${GO_TOOLCHAIN:?set GO_TOOLCHAIN (e.g. go1.26.8)}"
repo=$(git rev-parse --show-toplevel)
arch=${ARCH:-amd64}
case "$arch" in
    amd64) pin="$repo/llama/runtime-pin-linux.txt"; earch=x86_64 ;;
    arm64) pin="$repo/llama/runtime-pin-linux-arm64.txt"; earch=aarch64 ;;
    *) echo "docker-assemble: ARCH is '$arch', want amd64 or arm64" >&2; exit 1 ;;
esac
assets=${ASSET_DIR:-$out/.assets}
rootfs="$out/rootfs"
lib="$rootfs/lib/ollama"

fail() { echo "docker-assemble: $*" >&2; exit 1; }
field() { awk -v k="$1" '$1==k {print $2; exit}' "$pin"; }

[ -f "$pin" ] || fail "no runtime pin for linux/$arch ($pin)"
mkdir -p "$assets" "$out"
rm -rf "$lib"
mkdir -p "$lib" "$rootfs/bin"

# --- the pins -------------------------------------------------------------

upstream=$(field upstream)
rrepo=$(field repo); rtag=$(field tag); rasset=$(field asset)
rsum=$(field sha256); rinputs=$(field inputs); rbuilt=$(field built)
[ -n "$upstream" ] || fail "$pin is missing a directive (upstream)"
# A pin without its runtime rows is one the fork has not filled yet. That is a
# refusal, except for a local test that says it wants upstream's CPU runtime.
fork_runtime=1
if [ -z "$rasset$rsum" ] && [ -n "${ALLOW_UPSTREAM_RUNTIME:-}" ]; then
    [ -z "${CI:-}" ] || fail "ALLOW_UPSTREAM_RUNTIME is for local testing, not CI"
    fork_runtime=
    echo "TEST BUILD: no fork runtime pinned for linux/$arch; keeping upstream $upstream's CPU llama-server"
else
    for v in rrepo rtag rasset rsum rinputs rbuilt; do
        [ -n "${!v}" ] || fail "$pin is missing a directive ($v): the fork has published no runtime for linux/$arch yet"
    done
fi

if [ -n "$fork_runtime" ]; then
    # The inputs digest, README excluded on both sides: the runtime was built by
    # the fork, whose llama/compat/README.md is not a build input and is allowed to
    # differ. Every other byte of LLAMA_CPP_VERSION, llama/server and llama/compat
    # must equal what the runtime was built from.
    digest() {
        git -C "$repo" ls-tree -r "$1" -- LLAMA_CPP_VERSION llama/server llama/compat \
            | grep -v '[[:space:]]llama/compat/README\.md$' | sha256sum | cut -c1-64
    }
    now=$(digest HEAD)
    [ "$now" = "$rinputs" ] \
        || fail "the pinned runtime was built from llama inputs $rinputs, this commit has $now; move ${pin##*/} to a runtime built from them"
    echo "llama inputs $now match the pinned runtime ($rtag)"

    # And the pin's claim about its own provenance, when the fork's commit is
    # reachable (CI fetches it; a local clone may not have it).
    if git -C "$repo" cat-file -e "$rbuilt^{commit}" 2>/dev/null; then
        [ "$(digest "$rbuilt")" = "$rinputs" ] \
            || fail "the pin says the runtime was built at $rbuilt, whose llama inputs are not $rinputs"
        echo "provenance: $rrepo@$rbuilt has the same llama inputs"
    fi
fi

# The GPU backends are upstream's, which is only sound while both sides build
# the same llama.cpp (xollama-release.yaml applies the same guard on Windows).
ours=$(tr -d '[:space:]' < "$repo/LLAMA_CPP_VERSION")
theirs=$(gh api "repos/ollama/ollama/contents/LLAMA_CPP_VERSION?ref=$upstream" --jq .content | base64 -d | tr -d '[:space:]') \
    || fail "cannot read LLAMA_CPP_VERSION at ollama/ollama $upstream"
[ "$ours" = "$theirs" ] \
    || fail "LLAMA_CPP_VERSION is $ours here but $theirs in upstream $upstream; the GPU backends cannot be borrowed"
echo "llama.cpp $ours on both sides; GPU backends from upstream $upstream"

fetch() { # repo tag asset sha256
    local f="$assets/$3"
    if [ -f "$f" ] && [ "$(sha256sum "$f" | cut -c1-64)" = "$4" ]; then
        echo "cached $3"
    else
        gh release download "$2" --repo "$1" --pattern "$3" --dir "$assets" --clobber
        local got
        got=$(sha256sum "$f" | cut -c1-64)
        [ "$got" = "$4" ] || fail "$1 $2 $3 is $got, the pin says $4"
    fi
}

# --- the payload, in overlay order ------------------------------------------

# 1. upstream's GPU backends. Its CPU llama-server comes along and is replaced
#    in step 2; nothing else of upstream's (its ollama binary) is kept.
while read -r _ asset sum; do
    fetch ollama/ollama "$upstream" "$asset" "$sum"
    zstd -dc "$assets/$asset" | tar -x -C "$rootfs" lib/ollama
    [ -n "${KEEP_ASSETS:-}" ] || [ -z "${CI:-}" ] || rm -f "$assets/$asset"
done < <(awk '$1=="gpu"' "$pin")

# 2. the fork's CPU runtime -- llama-server and the CPU ggml libraries built
#    with llama/compat applied. Its root is ollama/, i.e. lib/ollama.
if [ -n "$fork_runtime" ]; then
    fetch "$rrepo" "$rtag" "$rasset" "$rsum"
    tar -xzf "$assets/$rasset" -C "$rootfs/lib"
fi

# 3. the opencoti engine, sha256-enforced by the same script the Dockerfile and
#    the release use. Whatever llm/engine/pin.txt names is what ships: the
#    engine, its GPU payload and its media sidecars ("#! sidecar": the codec
#    and audio.cpp), each beside the engine under its published name.
cmake -DPIN_FILE="$repo/llm/engine/pin.txt" -DARCH="$earch" \
    -DDEST_DIR="$lib" -DCACHE_DIR="$assets/opencoti" \
    -P "$repo/cmake/opencoti-fetch.cmake"

# The pin's sidecar rows, as the fetch above staged them. A row whose file is
# not there, or is other bytes, would ship an engine that refuses mp3, mp4 and
# the audio.cpp voices.
sidecars=()
while read -r skind spath ssum; do
    sfile="$lib/$(basename "$spath")"
    echo "$ssum  $sfile" | sha256sum -c --quiet || fail "$skind sidecar $(basename "$spath") is not what the pin names"
    sidecars+=("$sfile")
done < <(awk '$1=="#!" && $2=="sidecar" && $3==a {print $4, $5, $6}' a="$earch" "$repo/llm/engine/pin.txt")
echo "media sidecars staged beside the engine: ${#sidecars[@]}"

# 3b. the CUDA 12 payload ("#! dso-cuda12", for the cards the CUDA 13 payload
#     has no code for). The engine loads the ggml-cuda library beside its own
#     executable and one process loads one payload, so it goes in
#     engines/cuda_v12 beside a copy of the engine; the server picks that
#     directory per load (llm/engine CUDA12Dirs / cudaPayload).
epin="$repo/llm/engine/pin.txt"
c12=$(awk '$1=="#!" && $2=="dso-cuda12" && $3==a {print $4, $5}' a="$earch" "$epin")
if [ -n "$c12" ]; then
    read -r c12path c12sum <<<"$c12"
    erepo=$(awk '$1=="repo" {print $2}' "$epin")
    erev=$(awk '$1=="rev" {print $2}' "$epin")
    engine=$(find "$lib" -maxdepth 1 -type f -name 'opencoti-*' -perm -u+x | head -1)
    [ -n "$engine" ] || fail "no opencoti engine staged in $lib to pair the CUDA 12 payload with"
    mkdir -p "$assets/opencoti" "$lib/engines/cuda_v12"
    c12file="$assets/opencoti/$(basename "$c12path")"
    if ! echo "$c12sum  $c12file" | sha256sum -c --status 2>/dev/null; then
        curl -fsSL --retry 3 -o "$c12file" "https://huggingface.co/$erepo/resolve/$erev/$c12path"
    fi
    echo "$c12sum  $c12file" | sha256sum -c --quiet || fail "CUDA 12 payload does not match the pin"
    cp -p "$engine" "$lib/engines/cuda_v12/"
    cp "$c12file" "$lib/engines/cuda_v12/ggml-cuda.so"
    # The engine looks for its sidecars in its own directory, so this copy
    # needs them too.
    [ ${#sidecars[@]} -eq 0 ] || cp -p "${sidecars[@]}" "$lib/engines/cuda_v12/"
    echo "CUDA 12 payload $c12sum staged in engines/cuda_v12"
fi

# 4. the Go binary, inside AlmaLinux 8 (glibc 2.28) as the release builds it,
#    and its license bundle.
if [ -z "${SKIP_GO:-}" ]; then
    # A git worktree's .git names the main repository by host path; mount it
    # there so git (and cmake, which asks it) works inside the container.
    gitdir=$(git -C "$repo" rev-parse --path-format=absolute --git-common-dir)
    mounts=(-v "$repo:/src" -v "$rootfs:/out")
    case "$gitdir" in "$repo"/*) ;; *) mounts+=(-v "$gitdir:$gitdir:ro") ;; esac
    docker run --rm --platform "linux/$arch" "${mounts[@]}" -w /src \
        -e VERSION -e GOARCH_DL="$arch" -e WANT="$GO_TOOLCHAIN" -e CGO_ENABLED=1 -e CGO_LDFLAGS=-ldl -e GOTOOLCHAIN=local \
        -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
        almalinux:8 bash -euo pipefail -c '
            dnf install -y -q gcc gcc-c++ tar gzip findutils git cmake > /dev/null
            # arm64: clang, as upstream builds it. AlmaLinux 8 gcc has no
            # arm_bf16.h, which the MLX headers include.
            if [ "$GOARCH_DL" = arm64 ]; then dnf install -y -q clang > /dev/null; export CC=clang CXX=clang++; fi
            curl -fsSL "https://go.dev/dl/${WANT}.linux-${GOARCH_DL}.tar.gz" | tar -C /usr/local -xz
            export PATH=/usr/local/go/bin:$PATH GOFLAGS=-buildvcs=false
            git config --global --add safe.directory /src
            ldflags="-s -w -X=github.com/ollama/ollama/version.Version=$VERSION -X=github.com/ollama/ollama/server.mode=release"
            go build -trimpath -buildmode=pie -ldflags "$ldflags" -o /out/bin/xollama .
            cmake -S . -B /tmp/go-license -DOLLAMA_LLAMA_BACKENDS= -DOLLAMA_MLX_BACKENDS= > /dev/null
            cmake --build /tmp/go-license --target ollama-go-license > /dev/null
            cp /tmp/go-license/lib/ollama/GO_LICENSE /out/lib/ollama/GO_LICENSE
            chown -R "$HOST_UID:$HOST_GID" /out/bin /out/lib/ollama/GO_LICENSE
        '
fi
[ -x "$rootfs/bin/xollama" ] || fail "no xollama binary in $rootfs/bin"
got=$(GOTOOLCHAIN=local go version "$rootfs/bin/xollama" 2>/dev/null | awk '{print $NF}' || true)
[ -z "$got" ] || [ "$got" = "$GO_TOOLCHAIN" ] || fail "built with $got, not $GO_TOOLCHAIN"

cp "$repo/Dockerfile.xollama" "$out/Dockerfile"
cp "$repo/scripts/cosmo-dlopen-helper.c" "$out/cosmo-dlopen-helper.c"
cat > "$out/.dockerignore" <<'EOF'
.assets
EOF

# What the image carries, so a build log answers "which bytes" on its own.
{
    echo "xollama $VERSION ($GO_TOOLCHAIN) linux/$arch"
    if [ -n "$fork_runtime" ]; then
        echo "runtime $rrepo $rtag $rsum"
    else
        echo "runtime UPSTREAM ollama/ollama $upstream (test build, not the fork's)"
    fi
    awk '$1=="gpu" {print "gpu     ollama/ollama '"$upstream"' " $2 " " $3}' "$pin"
    awk '$1=="tag" || $1=="rev" {print "engine  " $1 " " $2}' "$repo/llm/engine/pin.txt"
    awk '$1=="#!" && $2=="dso-cuda12" && $3==a {print "engine  cuda12 " $5}' a="$earch" "$repo/llm/engine/pin.txt"
    awk '$1=="#!" && $2=="sidecar" && $3==a {print "engine  sidecar " $4 " " $6}' a="$earch" "$repo/llm/engine/pin.txt"
} | tee "$lib/PAYLOAD"
du -sh "$lib"/* | sort -h | tail -12
