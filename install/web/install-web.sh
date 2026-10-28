#!/usr/bin/env bash
# maio 官方命令行安装器 · 图形化 Web 组件（可选层）
#
# V4.1 起 Web 静态产物已嵌入 maio 二进制，安装只是把资源落盘到数据目录：
#
#   maio web install                  # 用户级
#   sudo maio --system web install    # 系统级
#
# 本脚本是给安装器/离线环境用的薄包装，避免重复实现路径解析。
set -euo pipefail

SCOPE=user
FORCE=0
BIN="${MAIO_BIN:-}"

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
maio 图形化 Web 组件安装器（V4.1）

用法：
  bash install-web.sh [--system|--user] [--force] [--bin PATH]

说明：
  Web 资源已嵌入 maio 二进制，本脚本调用 "maio web install" 把它
  落盘到数据目录，不联网、不下载额外包。

  --system   系统级（需要 root，使用 /var/lib/maio 或 /Library/Application Support/maio）
  --user     用户级（默认）
  --force    覆盖已安装的 Web 资源
  --bin PATH 指定 maio 可执行文件
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --system) SCOPE=system; shift ;;
    --user) SCOPE=user; shift ;;
    --force|-f) FORCE=1; shift ;;
    --bin) BIN="${2:-}"; [[ -n "$BIN" ]] || die "--bin 需要路径"; shift 2 ;;
    --bin=*) BIN="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1（--help 查看用法）" ;;
  esac
done

if [[ "$SCOPE" == system ]]; then
  [[ "$EUID" -eq 0 ]] || die "系统级安装需要 root：sudo bash $0 --system"
else
  [[ "$EUID" -ne 0 ]] || die "当前是 root。用户级请以目标用户运行；系统级请加 --system"
fi

if [[ -z "$BIN" ]]; then
  if command -v maio >/dev/null 2>&1; then
    BIN="$(command -v maio)"
  elif [[ -x "$HOME/.local/bin/maio" ]]; then
    BIN="$HOME/.local/bin/maio"
  elif [[ -x /usr/local/bin/maio ]]; then
    BIN=/usr/local/bin/maio
  else
    die "找不到 maio；先用平台安装器安装 CLI，或用 --bin 指定"
  fi
fi
[[ -x "$BIN" ]] || die "不可执行：$BIN"

args=()
[[ "$SCOPE" == system ]] && args+=(--system)
args+=(web install)
[[ "$FORCE" -eq 1 ]] && args+=(--force)

say "调用 $BIN ${args[*]}"
exec "$BIN" "${args[@]}"
