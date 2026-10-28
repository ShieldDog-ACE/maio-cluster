#!/usr/bin/env bash
# Build macOS tarballs from Linux/macOS without lipo/pkgbuild.
# Produces one tarball per architecture; on macOS build.sh makes a universal
# binary and packaging/build-pkg.sh makes a signed .pkg.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-4.1.1}"
RPC_BIN="${RPC_BIN:-}"
LLAMA_BIN="${LLAMA_BIN:-}"
for ARCH in arm64 amd64; do
  OUT="dist/mac-$ARCH"
  rm -rf "$OUT"; mkdir -p "$OUT/bin"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$ARCH" go build -trimpath \
    -ldflags "-s -w -X github.com/ShieldDog-ACE/maio-cluster/v4/core/version.Version=$VERSION" \
    -o "$OUT/maio" ./mac/cmd/maio
  cp mac/install.sh mac/README.md "$OUT/"
  [[ -f README.md ]] && cp README.md "$OUT/README.root.md"
  chmod +x "$OUT/install.sh"
  if [[ -n "$RPC_BIN" && -f "$RPC_BIN" ]]; then cp "$RPC_BIN" "$OUT/bin/ggml-rpc-server"; fi
  if [[ -n "$LLAMA_BIN" && -f "$LLAMA_BIN" ]]; then cp "$LLAMA_BIN" "$OUT/bin/llama-server"; fi
  (cd "$OUT" && tar -czf "../maio-mac-$ARCH.tar.gz" .)
  echo "built: dist/maio-mac-$ARCH.tar.gz"
done
