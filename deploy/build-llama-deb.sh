#!/usr/bin/env bash
# Package the minimal llama-server runtime produced by
# scripts/build-llama-minimal.sh or scripts/bundle-llama.sh.
#
# Usage: VERSION=3.0.0 ARCH=amd64 bash deploy/build-llama-deb.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-3.0.0}"
ARCH="${ARCH:-$(dpkg --print-architecture 2>/dev/null || echo amd64)}"
SRC="${LLAMA_RUNTIME:-$ROOT/dist/maio-llama}"
OUT="$ROOT/dist"
DEB="$OUT/maio-llama_${VERSION}_${ARCH}.deb"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if [ ! -x "$SRC/bin/llama-server" ]; then
  echo "error: $SRC/bin/llama-server not found" >&2
  echo "run: make llama-bundle   (or: make llama)" >&2
  exit 1
fi

install -D -m 0755 "$SRC/bin/llama-server" "$STAGE/opt/maio/llama/bin/llama-server"
if [ -d "$SRC/lib" ]; then
  install -d -m 0755 "$STAGE/opt/maio/llama/lib"
  for f in "$SRC"/lib/*; do
    [ -f "$f" ] && install -m 0755 "$f" "$STAGE/opt/maio/llama/lib/$(basename "$f")"
  done
fi
for f in README.txt manifest.json version.txt LICENSE.llama.cpp; do
  [ -f "$SRC/$f" ] && install -D -m 0644 "$SRC/$f" "$STAGE/usr/share/doc/maio-llama/$f"
done

SIZE_KB="$(du -sk "$STAGE" | awk '{print $1}')"
mkdir -p "$STAGE/DEBIAN"
cat > "$STAGE/DEBIAN/control" <<EOF
Package: maio-llama
Version: $VERSION
Section: utils
Priority: optional
Architecture: $ARCH
Maintainer: ShieldDog-ACE <shielddog-ace@users.noreply.github.com>
Installed-Size: $SIZE_KB
Homepage: https://github.com/ShieldDog-ACE/maio-cluster
Description: minimal Linux x86-64 llama.cpp server runtime for maio
 A stripped llama-server build containing only the server binary and its
 shared libraries. CUDA runtime libraries are provided by the NVIDIA
 driver/toolkit.
 .
 Set llama_bin = "/opt/maio/llama/bin/llama-server" in /etc/maio/maio.conf.
EOF
cat > "$STAGE/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if [ -x /opt/maio/llama/bin/llama-server ]; then
    echo "maio-llama installed: /opt/maio/llama/bin/llama-server"
    echo "set llama_bin in /etc/maio/maio.conf, then restart the maio service"
fi
exit 0
EOF
chmod 0755 "$STAGE/DEBIAN/postinst"
chmod 0755 "$STAGE"

mkdir -p "$OUT"
rm -f "$DEB"
dpkg-deb --build --root-owner-group "$STAGE" "$DEB" >/dev/null
echo "built: $DEB"
dpkg-deb --info "$DEB" | sed 's/^/  /'
