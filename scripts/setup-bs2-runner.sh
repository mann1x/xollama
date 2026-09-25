#!/usr/bin/env bash
# Prepare bs2 as the self-hosted runner that .github/workflows/docker-release.yaml
# builds the container image on.
#
# Run this ONCE, on bs2, as root. It is idempotent: every step checks before it
# acts, so a re-run after a partial failure is safe.
#
# It does NOT build anything and never touches the opencoti tree. bs2 is
# opencoti's build and gate host as well, and its BUILD_CYCLE protocol forbids
# starting a 32-thread job while a compile or a CPU-bound eval is live -- so run
# this when the box is quiet, and note that the workflow itself re-checks that
# on every release through its `preflight` job.
#
# Three things are installed:
#   1. the docker buildx plugin, which bs2 does not have and the workflow needs
#      for multi-platform builds and for `imagetools`;
#   2. QEMU binfmt handlers, so the emulated linux/arm64 leg can run at all;
#   3. the GitHub Actions runner, as a systemd service, labelled xollama-build.
#
# The registration token is fetched at run time and is valid for one hour. It is
# never written to disk here and must never be committed.

set -euo pipefail

REPO="${REPO:-mann1x/xollama}"
RUNNER_DIR="${RUNNER_DIR:-/srv/ml/actions-runner}"
RUNNER_USER="${RUNNER_USER:-root}"
LABELS="${LABELS:-xollama-build}"
RUNNER_NAME="${RUNNER_NAME:-bs2}"

log() { printf '\n== %s\n' "$*"; }

if [ "$(id -u)" -ne 0 ]; then
    echo "run this as root on bs2" >&2
    exit 1
fi

log "host check"
echo "  hostname: $(hostname)"
echo "  arch:     $(uname -m)"
echo "  cores:    $(nproc)"
echo "  free:     $(df -h --output=avail / | tail -1 | tr -d ' ') on /"
if [ "$(uname -m)" != "x86_64" ]; then
    echo "  note: this host is not x86_64; the workflow's X64 label will not match" >&2
fi

# The workflow builds inside Docker and caches to a registry, so the space it
# needs is the image cache. The ROCm build base alone is ~8 GiB compressed.
avail_gb=$(df --output=avail -BG / | tail -1 | tr -dc '0-9')
if [ "${avail_gb:-0}" -lt 80 ]; then
    echo "  WARNING: only ${avail_gb}G free on /; a cold build pulls ~30G of base images" >&2
fi

log "is the box busy? (opencoti BUILD_CYCLE preflight)"
if pgrep -x nvcc >/dev/null 2>&1 || pgrep -x cicc >/dev/null 2>&1 || pgrep -x ptxas >/dev/null 2>&1; then
    echo "  a compile is running -- installing is still safe, but do not trigger a build yet" >&2
else
    echo "  no compile running"
fi
echo "  load: $(cut -d' ' -f1-3 /proc/loadavg)"

log "docker buildx plugin"
if docker buildx version >/dev/null 2>&1; then
    echo "  already installed: $(docker buildx version)"
else
    echo "  installing docker-buildx-plugin"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq
        apt-get install -y docker-buildx-plugin
    else
        echo "  no apt-get; install the buildx plugin manually" >&2
        exit 1
    fi
    docker buildx version
fi

log "QEMU binfmt handlers (for the emulated linux/arm64 leg)"
if [ -e /proc/sys/fs/binfmt_misc/qemu-aarch64 ]; then
    echo "  already registered"
else
    echo "  registering via tonistiigi/binfmt"
    docker run --privileged --rm tonistiigi/binfmt --install arm64
fi

log "buildx builder"
if docker buildx inspect xollama >/dev/null 2>&1; then
    echo "  builder 'xollama' already exists"
else
    docker buildx create --name xollama --driver docker-container --bootstrap
fi
docker buildx inspect xollama | sed -n '1,12p'

log "GitHub Actions runner"
if [ -f "${RUNNER_DIR}/.runner" ]; then
    echo "  a runner is already configured in ${RUNNER_DIR}"
    echo "  remove it first if you need to re-register:"
    echo "    cd ${RUNNER_DIR} && ./svc.sh stop && ./svc.sh uninstall && ./config.sh remove"
    exit 0
fi

command -v gh >/dev/null 2>&1 || { echo "gh CLI is required to mint the registration token" >&2; exit 1; }

mkdir -p "${RUNNER_DIR}"
cd "${RUNNER_DIR}"

if [ ! -x ./config.sh ]; then
    version=$(curl -fsSL https://api.github.com/repos/actions/runner/releases/latest | grep -m1 '"tag_name"' | cut -d'"' -f4)
    version="${version#v}"
    echo "  downloading actions runner ${version}"
    curl -fsSL -o runner.tar.gz \
        "https://github.com/actions/runner/releases/download/v${version}/actions-runner-linux-x64-${version}.tar.gz"
    tar xzf runner.tar.gz
    rm -f runner.tar.gz
fi

echo "  minting a registration token (valid one hour, not stored)"
token=$(gh api -X POST "repos/${REPO}/actions/runners/registration-token" --jq .token)

./config.sh \
    --unattended \
    --url "https://github.com/${REPO}" \
    --token "${token}" \
    --name "${RUNNER_NAME}" \
    --labels "${LABELS}" \
    --work _work \
    --replace

# The runner must survive a reboot, and it must run as a user that can talk to
# the docker socket -- the workflow's every step is a docker call.
./svc.sh install "${RUNNER_USER}"
./svc.sh start
./svc.sh status | sed -n '1,10p'

log "done"
cat <<'NOTE'
Verify from anywhere:

    gh api repos/mann1x/xollama/actions/runners --jq '.runners[] | "\(.name) \(.status) \(.labels|map(.name)|join(","))"'

The runner must report labels including: self-hosted, linux, X64, xollama-build
NOTE
