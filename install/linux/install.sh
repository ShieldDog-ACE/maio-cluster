#!/usr/bin/env bash
# maio 官方命令行安装器 · Linux
#
# 安装 maio 内核 CLI（必需）。图形化 Web 组件是可选的第二层，
# 用 --with-web 或在装完 CLI 后单独运行 install/web/install-web.sh。
#
#   bash install.sh                                  # 用户级（默认）
#   sudo bash install.sh --system                    # 系统级
#   bash install.sh --from v4/dist/linux-amd64 # 离线：目录或 .tar.gz
#   bash install.sh --with-web                       # 同时装图形化组件
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSION="${MAIO_VERSION:-4.1.1}"
BASE_URL="${MAIO_BASE_URL:-https://github.com/ShieldDog-ACE/maio-cluster/releases/download}"
SCOPE=user
FROM=""
WITH_WEB=0
WORKER=0
NO_START=0
PREFIX=""
SHA256=""
TMP=""

cleanup() { if [[ -n "$TMP" && -d "$TMP" ]]; then rm -rf "$TMP"; fi; }
trap cleanup EXIT

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
maio 官方命令行安装器 · Linux

用法：
  bash install.sh [选项]

作用域（默认用户级）：
  --user              装到 ~/.local，使用 systemd 用户单元
  --system            装到 /usr/local，使用 systemd 系统单元（需要 root）

安装来源：
  --from PATH         本地安装包目录或 .tar.gz（离线安装）
  --version V         发布版本（默认 4.1.1）
  --base-url URL      发布下载根地址
  --sha256 HEX        安装包 SHA-256（离线安装时建议提供）

组件：
  --with-web          额外安装图形化 Web 组件

服务：
  --worker            只装子节点 agent，不跑协调者
  --no-start          只落盘二进制，不安装/启动服务
  --prefix PATH       覆盖安装前缀

  -h, --help          显示本帮助

示例：
  bash install.sh
  sudo bash install.sh --system
  bash install.sh --from v4/dist/linux-amd64
  sudo bash install.sh --system --with-web
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --user) SCOPE=user; shift ;;
    --system) SCOPE=system; shift ;;
    --from) FROM="${2:-}"; [[ -n "$FROM" ]] || die "--from 需要路径"; shift 2 ;;
    --from=*) FROM="${1#*=}"; shift ;;
    --version) VERSION="${2:-}"; [[ -n "$VERSION" ]] || die "--version 需要版本号"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    --base-url) BASE_URL="${2:-}"; [[ -n "$BASE_URL" ]] || die "--base-url 需要 URL"; shift 2 ;;
    --base-url=*) BASE_URL="${1#*=}"; shift ;;
    --sha256) SHA256="${2:-}"; [[ -n "$SHA256" ]] || die "--sha256 需要摘要"; shift 2 ;;
    --sha256=*) SHA256="${1#*=}"; shift ;;
    --with-web) WITH_WEB=1; shift ;;
    --worker) WORKER=1; shift ;;
    --no-start) NO_START=1; shift ;;
    --prefix) PREFIX="${2:-}"; [[ -n "$PREFIX" ]] || die "--prefix 需要路径"; shift 2 ;;
    --prefix=*) PREFIX="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1（--help 查看用法）" ;;
  esac
done

# ---- 运行身份 ----------------------------------------------------------
if [[ "$SCOPE" == system ]]; then
  [[ "$EUID" -eq 0 ]] || die "系统级安装需要 root：sudo bash $0 --system"
else
  [[ "$EUID" -ne 0 ]] || die "当前是 root。用户级安装请以目标用户运行；要装成系统服务请加 --system"
fi

# ---- 架构 --------------------------------------------------------------
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "不支持的架构：$(uname -m)（目前提供 amd64 / arm64）" ;;
esac

# ---- 路径 --------------------------------------------------------------
if [[ -z "$PREFIX" ]]; then
  if [[ "$SCOPE" == system ]]; then PREFIX=/usr/local; else PREFIX="$HOME/.local"; fi
fi
BIN="$PREFIX/bin"
LIB="$PREFIX/lib/maio"
if [[ "$SCOPE" == system ]]; then
  CONF=/etc/maio/cluster.toml
  DATA=/var/lib/maio
  LOG=/var/log/maio
else
  CONF="${XDG_CONFIG_HOME:-$HOME/.config}/maio/cluster.toml"
  DATA="${XDG_DATA_HOME:-$HOME/.local/share}/maio"
  LOG="$DATA/logs"
fi

# ---- 工具 --------------------------------------------------------------
need_curl() { command -v curl >/dev/null 2>&1 || die "需要 curl（或改用 --from 离线安装）"; }

verify_sha() {
  local file="$1" want="$2"
  if [[ -z "$want" ]]; then return 0; fi
  printf '%s  %s\n' "$want" "$file" | sha256sum -c - >/dev/null 2>&1 \
    || die "SHA-256 校验失败：$file"
  say "SHA-256 校验通过"
}

fetch_verify() {
  # fetch <url> <out>：下载并尽量校验同名 .sha256
  local url="$1" out="$2"
  need_curl
  say "下载 $url"
  curl -fL --retry 3 --connect-timeout 15 -o "$out" "$url" \
    || die "下载失败：$url"
  if [[ -n "$SHA256" ]]; then
    verify_sha "$out" "$SHA256"
  elif curl -fsSL --connect-timeout 10 -o "$out.sha256" "$url.sha256" 2>/dev/null; then
    ( cd "$(dirname "$out")" && sha256sum -c "$(basename "$out").sha256" >/dev/null ) \
      || die "SHA-256 校验失败：$out"
    say "SHA-256 校验通过"
  else
    warn "没有 .sha256 可校验；生产环境请用 --sha256 固定摘要"
  fi
}

# ---- 解析安装包 --------------------------------------------------------
BUNDLE=""
if [[ -n "$FROM" ]]; then
  if [[ -d "$FROM" ]]; then
    BUNDLE="$(cd "$FROM" && pwd)"
  elif [[ -f "$FROM" ]]; then
    verify_sha "$FROM" "$SHA256"
    TMP="$(mktemp -d)"
    tar -xzf "$FROM" -C "$TMP" || die "无法解压：$FROM"
    BUNDLE="$TMP"
  else
    die "找不到安装包：$FROM"
  fi
elif [[ -x "$HERE/maio" ]]; then
  # 从已解压的发布目录里直接运行本脚本
  BUNDLE="$HERE"
else
  TMP="$(mktemp -d)"
  PKG="$TMP/maio-linux-$ARCH.tar.gz"
  fetch_verify "$BASE_URL/v$VERSION/maio-linux-$ARCH.tar.gz" "$PKG"
  tar -xzf "$PKG" -C "$TMP" || die "无法解压安装包"
  BUNDLE="$TMP"
fi
[[ -x "$BUNDLE/maio" ]] || die "安装包缺少 maio 可执行文件：$BUNDLE"

# ---- 落盘 --------------------------------------------------------------
say "安装到 $PREFIX（$SCOPE 作用域）"
install -d -m 0755 "$BIN" "$LIB" "$LIB/bin"
# 先写临时名再原子替换：升级时不会撞上正在运行的二进制（ETXTBSY）
install -m 0755 "$BUNDLE/maio" "$LIB/.maio.new"
mv -f "$LIB/.maio.new" "$LIB/maio"
ln -sfn "$LIB/maio" "$BIN/maio"
for b in ggml-rpc-server llama-server; do
  if [[ -f "$BUNDLE/bin/$b" ]]; then
    install -m 0755 "$BUNDLE/bin/$b" "$LIB/bin/.$b.new"
    mv -f "$LIB/bin/.$b.new" "$LIB/bin/$b"
    say "已附带 $b"
  fi
done

if [[ "$SCOPE" == system ]]; then
  getent group maio >/dev/null 2>&1 || groupadd --system maio
  id maio >/dev/null 2>&1 || useradd --system --gid maio --home "$DATA" --shell /usr/sbin/nologin maio
  install -d -m 0755 /etc/maio "$DATA" "$DATA/models" "$LOG"
  chown -R maio:maio "$DATA" "$LOG"
fi

run_maio() {
  if [[ "$SCOPE" == system ]]; then "$BIN/maio" --system "$@"; else "$BIN/maio" "$@"; fi
}

if [[ ! -f "$CONF" ]]; then
  run_maio init --force >/dev/null || die "初始化集群身份失败"
  say "已生成集群身份：$CONF"
else
  say "保留现有配置：$CONF"
fi
if [[ "$SCOPE" == system ]]; then
  chown root:maio "$CONF"
  chmod 0640 "$CONF"
  chown root:maio /etc/maio
  chmod 0750 /etc/maio
fi

# ---- 服务 --------------------------------------------------------------
if [[ "$NO_START" -eq 0 ]]; then
  if [[ "$WORKER" -eq 1 ]]; then
    run_maio service install --worker || warn "服务安装失败；二进制已就绪，可手动运行：$BIN/maio agent"
  else
    run_maio service install || warn "服务安装失败；二进制已就绪，可手动运行：$BIN/maio cluster up"
  fi
else
  say "跳过服务安装（--no-start）"
fi

# ---- 图形化（可选层） --------------------------------------------------
if [[ "$WITH_WEB" -eq 1 ]]; then
  run_maio web install --force
  if [[ "$SCOPE" == system ]]; then
    chown -R maio:maio "$DATA/web" 2>/dev/null || true
  fi
fi

# ---- 收尾 --------------------------------------------------------------
if [[ ":$PATH:" != *":$BIN:"* ]]; then
  warn "$BIN 不在 PATH；当前 shell 请用 $BIN/maio，或把 $BIN 加进 PATH"
fi

echo
say "安装完成"
printf '  作用域  %s\n' "$SCOPE"
printf '  二进制  %s\n' "$BIN/maio"
printf '  配置    %s\n' "$CONF"
printf '  数据    %s\n' "$DATA"
if [[ "$WORKER" -eq 1 ]]; then
  if [[ "$SCOPE" == system ]]; then
    printf '  下一步  sudo %s --system cluster join '"'"'MAIO1-...'"'"'\n' "$BIN/maio"
  else
    printf '  下一步  %s cluster join '"'"'MAIO1-...'"'"'\n' "$BIN/maio"
  fi
else
  printf '  下一步  %s cluster up\n' "$BIN/maio"
fi
if [[ "$WITH_WEB" -eq 0 ]]; then
  printf '  图形化  bash %s/web/install-web.sh  （可选）\n' "$(cd "$HERE/.." && pwd)"
fi
