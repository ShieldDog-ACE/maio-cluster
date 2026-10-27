#!/usr/bin/env bash
# Build the maio quick-view APK without Gradle.
#
# Requirements (all overridable):
#   JAVA_HOME   JDK 17+
#   ANDROID_SDK Android SDK with platforms;android-34 and build-tools;34.0.0
#
# The script is deliberately dependency-free so the APK can be rebuilt on a
# machine that only has a JDK and the Android command-line tools.
set -euo pipefail
cd "$(dirname "$0")"

SDK="${ANDROID_SDK:-${ANDROID_HOME:-}}"
if [[ -z "$SDK" || ! -d "$SDK" ]]; then
  echo "error: set ANDROID_SDK to an Android SDK directory" >&2
  echo "  sdkmanager 'platforms;android-34' 'build-tools;34.0.0'" >&2
  exit 2
fi
BT="$SDK/build-tools/34.0.0"
PLATFORM="$SDK/platforms/android-34/android.jar"
if [[ ! -x "$BT/aapt2" || ! -f "$PLATFORM" ]]; then
  echo "error: SDK is missing build-tools;34.0.0 or platforms;android-34" >&2
  exit 2
fi
if [[ -z "${JAVA_HOME:-}" ]]; then
  if command -v javac >/dev/null 2>&1; then
    JAVA_BIN="$(dirname "$(command -v javac)")"
  else
    echo "error: set JAVA_HOME to a JDK 17+ installation" >&2
    exit 2
  fi
else
  JAVA_BIN="$JAVA_HOME/bin"
fi

export PATH="$JAVA_BIN:$PATH"

OUT="build"
DIST="dist"
rm -rf "$OUT"
mkdir -p "$OUT/compiled" "$OUT/gen" "$OUT/classes" "$OUT/dex" "$DIST"

echo "[1/6] aapt2 compile"
"$BT/aapt2" compile --dir app/src/main/res -o "$OUT/compiled"

echo "[2/6] aapt2 link"
"$BT/aapt2" link \
  -o "$OUT/base.apk" \
  -I "$PLATFORM" \
  --manifest app/src/main/AndroidManifest.xml \
  --java "$OUT/gen" \
  --min-sdk-version 24 \
  --target-sdk-version 34 \
  --version-code 40000 \
  --version-name 4.0.0 \
  "$OUT"/compiled/*.flat

echo "[3/6] javac"
SOURCES=$(find app/src/main/java "$OUT/gen" -name '*.java')
"$JAVA_BIN/javac" -source 8 -target 8 -encoding UTF-8 \
  -bootclasspath "$PLATFORM" \
  -classpath "$OUT/classes" \
  -d "$OUT/classes" \
  $SOURCES 2>&1 | grep -v 'bootstrap class path' || true

echo "[4/6] d8"
"$BT/d8" --lib "$PLATFORM" --min-api 24 --output "$OUT/dex" \
  $(find "$OUT/classes" -name '*.class' -print)

echo "[5/6] package"
(cd "$OUT" && zip -q -j base.apk dex/classes.dex)
"$BT/zipalign" -f 4 "$OUT/base.apk" "$OUT/aligned.apk"

if [[ ! -f "$OUT/debug.keystore" ]]; then
  "$JAVA_BIN/keytool" -genkeypair -keystore "$OUT/debug.keystore" \
    -alias androiddebugkey -storepass android -keypass android \
    -keyalg RSA -keysize 2048 -validity 10000 \
    -dname "CN=Android Debug,O=Android,C=US" >/dev/null 2>&1
fi

echo "[6/6] sign"
"$BT/apksigner" sign \
  --ks "$OUT/debug.keystore" \
  --ks-key-alias androiddebugkey \
  --ks-pass pass:android \
  --key-pass pass:android \
  --out "$DIST/maio-viewer.apk" \
  "$OUT/aligned.apk"
"$BT/apksigner" verify --print-certs "$DIST/maio-viewer.apk" | head -4
ls -lh "$DIST/maio-viewer.apk"
echo "built: $DIST/maio-viewer.apk"
