#!/usr/bin/env bash
# Install maio-cluster from a local apt repository so that the documented
# "sudo apt install maio-cluster" command works on this machine.
#
# The workspace path may contain spaces; the repository is therefore copied to
# the space-free /var/lib/maio/apt before it is registered with apt.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: sudo bash deploy/apt-install.sh" >&2
  exit 1
fi

VERSION="${VERSION:-2.0.0}"
ARCH="${ARCH:-$(dpkg --print-architecture 2>/dev/null || echo amd64)}"
DEB="$ROOT/dist/maio-cluster_${VERSION}_${ARCH}.deb"

make build VERSION="$VERSION"
VERSION="$VERSION" ARCH="$ARCH" bash deploy/build-deb.sh
VERSION="$VERSION" ARCH="$ARCH" bash deploy/apt-repo.sh

install -d -m 0755 /var/lib/maio/apt
rm -rf /var/lib/maio/apt/*
cp -a "$ROOT/dist/apt/." /var/lib/maio/apt/
echo "deb [trusted=yes] file:///var/lib/maio/apt ./" > /etc/apt/sources.list.d/maio-cluster.list

apt-get update -o Dir::Etc::sourcelist="sources.list.d/maio-cluster.list" \
  -o Dir::Etc::sourceparts="-" -o APT::Get::List-Cleanup="0" || true
apt-get install -y maio-cluster

echo
echo "maio-cluster installed. Future installs can use: sudo apt install maio-cluster"
echo "check: systemctl status maio && maio version"
