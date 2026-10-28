#!/usr/bin/env bash
# Build the Linux product: maio + the RPC-enabled llama.cpp runtime.
#
#   VERSION=4.1.0 ./linux/build.sh
#   RPC_BIN=/path/ggml-rpc-server LLAMA_BIN=/path/llama-server ./linux/build.sh
#
# Cross-compiling maio itself is possible from any OS:
#   GOOS=linux GOARCH=arm64 ./linux/build.sh
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-4.1.0}"
ARCH="${ARCH:-$(go env GOARCH)}"
OUT="${OUT:-dist/linux-$ARCH}"
RPC_BIN="${RPC_BIN:-}"
LLAMA_BIN="${LLAMA_BIN:-}"

rm -rf "$OUT"
mkdir -p "$OUT/bin"

echo "building maio $VERSION for linux/$ARCH"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath \
  -ldflags "-s -w -X github.com/ShieldDog-ACE/maio-cluster/v4/core/version.Version=$VERSION" \
  -o "$OUT/maio" ./linux/cmd/maio

if [[ -n "$RPC_BIN" && -f "$RPC_BIN" ]]; then
  cp "$RPC_BIN" "$OUT/bin/ggml-rpc-server"
  chmod 0755 "$OUT/bin/ggml-rpc-server"
  echo "bundled RPC server: $RPC_BIN"
else
  echo "warning: no RPC_BIN; distributed shards will need ggml-rpc-server installed separately" >&2
fi
if [[ -n "$LLAMA_BIN" && -f "$LLAMA_BIN" ]]; then
  cp "$LLAMA_BIN" "$OUT/bin/llama-server"
  chmod 0755 "$OUT/bin/llama-server"
  echo "bundled llama-server: $LLAMA_BIN"
else
  echo "warning: no LLAMA_BIN; local and frontend inference need llama-server" >&2
fi

cp linux/packaging/maio.service "$OUT/maio.service"
cp linux/packaging/install.sh "$OUT/install.sh"
cp README.md "$OUT/README.md" 2>/dev/null || true
chmod +x "$OUT/install.sh"
(cd "$OUT" && tar -czf "../maio-linux-$ARCH.tar.gz" .)
echo "built: $OUT"
echo "built: dist/maio-linux-$ARCH.tar.gz"
