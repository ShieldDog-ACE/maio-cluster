#!/usr/bin/env bash
# Build llama.cpp with the RPC backend for the current machine.
#
#   ./scripts/build-rpc.sh            # CPU
#   ./scripts/build-rpc.sh --cuda     # NVIDIA
#   ./scripts/build-rpc.sh --metal    # Apple silicon
#
# Output: dist/runtime/{llama-server,ggml-rpc-server}
# Then bundle: RPC_BIN=... LLAMA_BIN=... make package-linux
set -euo pipefail
cd "$(dirname "$0")/.."

SRC="${LLAMA_SRC:-}"
if [[ -z "$SRC" ]]; then
  for c in llama.cpp-master ../llama.cpp-master ../../llama.cpp-master; do
    if [[ -d "$c" ]]; then SRC="$c"; break; fi
  done
fi
if [[ -z "$SRC" || ! -d "$SRC" ]]; then
  echo "error: llama.cpp source not found (set LLAMA_SRC)" >&2
  echo "clone it next to v4 or at the repository root" >&2
  exit 2
fi
BACKEND="cpu"
for a in "$@"; do
  case "$a" in
    --cuda) BACKEND=cuda ;;
    --metal) BACKEND=metal ;;
    --cpu) BACKEND=cpu ;;
    --help|-h) echo "usage: $0 [--cpu|--cuda|--metal]"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done

CMAKE="${CMAKE:-cmake}"
if ! command -v "$CMAKE" >/dev/null 2>&1; then
  echo "error: cmake not found; install cmake or set CMAKE" >&2
  exit 2
fi
BUILD="build-rpc-$BACKEND"
FLAGS=(-DCMAKE_BUILD_TYPE=Release -DGGML_RPC=ON -DLLAMA_CURL=OFF -DBUILD_SHARED_LIBS=OFF)
case "$BACKEND" in
  cuda) FLAGS+=(-DGGML_CUDA=ON) ;;
  metal) FLAGS+=(-DGGML_METAL=ON) ;;
  *) FLAGS+=(-DGGML_CUDA=OFF) ;;
esac
# Native tuning is fine for a binary shipped to the same machine class; use
# -DGGML_NATIVE=OFF for a portable build.
FLAGS+=(-DGGML_NATIVE="${GGML_NATIVE:-ON}")

"$CMAKE" -S "$SRC" -B "$BUILD" -G "${CMAKE_GENERATOR:-Ninja}" "${FLAGS[@]}"
"$CMAKE" --build "$BUILD" --config Release -j "${JOBS:-$(nproc 2>/dev/null || echo 4)}" \
  --target llama-server ggml-rpc-server

mkdir -p dist/runtime/bin
cp "$BUILD/bin/llama-server" dist/runtime/bin/llama-server
cp "$BUILD/bin/ggml-rpc-server" dist/runtime/bin/ggml-rpc-server
chmod 0755 dist/runtime/bin/*
echo "built: dist/runtime/bin"
ldd dist/runtime/bin/llama-server 2>/dev/null | head -8 || true
