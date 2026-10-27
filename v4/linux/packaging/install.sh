#!/usr/bin/env bash
# Source installer for the Linux product.
#
#   sudo bash install.sh              # coordinator + agent
#   sudo bash install.sh --worker     # only the agent
#   sudo bash install.sh --no-start   # install but do not enable the service
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
PREFIX="${PREFIX:-/usr/local}"
LIB="$PREFIX/lib/maio"
CONF=/etc/maio/cluster.toml
WORKER=0
START=1
for a in "$@"; do
  case "$a" in
    --worker) WORKER=1 ;;
    --no-start) START=0 ;;
    --help|-h)
      echo "usage: sudo bash install.sh [--worker] [--no-start]"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done
if [[ $EUID -ne 0 ]]; then
  echo "run with sudo: sudo bash install.sh" >&2
  exit 5
fi

getent group maio >/dev/null 2>&1 || groupadd --system maio
getent passwd maio >/dev/null 2>&1 || useradd --system --gid maio --home /var/lib/maio --shell /usr/sbin/nologin maio
install -d -m 0755 "$LIB/bin" "$PREFIX/bin" /etc/maio /var/lib/maio/models /var/log/maio
install -m 0755 "$HERE/maio" "$LIB/maio"
ln -sf "$LIB/maio" "$PREFIX/bin/maio"
if [[ -f "$HERE/bin/ggml-rpc-server" ]]; then
  install -m 0755 "$HERE/bin/ggml-rpc-server" "$LIB/bin/ggml-rpc-server"
fi
if [[ -f "$HERE/bin/llama-server" ]]; then
  install -m 0755 "$HERE/bin/llama-server" "$LIB/bin/llama-server"
fi
chown -R maio:maio /var/lib/maio /var/log/maio

if [[ ! -f "$CONF" ]]; then
  "$PREFIX/bin/maio" --system init --force >/dev/null
fi
# Let the service user read the token but not the rest of the world.
chown root:maio "$CONF"
chmod 0640 "$CONF"

if [[ $WORKER -eq 1 ]]; then
  "$PREFIX/bin/maio" --system service install --worker
else
  "$PREFIX/bin/maio" --system service install
fi
if [[ $START -eq 1 ]]; then
  systemctl enable --now maio
fi
echo
echo "maio installed."
echo "  config:  $CONF"
echo "  join:    sudo maio --system cluster join-code"
echo "  status:  sudo maio --system cluster nodes"
echo "  logs:    journalctl -u maio -f"
