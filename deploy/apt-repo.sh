#!/usr/bin/env bash
# Build a local unsigned apt repository from the maio-cluster .deb.
# The generated repository is meant for a trusted local machine only.
# Usage: VERSION=2.0.0 ARCH=amd64 bash deploy/apt-repo.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-2.0.0}"
ARCH="${ARCH:-$(dpkg --print-architecture 2>/dev/null || echo amd64)}"
DEB="$ROOT/dist/maio-cluster_${VERSION}_${ARCH}.deb"
REPO="$ROOT/dist/apt"

[ -f "$DEB" ] || VERSION="$VERSION" ARCH="$ARCH" bash deploy/build-deb.sh

rm -rf "$REPO"
mkdir -p "$REPO/pool/main/m/maio-cluster"
cp "$DEB" "$REPO/pool/main/m/maio-cluster/"
REL="pool/main/m/maio-cluster/$(basename "$DEB")"

{
  dpkg-deb -f "$DEB" Package Version Architecture Maintainer Installed-Size Section Priority Homepage
  echo "Filename: $REL"
  echo "Size: $(stat -c%s "$DEB")"
  echo "SHA256: $(sha256sum "$DEB" | awk '{print $1}')"
  echo "Description: $(dpkg-deb -f "$DEB" Description | head -n 1)"
} > "$REPO/Packages"
gzip -9c "$REPO/Packages" > "$REPO/Packages.gz"

PKG_SHA="$(sha256sum "$REPO/Packages.gz" | awk '{print $1}')"
PKG_SIZE="$(stat -c%s "$REPO/Packages.gz")"
{
  echo "Origin: maio"
  echo "Label: maio"
  echo "Suite: stable"
  echo "Codename: stable"
  echo "Architectures: $ARCH"
  echo "Components: main"
  echo "Description: maio local apt repository"
  echo "Date: $(date -Ru)"
  echo "SHA256:"
  echo " $PKG_SHA $PKG_SIZE Packages.gz"
} > "$REPO/Release"

cat > "$REPO/maio-cluster.list" <<EOF
deb [trusted=yes] file://$REPO ./
EOF

echo "local apt repository: $REPO"
echo
echo "one-off install without a repository:"
echo "  sudo apt install \"$DEB\""
echo
echo "make 'sudo apt install maio-cluster' work on this machine (root):"
echo "  sudo bash deploy/apt-install.sh"
echo
echo "or add the repository manually:"
echo "  sudo cp \"$REPO/maio-cluster.list\" /etc/apt/sources.list.d/maio-cluster.list"
echo "  sudo apt update"
echo "  sudo apt install maio-cluster"
