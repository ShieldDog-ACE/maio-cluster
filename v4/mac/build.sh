#!/usr/bin/env bash
# Build the macOS product.
#
# On macOS this produces a universal binary (arm64 + x86_64), a .pkg and a .dmg.
# Run mac/packaging/build-pkg.sh after this script, or use build-cross.sh from
# Linux to produce per-architecture tarballs.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-4.0.0}"
OUT="${OUT:-dist/mac-universal}"
RPC_BIN="${RPC_BIN:-}"
LLAMA_BIN="${LLAMA_BIN:-}"

rm -rf "$OUT"; mkdir -p "$OUT/bin"
for arch in arm64 amd64; do
  echo "building maio $VERSION for darwin/$arch"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X github.com/ShieldDog-ACE/maio-cluster/v4/core/version.Version=$VERSION" \
    -o "$OUT/maio-$arch" ./mac/cmd/maio
done
if command -v lipo >/dev/null 2>&1; then
  lipo -create -output "$OUT/maio" "$OUT/maio-arm64" "$OUT/maio-amd64"
  rm -f "$OUT/maio-arm64" "$OUT/maio-amd64"
else
  cp "$OUT/maio-arm64" "$OUT/maio"
  echo "warning: lipo not available; shipping the arm64 binary as maio" >&2
fi
chmod 0755 "$OUT/maio"

if [[ -n "$RPC_BIN" && -f "$RPC_BIN" ]]; then
  cp "$RPC_BIN" "$OUT/bin/ggml-rpc-server"
  chmod 0755 "$OUT/bin/ggml-rpc-server"
fi
if [[ -n "$LLAMA_BIN" && -f "$LLAMA_BIN" ]]; then
  cp "$LLAMA_BIN" "$OUT/bin/llama-server"
  chmod 0755 "$OUT/bin/llama-server"
fi
cp mac/install.sh "$OUT/install.sh"
cp mac/README.md "$OUT/README.md"
chmod +x "$OUT/install.sh"
echo "built: $OUT"
