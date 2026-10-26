#!/usr/bin/env bash
# Compatibility wrapper: use the repository root installer `maio-cluster`.
exec bash "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/maio-cluster" "$@"
