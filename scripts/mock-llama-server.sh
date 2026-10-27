#!/usr/bin/env bash
# Executable stand-in for llama-server. The process command line contains
# "llama-server" so maio's process-identity guard accepts it.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "$here/mock-llama.py" "$@"
