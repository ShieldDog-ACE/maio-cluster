#!/usr/bin/env bash
# Build a flat apt repository for the Linux package.
#
#   VERSION=4.1.1 ARCH=amd64 ./linux/packaging/build-apt-repo.sh
#
# Output: dist/apt/{Packages,Release,maio-cluster_<version>_<arch>.deb,setup.sh}
# The repository is unsigned; setup.sh uses apt's [trusted=yes] for LAN/CI use.
set -euo pipefail
cd "$(dirname "$0")/../.."

VERSION="${VERSION:-4.1.1}"
ARCH="${ARCH:-amd64}"
OUT="dist/apt"
DEB="dist/maio-cluster_${VERSION}_${ARCH}.deb"

VERSION="$VERSION" ARCH="$ARCH" ./linux/packaging/build-deb.sh >/dev/null
[[ -f "$DEB" ]] || { echo "error: $DEB was not produced" >&2; exit 1; }

rm -rf "$OUT"
mkdir -p "$OUT"
cp "$DEB" "$OUT/"

SIZE=$(stat -c %s "$DEB")
SHA=$(sha256sum "$DEB" | awk '{print $1}')
INSTALLED=$(dpkg-deb -f "$DEB" Installed-Size 2>/dev/null || true)
[ -n "$INSTALLED" ] || INSTALLED=0

cat > "$OUT/Packages" <<EOF
Package: maio-cluster
Version: $VERSION
Architecture: $ARCH
Maintainer: ShieldDog-ACE <https://github.com/ShieldDog-ACE/maio-cluster>
Installed-Size: $INSTALLED
Filename: ./$(basename "$DEB")
Size: $SIZE
SHA256: $SHA
Section: net
Priority: optional
Description: maio distributed llama.cpp cluster
 Coordinator, agent, LAN discovery, package manager and optional web console.
EOF

PACKAGES_SHA=$(sha256sum "$OUT/Packages" | awk '{print $1}')
PACKAGES_SIZE=$(stat -c %s "$OUT/Packages")
DATE=$(date -Ru)
cat > "$OUT/Release" <<EOF
Origin: maio
Label: maio
Suite: stable
Codename: stable
Architectures: $ARCH
Components: main
Description: maio apt repository
Date: $DATE
SHA256:
 $PACKAGES_SHA $PACKAGES_SIZE Packages
EOF

cp linux/packaging/apt-setup.sh "$OUT/setup.sh"
chmod +x "$OUT/setup.sh"
cp linux/packaging/apt-README.md "$OUT/README.md"

echo "apt repo: $OUT"
echo "  Packages  $PACKAGES_SHA"
echo "  deb       $SHA"
