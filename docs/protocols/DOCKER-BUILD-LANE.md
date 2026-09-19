# Docker build lane — pinned glibc, GCC 12

## Why this exists

solidPC cannot build the tree any more. `cmake/local.cmake` passes
`-DGGML_CPU_ALL_VARIANTS=ON`, so every microarch CPU variant is compiled; at the
`LLAMA_CPP_VERSION` pin `b10969` the sapphirerapids variant needs AMX
intrinsics, and AMX needs **GCC ≥ 11**. solidPC is Debian 11 with GCC 10.2.1:

```
cc: error: unrecognized command-line option '-mamx-tile'
gmake: *** [Makefile:136: all] Error 2
```

That is bug-012, and it is why `build/lib/ollama` was still dated
**2026-09-02** while the pin moved to b10969 on **2026-09-15**. Nothing failed
loudly — the payload simply stayed at the last version that happened to
compile, and a two-week-old runtime was mistaken for a model bug (bug-011).

A container solves both halves at once: a compiler new enough to build the tree,
on a base old enough that what comes out still runs on Debian 11.

## The contract

Every ELF the lane produces must require **≤ GLIBC_2.29**, so it loads on
Debian-11-class hosts. The lane image is Rocky 8 (**glibc 2.28**) for exactly
that reason, and `scripts/glibc-floor-gate.sh` enforces it after every build.

The gate is hard, not advisory, because an over-floor object does not fail
loudly: the loader refuses it, the caller swallows the `dlopen` error, and the
process runs on without the backend. opencoti's bug-2115 is the worked example —
"no usable GPU found", ~5 tps, and a window sizer reporting host RAM as free
VRAM, with nothing anywhere saying "wrong glibc".

## The image — `xollama/build:cuda13.3-glibc2.28`

Canonical Dockerfile: [`vendors/docker/xollama-build/Dockerfile`](../../vendors/docker/xollama-build/Dockerfile).
Copies deployed to a build host are deployments; edit the repo file.

| Piece | Why (do not drop without reading) |
|---|---|
| `nvidia/cuda:13.3.1-devel-rockylinux8` | Rocky 8 ⇒ glibc **2.28**, under the 2.29 ceiling. The `.1` **is** u1 and is deliberate — opencoti's #713 found CUDA 13.2 miscompiled sm_120 2/3-bit quants, and their bug-2382 was this exact pin silently reading `13.3.0`. Verify with `nvcc --version` → `V13.3.73`. |
| `gcc-toolset-12` + the `ENV PATH` line | The entire point: GCC 12 has AMX, the base Rocky 8 gcc 8 does not. **The `PATH` line is load-bearing** — without it the base gcc 8 wins and the build fails exactly the way solidPC's does. DTS links `libstdc++_nonshared` statically, so compiling with gcc 12 does not raise the GLIBCXX floor. |
| CMake from Kitware, pinned | `CMakeLists.txt` requires ≥ 3.24; EL8's packaged cmake has been below that. Pinned rather than "whatever dnf has" so the image is reproducible. |
| Go from the official tarball, matching `go.mod` | `cmake/local.cmake` makes `ollama-go` an `ALL` target that runs `cmake -E false` when Go is missing, so `cmake --build` **cannot complete** without it — even though only the native payload is what solidPC cannot build. |
| `git config --global --add safe.directory /src` | The container runs as root against a tree it does not own, so git refuses with `fatal: detected dubious ownership in repository at '/src'` and `go build` reports `error obtaining VCS status: exit status 128` while trying to stamp the revision. Scoped to the mount point rather than `*`, so an unexpected repo elsewhere still gets the check. |
| `ccache` (EPEL) | Object cache, persistent only because the lane bind-mounts it **and sets `CCACHE_DIR`**. EL8 ships ccache 3.7.7, which predates XDG and defaults to `/root/.ccache` — the container overlay, which dies with `--rm`. opencoti's bug-2381 is precisely this mistake. |

The final image layer is a set of **asserts, not documentation**: gcc ≥ 11, an
actual `-mamx-tile -mamx-int8` compile, cmake ≥ 3.24, `go version` matching the
`GO_VERSION` arg, and `nvcc` reading `V13.3.x`. Each one has already been a real
failure.

## Running it

```sh
scripts/docker-build-payload.sh --host bs2                 # default
scripts/docker-build-payload.sh --host local               # solidPC
scripts/docker-build-payload.sh --host local --arch 86     # quick 3090-only respin
scripts/docker-build-payload.sh --backends "" --no-gate    # CPU-only smoke
```

| Option | Default | Notes |
|---|---|---|
| `--host` | `bs2` | `bs2` rsyncs the tree out and the payload back; `local` builds in place. |
| `--backends` | `cuda_v13` | `OLLAMA_LLAMA_BACKENDS`. Empty string for a CPU-only payload. |
| `--arch` | `75;80;86;89;90;120` | See the warning below. |
| `--jobs` | `nproc` on the build host | |
| `--max-floor` | `29` | Lower it against a known-good artifact to prove the gate can still fail. |

### `--arch` must be explicit

`llama/server/CMakeLists.txt` defaults `CMAKE_CUDA_ARCHITECTURES` to `native`.
No GPU is needed to *compile* CUDA, so the lane deliberately never passes
`--gpus` — which means `native` would detect nothing. The lane always passes an
explicit set. The default covers Turing through Blackwell so one payload serves
the 3090, bs2's cards and pandorum.

### `.git` is synced to bs2 on purpose

The Go binary is VCS-stamped (`vcs.revision`, `vcs.time`, `vcs.modified`), and a
stamp is only possible where `.git` is visible. Excluding it from the bs2 rsync
would mean the same script and the same image produced a **stamped** binary on
solidPC and an **unstamped** one on bs2 — a difference in the artifact decided by
where it happened to be built. So the lane syncs `.git` (~131 MB once, then
incremental) and the image carries the `safe.directory` exception that makes git
usable in there. Verify with:

```sh
go version -m build-docker/lib/ollama/../../xollama | grep vcs.
```

### Caching: two layers, and the one that matters

| Layer | What it protects | Where |
|---|---|---|
| `build-docker/` | CMake/make incrementality — the dominant effect | in the repo, survives runs |
| ccache | recompiles CMake decides it *does* need | `/srv/ml/xollama-lane/ccache/ccache` |

A no-op rebuild is **~10 s**; a full cold build is ~40 min. The persistent
`build-docker/` tree is what buys that, so never delete it to "start clean"
unless the toolchain itself changed.

**ccache must be 4.x.** EL8's 3.7.7 cannot cache this build's CUDA at all:
nvcc names per-arch temporaries after itself, so with six `-gencode` targets the
dependency output references `tmpxft_*.cudafe1.stub.c` files that are gone by the
time ccache stats them. Direct mode is refused and the preprocessed fallback
hashes those varying names, so every CUDA TU misses forever — a full build left
`cache hit (direct) 0`, and recompiling an *unchanged* file still missed. This is
invisible unless you look: single-arch nvcc hits fine on 3.7.7, so a quick probe
says the wiring is correct.

Check it with the same test rather than trusting the config — compile the same
content twice and require hits:

```sh
touch build-docker/_deps/llama_cpp-src/ggml/src/ggml-cuda/norm.cu
scripts/docker-build-payload.sh --host local --no-gate     # populates
touch build-docker/_deps/llama_cpp-src/ggml/src/ggml-cuda/norm.cu
scripts/docker-build-payload.sh --host local --no-gate     # must HIT
docker run --rm -v /srv/ml/xollama-lane/ccache:/ccache xollama/build:cuda13.3-glibc2.28 \
  bash -c 'CCACHE_DIR=/ccache/ccache ccache -s'
```

Only one ccache layer is wired: ggml sets `RULE_LAUNCH_COMPILE` itself
(`GGML_CCACHE`, on by default), which covers C, C++ **and** CUDA. Putting
`/usr/lib64/ccache` on PATH as well made cmake record `/usr/lib64/ccache/cc` as
the compiler and ran ccache twice per TU, so the lane deliberately does not.

### It does not write `build/`

The lane builds into `build-docker/` and stops. Two reasons: the container's
toolchain would collide with a native `CMakeCache.txt` in `build/`, and a failed
floor gate must never be able to replace a working payload. Promote by hand
after the gate passes:

```sh
rsync -a --delete build-docker/lib/ollama/ build/lib/ollama/
```

## Two gates, because they answer different questions

The lane will not call a payload good until both pass. They look similar and are
not interchangeable.

| | `glibc-floor-gate.sh` | `payload-deps-gate.sh` |
|---|---|---|
| asks | which glibc **symbol versions** does this object need? | are this object's **NEEDED libraries** actually present? |
| catches | built on too new a host; `GLIBC_2.38` in a binary meant for Debian 11 | a library the build host had and the target host does not |
| blind to | a library missing outright | a symbol version that is too new |
| exit | 0 pass / 2 over ceiling / 3 precondition | 0 pass / 2 unresolved / 3 precondition |

The second gate exists because the first one passed a broken payload. On
2026-09-19 the floor gate reported `PASS (max <= GLIBC_2.29, 27 file(s))` on a
`libggml-cuda.so` that could not load at all: the CUDA image ships
`libnccl-2.30.7`, ggml's `GGML_CUDA_NCCL` defaults **ON** and is guarded only by
`find_package(NCCL)`, so the object came out with `NEEDED libnccl.so.2` and the
host has no NCCL. ggml treats a backend that will not `dlopen` as *absent*
rather than as an error, so the entire symptom was one line —

```
warning: no usable GPU found, --gpu-layers option will be ignored
```

— followed by a clean, plausible, completely CPU-bound run. Exit code 0. That is
bug-015, and the reason nothing gets promoted on a serve log alone.

The deps gate resolves every shipped ELF against the payload's own directories
plus an explicit allowlist of libraries the host genuinely provides (`libcuda`,
`libc`/`libm`/`libdl`/`libpthread`/`librt`, `libstdc++`, `libgcc_s`,
`ld-linux`). Anything else unresolved fails the build. Note it needs `find -L`
and `pwd -P`: `build/lib/ollama` is a symlink to `/usr/local/lib/ollama`, and
without those the gate walks an empty tree and reports a precondition failure on
a payload that is sitting right there.

### `--nccl` is off by default

NCCL is only the multi-GPU all-reduce path, and its init is chained
`nccl -> internal -> none`, so a single-GPU host loses nothing and a multi-GPU
host falls back to the internal butterfly path. Shipping `libnccl.so.2` in the
payload instead would add **252 MB**. Pass `--nccl` only when the target host
demonstrably has NCCL installed.

Because `-DGGML_*` cache vars are forwarded into the per-backend sub-build by
`ollama_collect_cache_args_with_prefix` (`cmake/local.cmake:425`), the top-level
flag is enough. Be aware that flipping it is **not** a cheap rebuild: dropping
`GGML_USE_NCCL` invalidates every TU under `ggml/src/ggml-cuda/` because it is a
directory-wide `add_compile_definitions`, so ccache cannot help and you pay a
full multi-arch CUDA compile (~40 min on solidPC).

## Host choice

Both hosts work; the script is the same either way.

| | solidPC (`--host local`) | bs2 (`--host bs2`) |
|---|---|---|
| cores | 12 | 32 |
| syncing | none — it is the target host | tree out, payload back |
| native floor | 2.31 (shippable, but cannot compile) | 2.39 — **never shippable natively** |

**Check bs2's load first.** It is shared with opencoti's release lane, and a
saturated bs2 means both builds crawl — their protocol carries the same warning
in the other direction. `ssh bs2 uptime` before launching a 32-thread job.

## Related

- bug-011 / bug-012 / bug-014 / bug-015 in `.wolf/buglog.json`
- [`features/gemma4-drafter.md`](../features/gemma4-drafter.md) — the work that
  surfaced the stale payload
- opencoti's `docs/protocols/BUILD_CYCLE.md`, sections "Release DSO — glibc-2.29
  floor lane" and "The lane image" — this lane is modelled on it, and
  `scripts/glibc-floor-gate.sh` is ported from theirs rather than reimplemented.
