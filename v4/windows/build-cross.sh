#!/usr/bin/env bash
# Build the Windows zip from Linux/macOS (Go cross-compilation).
# llama.cpp RPC binaries must be supplied for a fully distributed package.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-4.1.0}"
ARCH="${ARCH:-amd64}"
OUT="dist/windows-$ARCH"
RPC_BIN="${RPC_BIN:-}"
LLAMA_BIN="${LLAMA_BIN:-}"
rm -rf "$OUT"; mkdir -p "$OUT/bin"
CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" go build -trimpath \
  -ldflags "-s -w -X github.com/ShieldDog-ACE/maio-cluster/v4/core/version.Version=$VERSION" \
  -o "$OUT/maio.exe" ./windows/cmd/maio
[[ -n "$RPC_BIN" && -f "$RPC_BIN" ]] && cp "$RPC_BIN" "$OUT/bin/ggml-rpc-server.exe"
[[ -n "$LLAMA_BIN" && -f "$LLAMA_BIN" ]] && cp "$LLAMA_BIN" "$OUT/bin/llama-server.exe"
cp windows/install.ps1 windows/README.md "$OUT/"
[[ -f README.md ]] && cp README.md "$OUT/README.root.md"
(cd "$OUT" && zip -qr "../maio-windows-$ARCH.zip" .)
echo "built: dist/maio-windows-$ARCH.zip"
