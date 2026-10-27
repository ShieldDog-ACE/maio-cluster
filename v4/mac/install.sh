#!/usr/bin/env bash
# Source installer for macOS.
#
#   sudo bash install.sh              # system LaunchDaemon
#   bash install.sh --user            # per-user LaunchAgent
#   sudo bash install.sh --worker     # agent only
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
USER_MODE=0
WORKER=0
NO_START=0
for a in "$@"; do
  case "$a" in
    --user) USER_MODE=1 ;;
    --system) USER_MODE=0 ;;
    --worker) WORKER=1 ;;
    --no-start) NO_START=1 ;;
    --help|-h) echo "usage: bash install.sh [--user] [--worker] [--no-start]"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done

if [[ $USER_MODE -eq 0 && $EUID -ne 0 ]]; then
  echo "system install needs root: sudo bash install.sh (or use --user)" >&2
  exit 5
fi
PREFIX="${PREFIX:-/usr/local}"
LIB="$PREFIX/lib/maio"
if [[ $USER_MODE -eq 1 ]]; then
  BIN="$HOME/.local/bin"
  CONF="$HOME/Library/Application Support/maio/cluster.toml"
  SCOPE=(--config "$CONF")
else
  BIN="$PREFIX/bin"
  CONF="/Library/Application Support/maio/cluster.toml"
  SCOPE=(--system)
fi

install -d -m 0755 "$LIB/bin" "$BIN"
install -m 0755 "$HERE/maio" "$LIB/maio"
ln -sf "$LIB/maio" "$BIN/maio"
[[ -f "$HERE/bin/ggml-rpc-server" ]] && install -m 0755 "$HERE/bin/ggml-rpc-server" "$LIB/bin/ggml-rpc-server"
[[ -f "$HERE/bin/llama-server" ]] && install -m 0755 "$HERE/bin/llama-server" "$LIB/bin/llama-server"

if [[ ! -f "$CONF" ]]; then
  "$BIN/maio" "${SCOPE[@]}" init --force >/dev/null
fi

if [[ $WORKER -eq 1 ]]; then
  "$BIN/maio" "${SCOPE[@]}" service install --worker
else
  "$BIN/maio" "${SCOPE[@]}" service install
fi
if [[ $NO_START -eq 1 ]]; then
  if [[ $USER_MODE -eq 1 ]]; then
    launchctl bootout "gui/$(id -u)/dev.maio.cluster" 2>/dev/null || true
  else
    launchctl bootout system/dev.maio.cluster 2>/dev/null || true
  fi
fi

echo
echo "maio installed."
echo "  config:  $CONF"
echo "  join:    $BIN/maio ${SCOPE[*]} cluster join-code"
echo "  status:  $BIN/maio ${SCOPE[*]} cluster nodes"
