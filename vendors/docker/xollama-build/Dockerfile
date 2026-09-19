# xollama — pinned-glibc native build image.
#
# WHY THIS EXISTS. solidPC is Debian 11 (glibc 2.31, GCC 10.2.1) and, since the
# llama.cpp pin moved to b10969, it can no longer build the payload at all:
# cmake/local.cmake passes -DGGML_CPU_ALL_VARIANTS=ON, the sapphirerapids
# variant now needs AMX intrinsics, and AMX needs GCC >= 11 —
#
#     cc: error: unrecognized command-line option '-mamx-tile'
#
# That is bug-012, and it is why build/lib/ollama sat at 2026-09-02 while
# LLAMA_CPP_VERSION moved to b10969 on 2026-09-15: the payload silently stayed
# at the last version that happened to compile. A container fixes both halves
# at once — a compiler new enough to build the tree, on a base old enough that
# what comes out still runs on Debian 11.
#
# Modelled on opencoti's release lane (opencoti/dso-build:cuda13.3-glibc2.28,
# docs/protocols/BUILD_CYCLE.md "The lane image"). Same base, same reasoning.
# Build:  docker build -t xollama/build:cuda13.3-glibc2.28 .
#
# --- why each piece is here (do not drop one without reading this) ----------
#
# nvidia/cuda:13.3.1-devel-rockylinux8
#     Rocky 8 => glibc 2.28, under the 2.29 ceiling every shipped ELF has to
#     clear (opencoti bug-2190/bug-2219; solidPC itself is 2.31). The `.1` IS
#     u1 and is deliberate: opencoti's #713 found CUDA 13.2 miscompiled sm_120
#     2/3-bit quants, and their bug-2382 was this exact pin silently reading
#     13.3.0. Verify with `nvcc --version` -> V13.3.73.
#
# gcc-toolset-12 + the ENV PATH line
#     The whole point: GCC 12 has AMX, the base Rocky 8 gcc 8 does not. The
#     PATH line is load-bearing — without it the base gcc 8 wins and the build
#     fails exactly the way solidPC's does. DTS links libstdc++_nonshared
#     statically, so compiling with gcc 12 does NOT raise the GLIBCXX floor.
#
# CMake from Kitware, not dnf
#     CMakeLists.txt requires >= 3.24 and EL8's packaged cmake has been below
#     that. Pinned rather than "whatever dnf has" so the image is reproducible.
#
# Go from the official tarball
#     cmake/local.cmake defines `ollama-go` as an ALL target that runs
#     `cmake -E false` when Go is missing, so `cmake --build build` cannot
#     complete without it — the Go toolchain is not optional here even though
#     only the native payload is what solidPC cannot build. Must match go.mod.
#
# git safe.directory /src
#     So the VCS stamp works. Both hosts now carry .git — the bs2 lane rsyncs it
#     deliberately — so the binary is stamped identically wherever it is built,
#     which is the property -buildvcs=false was protecting before.
#
# ccache 4.10.2, from the upstream static tarball — NOT EL8's 3.7.7
#     3.7.7 CANNOT cache this build's CUDA, measured rather than assumed. nvcc
#     names per-arch temporaries after itself, so with six -gencode targets the
#     dependency output references files like
#     tmpxft_00000031_00000000-6_norm.compute_120a.cudafe1.stub.c that are gone
#     by the time ccache stats them. Direct mode is refused ("Failed to stat
#     ... No such file or directory"), the fallback hashes preprocessor output
#     that embeds those varying tmpxft_ names, and every CUDA TU misses forever:
#     a full build left `cache hit (direct) 0`, and recompiling an UNCHANGED
#     file still missed. 4.10.2 handles it — the same multi-arch nvcc command
#     twice is Direct 1/1 (100%). The tarball is static, so it runs on 2.28.
#
#     Persistence is the lane's job: it bind-mounts a dir and sets CCACHE_DIR.
#     ccache 3.x defaulted to /root/.ccache, the container overlay, which dies
#     with --rm — opencoti bug-2381 is exactly that, a mount that looked
#     persistent and was not.

FROM nvidia/cuda:13.3.1-devel-rockylinux8

ARG GO_VERSION=1.26.0
ARG CMAKE_VERSION=3.28.6

RUN dnf install -y gcc-toolset-12-gcc gcc-toolset-12-gcc-c++ \
      git make python3 tar gzip xz which findutils binutils \
 && (dnf install -y epel-release \
      || dnf install -y https://dl.fedoraproject.org/pub/epel/epel-release-latest-8.noarch.rpm \
      || true) \
 && (dnf install -y ccache || true) \
 && dnf clean all

# Ahead of the base gcc 8. Load-bearing; see the header.
ENV PATH=/opt/rh/gcc-toolset-12/root/usr/bin:$PATH

RUN curl -fsSL "https://github.com/Kitware/CMake/releases/download/v${CMAKE_VERSION}/cmake-${CMAKE_VERSION}-linux-x86_64.tar.gz" \
      | tar -xz -C /usr/local --strip-components=1

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
      | tar -xz -C /usr/local

# Ahead of EL8's /usr/bin/ccache 3.7.7 on PATH. See the header for why 3.7.7 is
# not good enough here.
ARG CCACHE_VERSION=4.10.2
RUN curl -fsSL "https://github.com/ccache/ccache/releases/download/v${CCACHE_VERSION}/ccache-${CCACHE_VERSION}-linux-x86_64.tar.xz" \
      -o /tmp/ccache.tar.xz \
 && tar -xf /tmp/ccache.tar.xz -C /tmp \
 && install -m 0755 "/tmp/ccache-${CCACHE_VERSION}-linux-x86_64/ccache" /usr/local/bin/ccache \
 && rm -rf /tmp/ccache.tar.xz "/tmp/ccache-${CCACHE_VERSION}-linux-x86_64"
ENV PATH=/usr/local/go/bin:$PATH
ENV GOTOOLCHAIN=local

# Asserts, not documentation. Each one has already been a real failure:
# the wrong gcc on PATH (bug-012's shape), a cmake too old for the tree, a Go
# that does not match go.mod, and a CUDA pin that read GA instead of u1.
RUN set -eux; \
    gcc --version | head -1; \
    test "$(gcc -dumpversion | cut -d. -f1)" -ge 11; \
    echo 'int main(){return 0;}' | gcc -mamx-tile -mamx-int8 -x c - -o /dev/null; \
    cmake --version | head -1; \
    cmake -P /dev/stdin <<<'cmake_minimum_required(VERSION 3.24)'; \
    go version | grep -q "go${GO_VERSION}"; \
    command -v ccache | grep -qx /usr/local/bin/ccache; \
    ccache --version | head -1 | grep -qE "ccache version 4\\."; \
    nvcc --version | grep -q 'V13\.3\.'; \
    ldd --version | head -1

# The lane bind-mounts the repo at /src. The container runs as root while the
# tree is owned by whoever checked it out, so git refuses it:
#
#     fatal: detected dubious ownership in repository at '/src'
#
# and `go build` turns that into "error obtaining VCS status: exit status 128"
# while trying to stamp the revision into the binary. The exception is scoped to
# the mount point rather than '*' so an unexpected repo elsewhere in the
# container still gets the check.
RUN git config --global --add safe.directory /src

WORKDIR /src
