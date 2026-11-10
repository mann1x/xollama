#!/usr/bin/env bash
# Build a Linux CPU companion with a pinned whisper.cpp and the Go frontend.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$root/build/whisper-timestamps}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
src="$root/build/whisper-timestamps-src"
revision=2eeeba56e9edd762b4b38467bab96c2517163158 # whisper.cpp v1.8.3
if [[ ! -d "$src/.git" ]]; then
    git clone --depth 1 --branch v1.8.3 https://github.com/ggml-org/whisper.cpp.git "$src"
fi
[[ "$(git -C "$src" rev-parse HEAD)" == "$revision" ]] || { echo "Unexpected whisper.cpp revision" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "Install ffmpeg before using word timestamps" >&2; exit 1; }
cmake -S "$src" -B "$src/build" -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF -DGGML_CUDA=OFF -DGGML_VULKAN=OFF \
    -DGGML_NATIVE=OFF -DWHISPER_BUILD_TESTS=OFF
cmake --build "$src/build" --target whisper-cli --parallel 8
cp "$src/build/bin/whisper-cli" "$out/whisper-cli"
cp "$src/LICENSE" "$out/LICENSE.whisper.cpp"
(cd "$root" && go build -o "$out/xollama" .)
echo "Built $out/xollama with $out/whisper-cli (requires ffmpeg in PATH)"
