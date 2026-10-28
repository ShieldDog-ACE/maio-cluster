#!/usr/bin/env bash
# Build an installable Debian package for the Linux product.
#
#   VERSION=4.1.2 ARCH=amd64 \
#   RPC_BIN=/tmp/llama-rpc-build/bin/ggml-rpc-server \
#   LLAMA_BIN=/tmp/llama-rpc-build/bin/llama-server \
#   ./linux/packaging/build-deb.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

VERSION="${VERSION:-4.1.2}"
ARCH="${ARCH:-amd64}"
RPC_BIN="${RPC_BIN:-}"
LLAMA_BIN="${LLAMA_BIN:-}"
OUT="dist"
STAGE="$OUT/deb-root"
DEB="$OUT/maio-cluster_${VERSION}_${ARCH}.deb"

./linux/build.sh

rm -rf "$STAGE"
mkdir -p "$STAGE/DEBIAN" "$STAGE/usr/bin" "$STAGE/usr/lib/maio/bin" \
  "$STAGE/etc/maio" "$STAGE/lib/systemd/system" "$STAGE/usr/share/doc/maio-cluster"

install -m 0755 "dist/linux-$ARCH/maio" "$STAGE/usr/bin/maio"
if [[ -f "dist/linux-$ARCH/bin/ggml-rpc-server" ]]; then
  install -m 0755 "dist/linux-$ARCH/bin/ggml-rpc-server" "$STAGE/usr/lib/maio/bin/ggml-rpc-server"
fi
if [[ -f "dist/linux-$ARCH/bin/llama-server" ]]; then
  install -m 0755 "dist/linux-$ARCH/bin/llama-server" "$STAGE/usr/lib/maio/bin/llama-server"
fi
install -m 0644 linux/packaging/maio.service "$STAGE/lib/systemd/system/maio.service"
install -m 0644 README.md "$STAGE/usr/share/doc/maio-cluster/README.md" 2>/dev/null || true
install -m 0644 linux/README.md "$STAGE/usr/share/doc/maio-cluster/README.linux.md" 2>/dev/null || true

cat > "$STAGE/DEBIAN/control" <<EOF
Package: maio-cluster
Version: $VERSION
Section: net
Priority: optional
Architecture: $ARCH
Depends: libc6, libgomp1, libstdc++6, systemd
Maintainer: ShieldDog-ACE <https://github.com/ShieldDog-ACE/maio-cluster>
Homepage: https://github.com/ShieldDog-ACE/maio-cluster
Description: maio v4 distributed llama.cpp cluster
 Coordinator, agent, LAN/USB/hotspot discovery and llama.cpp RPC layer
 splitting. Bundles a CPU ggml-rpc-server and llama-server; install a
 CUDA/Metal build for accelerator shards.
EOF

cat > "$STAGE/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
getent group maio >/dev/null 2>&1 || groupadd --system maio || true
id maio >/dev/null 2>&1 || useradd --system --gid maio --home /var/lib/maio --shell /usr/sbin/nologin maio || true
install -d -o maio -g maio -m 0755 /var/lib/maio /var/lib/maio/models /var/log/maio
install -d -o root -g maio -m 0750 /etc/maio
if [ ! -f /etc/maio/cluster.toml ]; then
  /usr/bin/maio --system init --force >/dev/null 2>&1 || true
fi
if [ -f /etc/maio/cluster.toml ]; then
  chown root:maio /etc/maio/cluster.toml
  chmod 0640 /etc/maio/cluster.toml
fi
systemctl daemon-reload >/dev/null 2>&1 || true
systemctl enable maio >/dev/null 2>&1 || true
systemctl restart maio >/dev/null 2>&1 || true
exit 0
EOF

cat > "$STAGE/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
systemctl stop maio >/dev/null 2>&1 || true
systemctl disable maio >/dev/null 2>&1 || true
exit 0
EOF

cat > "$STAGE/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
systemctl daemon-reload >/dev/null 2>&1 || true
if [ "$1" = "purge" ]; then
  rm -rf /var/lib/maio /var/log/maio /etc/maio
  id maio >/dev/null 2>&1 && userdel maio >/dev/null 2>&1 || true
fi
exit 0
EOF

chmod 0755 "$STAGE/DEBIAN/postinst" "$STAGE/DEBIAN/prerm" "$STAGE/DEBIAN/postrm"
mkdir -p "$OUT"
dpkg-deb --build --root-owner-group "$STAGE" "$DEB"
echo "built: $DEB"
dpkg-deb --info "$DEB" | head -12
