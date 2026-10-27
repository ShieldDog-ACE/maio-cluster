#!/usr/bin/env bash
# Build a Debian package for maio-cluster.
# Usage: VERSION=2.0.0 ARCH=amd64 bash deploy/build-deb.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-2.0.0}"
ARCH="${ARCH:-$(dpkg --print-architecture 2>/dev/null || echo amd64)}"
OUT="$ROOT/dist"
DEB="$OUT/maio-cluster_${VERSION}_${ARCH}.deb"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if [ ! -x "$ROOT/bin/maio" ]; then
  echo "building bin/maio first" >&2
  make build VERSION="$VERSION"
fi

install -D -m 0755 "$ROOT/bin/maio" "$STAGE/usr/bin/maio"
install -D -m 0644 "$ROOT/deploy/maio.service" "$STAGE/usr/lib/systemd/system/maio.service"
install -D -m 0644 "$ROOT/deploy/maio.conf" "$STAGE/etc/maio/maio.conf"
install -D -m 0755 "$ROOT/deploy/deb/postinst" "$STAGE/DEBIAN/postinst"
install -D -m 0755 "$ROOT/deploy/deb/prerm" "$STAGE/DEBIAN/prerm"
install -D -m 0755 "$ROOT/deploy/deb/postrm" "$STAGE/DEBIAN/postrm"
install -D -m 0644 "$ROOT/LICENSE" "$STAGE/usr/share/doc/maio-cluster/copyright"
install -D -m 0644 "$ROOT/README.md" "$STAGE/usr/share/doc/maio-cluster/README.md"

chmod 0755 "$STAGE"
SIZE_KB="$(du -sk "$STAGE/usr" | awk '{print $1}')"
sed -e "s/@VERSION@/$VERSION/g" \
    -e "s/@ARCH@/$ARCH/g" \
    -e "s/@INSTALLED_SIZE@/$SIZE_KB/g" \
    "$ROOT/deploy/deb/control.in" > "$STAGE/DEBIAN/control"

mkdir -p "$OUT"
rm -f "$DEB"
dpkg-deb --build --root-owner-group "$STAGE" "$DEB" >/dev/null
echo "built: $DEB"
dpkg-deb --info "$DEB" | sed 's/^/  /'
