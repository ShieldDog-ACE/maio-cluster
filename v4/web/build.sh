#!/usr/bin/env bash
# Build the optional web component into web/assets.
#
#   ./web/build.sh
#
# Requires tsc (TypeScript). The output is embedded into the maio binary and
# materialized by "maio web install".
set -euo pipefail
cd "$(dirname "$0")"
command -v tsc >/dev/null 2>&1 || { echo "tsc not found: install TypeScript first" >&2; exit 1; }
rm -rf assets
mkdir -p assets
tsc -p tsconfig.json
cp src/index.html src/style.css assets/
echo "built web/assets: $(find assets -type f | wc -l) files"
