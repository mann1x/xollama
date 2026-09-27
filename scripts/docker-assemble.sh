#!/usr/bin/env bash
# Assemble the xollama container image's build context from pinned artifacts.
#
# Nothing native is compiled. Every native piece already exists as a published,
# sha256-pinned artifact (llama/runtime-pin-linux.txt, llm/engine/pin.txt);
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
#   SKIP_GO=1     reuse <out-dir>/rootfs/bin/xollama (local iteration only)
#
# Result: <out-dir>/rootfs/{bin/xollama,lib/ollama/...} plus the Dockerfile,
# ready for `docker build <out-dir>`.

set -euo pipefail

out=${1:?usage: $0 <out-dir>}
: "${VERSION:?set VERSION}"
: "${GO_TOOLCHAIN:?set GO_TOOLCHAIN (e.g. go1.26.8)}"
repo=$(git rev-parse --show-toplevel)
pin="$repo/llama/runtime-pin-linux.txt"
assets=${ASSET_DIR:-$out/.assets}
rootfs="$out/rootfs"
lib="$rootfs/lib/ollama"

fail() { echo "docker-assemble: $*" >&2; exit 1; }
field() { awk -v k="$1" '$1==k {print $2; exit}' "$pin"; }

mkdir -p "$assets" "$out"
rm -rf "$lib"
mkdir -p "$lib" "$rootfs/bin"

# --- the pins -------------------------------------------------------------

upstream=$(field upstream)
rrepo=$(field repo); rtag=$(field tag); rasset=$(field asset)
rsum=$(field sha256); rinputs=$(field inputs); rbuilt=$(field built)
for v in upstream rrepo rtag rasset rsum rinputs rbuilt; do
    [ -n "${!v}" ] || fail "$pin is missing a directive ($v)"
done

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
    || fail "the pinned runtime was built from llama inputs $rinputs, this commit has $now; move llama/runtime-pin-linux.txt to a runtime built from them"
echo "llama inputs $now match the pinned runtime ($rtag)"

# And the pin's claim about its own provenance, when the fork's commit is
# reachable (CI fetches it; a local clone may not have it).
if git -C "$repo" cat-file -e "$rbuilt^{commit}" 2>/dev/null; then
    [ "$(digest "$rbuilt")" = "$rinputs" ] \
        || fail "the pin says the runtime was built at $rbuilt, whose llama inputs are not $rinputs"
    echo "provenance: $rrepo@$rbuilt has the same llama inputs"
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
fetch "$rrepo" "$rtag" "$rasset" "$rsum"
tar -xzf "$assets/$rasset" -C "$rootfs/lib"

# 3. the opencoti engine, sha256-enforced by the same script the Dockerfile and
#    the release use. Whatever llm/engine/pin.txt names is what ships.
cmake -DPIN_FILE="$repo/llm/engine/pin.txt" -DARCH=x86_64 \
    -DDEST_DIR="$lib" -DCACHE_DIR="$assets/opencoti" \
    -P "$repo/cmake/opencoti-fetch.cmake"

# 4. the Go binary, inside AlmaLinux 8 (glibc 2.28) as the release builds it,
#    and its license bundle.
if [ -z "${SKIP_GO:-}" ]; then
    # A git worktree's .git names the main repository by host path; mount it
    # there so git (and cmake, which asks it) works inside the container.
    gitdir=$(git -C "$repo" rev-parse --path-format=absolute --git-common-dir)
    mounts=(-v "$repo:/src" -v "$rootfs:/out")
    case "$gitdir" in "$repo"/*) ;; *) mounts+=(-v "$gitdir:$gitdir:ro") ;; esac
    docker run --rm "${mounts[@]}" -w /src \
        -e VERSION -e WANT="$GO_TOOLCHAIN" -e CGO_ENABLED=1 -e CGO_LDFLAGS=-ldl -e GOTOOLCHAIN=local \
        -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
        almalinux:8 bash -euo pipefail -c '
            dnf install -y -q gcc gcc-c++ tar gzip findutils git cmake > /dev/null
            curl -fsSL "https://go.dev/dl/${WANT}.linux-amd64.tar.gz" | tar -C /usr/local -xz
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
cat > "$out/.dockerignore" <<'EOF'
.assets
EOF

# What the image carries, so a build log answers "which bytes" on its own.
{
    echo "xollama $VERSION ($GO_TOOLCHAIN)"
    echo "runtime $rrepo $rtag $rsum"
    awk '$1=="gpu" {print "gpu     ollama/ollama '"$upstream"' " $2 " " $3}' "$pin"
    awk '$1=="tag" || $1=="rev" {print "engine  " $1 " " $2}' "$repo/llm/engine/pin.txt"
} | tee "$lib/PAYLOAD"
du -sh "$lib"/* | sort -h | tail -12
