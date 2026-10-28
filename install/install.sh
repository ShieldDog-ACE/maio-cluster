#!/usr/bin/env bash
# maio 官方命令行安装入口 · 按平台分派
#
#   bash install/install.sh                 # 当前平台，用户级
#   sudo bash install/install.sh --system   # 当前平台，系统级
#
# 图形化组件不在本入口里；装完 CLI 后再跑 install/web/install-web.sh，
# 或给平台安装器加 --with-web。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

case "$(uname -s)" in
  Linux) exec bash "$HERE/linux/install.sh" "$@" ;;
  Darwin) exec bash "$HERE/mac/install.sh" "$@" ;;
  *)
    printf '\033[1;31m[error]\033[0m 不支持的平台：%s（目前提供 Linux / macOS；Windows 见 v4/windows）\n' "$(uname -s)" >&2
    exit 1
    ;;
esac
