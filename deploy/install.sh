#!/usr/bin/env bash
# maio local installer: installs bin/maio to /usr/local/bin and writes a default /etc/maio/maio.conf.
# Usage: sudo bash deploy/install.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/bin/maio"
DEST="/usr/local/bin/maio"
CONF_DIR="/etc/maio"
CONF="$CONF_DIR/maio.conf"
MODELS_ROOT="/var/lib/maio/models"

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: sudo bash deploy/install.sh" >&2
  exit 1
fi
if [ ! -f "$SRC" ]; then
  echo "missing $SRC; run make build first" >&2
  exit 1
fi

install -m 0755 "$SRC" "$DEST"
mkdir -p "$CONF_DIR" "$MODELS_ROOT" "/var/lib/maio/run" "/var/lib/maio/log"

if [ ! -f "$CONF" ]; then
  cat > "$CONF" <<EOF
# maio global config (not maio.toml; one per machine)
boot        = 0
models_root = "$MODELS_ROOT"
state_dir   = "/var/lib/maio"
host        = "127.0.0.1"
port        = 11434
mirror      = "https://www.modelscope.cn"
llama_bin   = ""
llama_lib   = ""
ctx_size    = 0
EOF
  echo "default config written: $CONF"
else
  echo "keeping existing config: $CONF"
fi

echo "installed: $DEST"
echo "next: set llama_bin in $CONF to your llama-server, then run maio list"
