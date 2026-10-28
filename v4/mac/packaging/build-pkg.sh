#!/usr/bin/env bash
# Build a macOS .pkg installer. Run on macOS after mac/build.sh.
#
#   VERSION=4.1.0 ./mac/packaging/build-pkg.sh
#   SIGN_ID="Developer ID Installer: ..." ./mac/packaging/build-pkg.sh
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION="${VERSION:-4.1.0}"
OUT="dist"
STAGE="$OUT/pkg-root"
SCRIPTS="$OUT/pkg-scripts"
PKG="$OUT/maio-cluster-$VERSION.pkg"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "error: pkgbuild/productbuild require macOS" >&2
  exit 2
fi
if [[ ! -f dist/mac-universal/maio ]]; then
  ./mac/build.sh
fi

rm -rf "$STAGE" "$SCRIPTS"
mkdir -p "$STAGE/usr/local/bin" "$STAGE/usr/local/lib/maio/bin" \
  "$STAGE/Applications" "$SCRIPTS"
install -m 0755 dist/mac-universal/maio "$STAGE/usr/local/lib/maio/maio"
ln -s ../lib/maio/maio "$STAGE/usr/local/bin/maio"
if [[ -f dist/mac-universal/bin/ggml-rpc-server ]]; then
  install -m 0755 dist/mac-universal/bin/ggml-rpc-server "$STAGE/usr/local/lib/maio/bin/"
fi
if [[ -f dist/mac-universal/bin/llama-server ]]; then
  install -m 0755 dist/mac-universal/bin/llama-server "$STAGE/usr/local/lib/maio/bin/"
fi
install -m 0644 mac/README.md "$STAGE/usr/local/lib/maio/README.md"
# Keep the package daemon payload minimal: the adapter writes the final plist
# with the correct absolute paths in postinstall.

cat > "$SCRIPTS/postinstall" <<'EOF'
#!/bin/sh
set -e
CONF="/Library/Application Support/maio/cluster.toml"
install -d -m 0755 "/Library/Application Support/maio" "/Library/Application Support/maio/models" "/Library/Logs/maio"
if [ ! -f "$CONF" ]; then
  /usr/local/bin/maio --system init --force >/dev/null 2>&1 || true
fi
/usr/local/bin/maio --system service install >/dev/null 2>&1 || true
exit 0
EOF

cat > "$SCRIPTS/preinstall" <<'EOF'
#!/bin/sh
set -e
launchctl bootout system/dev.maio.cluster >/dev/null 2>&1 || true
exit 0
EOF
chmod 0755 "$SCRIPTS/postinstall" "$SCRIPTS/preinstall"

mkdir -p "$OUT"
pkgbuild --root "$STAGE" --scripts "$SCRIPTS" \
  --identifier dev.maio.cluster --version "$VERSION" \
  --install-location / "$OUT/maio-component.pkg"
productbuild --package "$OUT/maio-component.pkg" "$PKG"
if [[ -n "${SIGN_ID:-}" ]]; then
  productsign --sign "$SIGN_ID" "$PKG" "$PKG.signed"
  mv "$PKG.signed" "$PKG"
fi
rm -f "$OUT/maio-component.pkg"
echo "built: $PKG"
