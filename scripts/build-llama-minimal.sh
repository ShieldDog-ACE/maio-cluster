#!/usr/bin/env bash
# Build a minimal llama-server from source for Linux x86-64 only.
#
# What "minimal" means here:
#   - only the llama-server executable is built (no cli, bench, quantize,
#     tests, examples, Web UI or Python tooling)
#   - static llama/ggml libraries are linked into the server (BUILD_SHARED_LIBS=OFF),
#     so there is no bin/ directory full of unrelated tools
#   - non-Linux backends (Metal/Vulkan/SYCL/OpenCL) and RPC are disabled
#   - CUDA is compiled for the requested architecture (default sm_70, Tesla V100)
#
# Requirements: Linux x86_64, cmake >= 3.18, a C++17 compiler, and optionally
# the CUDA toolkit (nvcc). The CUDA runtime (libcudart/libcublas) remains a
# dynamic dependency and is provided by the NVIDIA driver/toolkit.
#
# Usage:
#   bash scripts/build-llama-minimal.sh
#   CUDA=0 bash scripts/build-llama-minimal.sh          # CPU-only build
#   CUDA_ARCH=70 CUDA_ROOT=/usr/local/cuda-12.9 bash scripts/build-llama-minimal.sh
#   JOBS=12 NATIVE=1 bash scripts/build-llama-minimal.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${LLAMA_SRC:-$ROOT/llama.cpp-master}"
BUILD="${LLAMA_BUILD_DIR:-$ROOT/dist/llama-build}"
OUT="${OUT:-$ROOT/dist/maio-llama}"
CUDA="${CUDA:-1}"
CUDA_ARCH="${CUDA_ARCH:-70}"
NATIVE="${NATIVE:-1}"
JOBS="${JOBS:-$(nproc)}"
CUDA_ROOT="${CUDA_ROOT:-}"

if [ "$(uname -s)" != "Linux" ] || [ "$(uname -m)" != "x86_64" ]; then
  echo "error: this build script targets Linux x86_64 only" >&2
  exit 1
fi
if [ ! -f "$SRC/CMakeLists.txt" ]; then
  echo "error: llama.cpp source not found at $SRC" >&2
  echo "set LLAMA_SRC to the llama.cpp checkout" >&2
  exit 1
fi
CMAKE="$(command -v cmake || true)"
if [ -z "$CMAKE" ]; then
  echo "error: cmake >= 3.18 is required" >&2
  echo "on Ubuntu: sudo apt install cmake build-essential" >&2
  exit 1
fi
if [ "$CUDA" = "1" ] && [ -z "$CUDA_ROOT" ]; then
  for d in /usr/local/cuda /usr/local/cuda-*; do
    if [ -x "$d/bin/nvcc" ]; then CUDA_ROOT="$d"; break; fi
  done
fi
if [ "$CUDA" = "1" ] && { [ -z "$CUDA_ROOT" ] || [ ! -x "$CUDA_ROOT/bin/nvcc" ]; }; then
  echo "error: nvcc not found; set CUDA_ROOT or use CUDA=0 for a CPU build" >&2
  exit 1
fi
if [ "$CUDA" = "1" ]; then
  CXX_BIN="${CXX:-$(command -v g++ || true)}"
  CXX_MAJOR=""
  if [ -n "$CXX_BIN" ]; then
    CXX_MAJOR="$("$CXX_BIN" -dumpversion 2>/dev/null | cut -d. -f1)"
  fi
  NVCC_RELEASE="$("$CUDA_ROOT/bin/nvcc" --version 2>/dev/null | sed -n 's/.*release \([0-9][0-9]*\)\..*/\1/p' | head -n1)"
  if [ -n "$CXX_MAJOR" ] && [ "$CXX_MAJOR" -gt 14 ] && [ "${ALLOW_UNSUPPORTED_COMPILER:-0}" != "1" ] && [ "${NVCC_RELEASE:-12}" -lt 13 ]; then
    cat >&2 <<EOF
error: CUDA ${NVCC_RELEASE:-12}.x supports GCC <= 14, but $CXX_BIN is GCC $CXX_MAJOR.
choose one:
  sudo apt install g++-14 && CXX=g++-14 CC=gcc-14 make llama
  make llama CUDA=0                       # CPU-only minimal build
  make llama-bundle LLAMA_BUILD=...        # reuse an existing CUDA build
  ALLOW_UNSUPPORTED_COMPILER=1 make llama  # try anyway (may fail)
EOF
    exit 1
  fi
fi

ARGS=(
  -S "$SRC" -B "$BUILD"
  -DCMAKE_BUILD_TYPE=Release
  -DCMAKE_INSTALL_PREFIX="$OUT"
  -DBUILD_SHARED_LIBS=OFF
  -DLLAMA_BUILD_TESTS=OFF
  -DLLAMA_BUILD_EXAMPLES=OFF
  -DLLAMA_BUILD_TOOLS=ON
  -DLLAMA_BUILD_SERVER=ON
  -DLLAMA_BUILD_APP=OFF
  -DLLAMA_BUILD_UI=OFF
  -DLLAMA_BUILD_MTMD=OFF
  -DLLAMA_CURL=OFF
  -DLLAMA_OPENSSL=OFF
  -DGGML_VULKAN=OFF
  -DGGML_SYCL=OFF
  -DGGML_OPENCL=OFF
  -DGGML_RPC=OFF
  -DGGML_CUDA_NCCL=OFF
  -DGGML_CUDA_FA=ON
  -DGGML_CPU_ALL_VARIANTS=OFF
  -DGGML_NATIVE="$([ "$NATIVE" = "1" ] && echo ON || echo OFF)"
  -DCMAKE_CUDA_ARCHITECTURES="$CUDA_ARCH"
  -DCMAKE_CUDA_FLAGS="${CUDA_FLAGS:--allow-unsupported-compiler}"
)
if [ "$CUDA" = "1" ]; then
  ARGS+=(
    -DGGML_CUDA=ON
    -DCUDAToolkit_ROOT="$CUDA_ROOT"
    -DCMAKE_CUDA_COMPILER="$CUDA_ROOT/bin/nvcc"
  )
  export PATH="$CUDA_ROOT/bin:$PATH"
  export LD_LIBRARY_PATH="$CUDA_ROOT/lib64:${LD_LIBRARY_PATH:-}"
else
  ARGS+=(-DGGML_CUDA=OFF)
fi

echo "configuring minimal llama-server"
echo "  source    : $SRC"
echo "  build dir : $BUILD"
echo "  output    : $OUT"
echo "  cuda      : $CUDA${CUDA_ROOT:+ ($CUDA_ROOT)} arch $CUDA_ARCH"
echo "  native    : $NATIVE"
"$CMAKE" "${ARGS[@]}"

echo "building target llama-server (-j$JOBS)"
"$CMAKE" --build "$BUILD" --target llama-server -j "$JOBS"

rm -rf "$OUT"
mkdir -p "$OUT/bin"
cp "$BUILD/bin/llama-server" "$OUT/bin/llama-server"
chmod 0755 "$OUT/bin/llama-server"
if command -v strip >/dev/null 2>&1; then
  strip --strip-unneeded "$OUT/bin/llama-server" || true
fi

LIB_PATH="$OUT/bin:${LD_LIBRARY_PATH:-}"
# Copy any non-system shared libraries that static linking did not absorb
# (CUDA is expected to be supplied by the driver/toolkit, not copied).
ldd "$OUT/bin/llama-server" 2>/dev/null | awk '/=> \// {print $3}' | while read -r lib; do
  case "$lib" in
    /lib/*|/usr/lib/*|/lib64/*|/usr/lib64/*) ;;
    *) echo "note: dynamic dependency: $lib" ;;
  esac
done

if ! LD_LIBRARY_PATH="$LIB_PATH" "$OUT/bin/llama-server" --version >"$OUT/version.txt" 2>&1; then
  echo "error: the built server does not start:" >&2
  cat "$OUT/version.txt" >&2
  exit 1
fi

if [ -f "$SRC/LICENSE" ]; then cp "$SRC/LICENSE" "$OUT/LICENSE.llama.cpp"; fi
cat > "$OUT/README.txt" <<EOF
maio minimal llama-server (Linux x86_64)
========================================
built at     $(date -u +%Y-%m-%dT%H:%M:%SZ)
cuda         $CUDA (arch $CUDA_ARCH)
native       $NATIVE
source       $SRC

The llama/ggml libraries are statically linked into bin/llama-server.
CUDA runtime libs (libcudart, libcublas, libcublasLt) are dynamic and must be
available through the NVIDIA driver/toolkit.

Set in /etc/maio/maio.conf:
  llama_bin = "$OUT/bin/llama-server"
  llama_lib = "${CUDA_ROOT:+$CUDA_ROOT/lib64}"
EOF
cat > "$OUT/manifest.json" <<EOF
{
  "platform": "linux",
  "arch": "x86_64",
  "server_sha256": "$(sha256sum "$OUT/bin/llama-server" | awk '{print $1}')",
  "built_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "cuda": "$CUDA",
  "cuda_arch": "$CUDA_ARCH",
  "native": "$NATIVE",
  "source": "$SRC"
}
EOF

echo
echo "built minimal llama-server: $OUT/bin/llama-server"
echo "  size   : $(du -sh "$OUT" | awk '{print $1}')"
echo "  version: $(head -n 1 "$OUT/version.txt")"
echo "  set llama_bin = \"$OUT/bin/llama-server\" in maio.conf"
