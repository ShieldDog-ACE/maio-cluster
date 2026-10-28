#!/usr/bin/env bash
# Build a macOS .dmg containing the .pkg and the quick-view APK.
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION="${VERSION:-4.1.0}"
PKG="dist/maio-cluster-$VERSION.pkg"
[[ -f "$PKG" ]] || ./mac/packaging/build-pkg.sh

STAGE="dist/dmg-stage"
rm -rf "$STAGE"; mkdir -p "$STAGE"
cp "$PKG" "$STAGE/"
[[ -f android/dist/maio-viewer.apk ]] && cp android/dist/maio-viewer.apk "$STAGE/"
cp mac/README.md "$STAGE/README.md"
ln -s /Applications "$STAGE/Applications"
hdiutil create -volname "maio $VERSION" -srcfolder "$STAGE" -ov -format UDZO "dist/maio-cluster-$VERSION.dmg"
echo "built: dist/maio-cluster-$VERSION.dmg"
