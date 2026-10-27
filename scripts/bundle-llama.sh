#!/usr/bin/env bash
# Build a minimal, stripped llama-server runtime for Linux x86-64 from an
# existing llama.cpp build directory. Only the server binary and the shared
# libraries it actually loads are copied; every CLI, benchmark, test and
# example is left behind.
#
# Usage:
#   LLAMA_BUILD=/path/to/llama.cpp/build bash scripts/bundle-llama.sh
#   OUT=/opt/maio/llama bash scripts/bundle-llama.sh
#
# CUDA runtime libraries (libcudart, libcublas, libcublasLt) are NOT bundled:
# they are large and already provided by the NVIDIA driver/toolkit. Point
# llama_lib at them, or put them on the loader path.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_BUILD="${LLAMA_BUILD:-$ROOT/llama.cpp-master/build}"
OUT="${OUT:-$ROOT/dist/maio-llama}"
BIN="$SRC_BUILD/bin/llama-server"

if [ "$(uname -s)" != "Linux" ] || [ "$(uname -m)" != "x86_64" ]; then
  echo "error: this bundle targets Linux x86_64 only" >&2
  exit 1
fi
if [ ! -x "$BIN" ]; then
  echo "error: llama-server not found at $BIN" >&2
  echo "build it first or set LLAMA_BUILD to an existing build directory" >&2
  exit 1
fi

# The SONAMEs maio's engine needs. libllama-server-impl.so is the unversioned
# server implementation; the rest are the llama/ggml stack.
LIBS=(
  libllama-server-impl.so
  libllama-common.so.0
  libmtmd.so.0
  libllama.so.0
  libggml.so.0
  libggml-base.so.0
  libggml-cpu.so.0
  libggml-cuda.so.0
)

rm -rf "$OUT"
mkdir -p "$OUT/bin" "$OUT/lib"

cp -L "$BIN" "$OUT/bin/llama-server"
chmod 0755 "$OUT/bin/llama-server"

missing=0
for name in "${LIBS[@]}"; do
  src="$SRC_BUILD/bin/$name"
  if [ -e "$src" ]; then
    cp -L "$src" "$OUT/lib/$name"
    chmod 0755 "$OUT/lib/$name"
  else
    echo "warning: $name is not present in $SRC_BUILD/bin" >&2
    missing=1
  fi
done

if [ "$missing" -eq 1 ]; then
  echo "error: the build is incomplete; refusing to produce a broken runtime" >&2
  exit 1
fi

strip_tool="$(command -v strip || true)"
if [ -n "$strip_tool" ]; then
  "$strip_tool" --strip-unneeded "$OUT/bin/llama-server" || true
  for f in "$OUT"/lib/*.so*; do
    "$strip_tool" --strip-unneeded "$f" || true
  done
fi

# Verify the copied runtime starts without referring to the source tree.
LIB_PATH="$OUT/lib:${LD_LIBRARY_PATH:-}"
if [ -n "${LLAMA_LIB:-}" ]; then
  LIB_PATH="$LIB_PATH:$LLAMA_LIB"
fi
for d in /usr/local/cuda/lib64 /usr/local/cuda*/lib64; do
  [ -d "$d" ] && LIB_PATH="$LIB_PATH:$d"
done
export LD_LIBRARY_PATH="$LIB_PATH"
if ! "$OUT/bin/llama-server" --version >"$OUT/version.txt" 2>&1; then
  echo "error: the bundled llama-server does not start:" >&2
  cat "$OUT/version.txt" >&2
  exit 1
fi

# Keep the upstream license next to the binaries.
if [ -f "$ROOT/llama.cpp-master/LICENSE" ]; then
  cp "$ROOT/llama.cpp-master/LICENSE" "$OUT/LICENSE.llama.cpp"
fi

CUDA_NOTE="${LLAMA_LIB:-}"
cat > "$OUT/README.txt" <<EOF
maio minimal llama.cpp runtime (Linux x86-64)
=============================================

bin/llama-server   stripped server binary
lib/               only the shared libraries the server loads
version.txt        observed --version output
built at           $(date -u +%Y-%m-%dT%H:%M:%SZ)
source build       $SRC_BUILD

Configure maio:

  llama_bin = "$OUT/bin/llama-server"
  llama_lib = "$OUT/lib${CUDA_NOTE:+:$CUDA_NOTE}"

CUDA runtime libraries (libcudart, libcublas, libcublasLt) are provided by
the NVIDIA driver/toolkit. If maio cannot load them, add their directory to
llama_lib in /etc/maio/maio.conf.

This runtime is Linux x86-64 only and intentionally contains no llama-cli,
llama-bench, quantize, tests, examples or Web UI.
EOF

cat > "$OUT/manifest.json" <<EOF
{
  "platform": "linux",
  "arch": "x86_64",
  "server_sha256": "$(sha256sum "$OUT/bin/llama-server" | awk '{print $1}')",
  "built_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source_build": "$SRC_BUILD"
}
EOF

echo "bundled minimal llama-server: $OUT"
echo "  binary : $OUT/bin/llama-server"
echo "  libs   : $(find "$OUT/lib" -maxdepth 1 -type f | wc -l) files"
echo "  size   : $(du -sh "$OUT" | awk '{print $1}')"
echo "  version: $(head -n 1 "$OUT/version.txt")"
echo
echo "set in maio.conf:"
echo "  llama_bin = \"$OUT/bin/llama-server\""
echo "  llama_lib = \"$OUT/lib${CUDA_NOTE:+:$CUDA_NOTE}\""
