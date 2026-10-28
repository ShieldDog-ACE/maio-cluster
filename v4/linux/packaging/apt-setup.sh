#!/bin/sh
# Add the maio apt repository and install maio-cluster.
#
#   sudo sh setup.sh
#
# The repository is unsigned, so [trusted=yes] is used. For a production
# deployment, mirror the repository internally and sign it with your own key.
set -eu

REPO="${MAIO_APT_URL:-https://cdn.jsdelivr.net/gh/ShieldDog-ACE/maio-apt@apt-v4.1.1}"
LIST=/etc/apt/sources.list.d/maio.list

if [ "$(id -u)" -ne 0 ]; then
  echo "run with sudo: sudo sh setup.sh" >&2
  exit 1
fi

echo "deb [trusted=yes] $REPO ./" > "$LIST"
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y maio-cluster

echo
echo "installed. next:"
echo "  sudo maio --system token show"
echo "  sudo maio --system web install"
echo "  sudo maio --system cluster join-code"
