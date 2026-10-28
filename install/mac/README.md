# macOS 官方安装器

`install.sh` 安装 maio 内核 CLI，并可选装图形化 Web 组件。默认**用户级** LaunchAgent，不加 sudo。

## 用法

```bash
bash install.sh                 # 用户级，装到 ~/.local
sudo bash install.sh --system   # 系统级，装到 /usr/local
bash install.sh --worker        # 只跑 agent
bash install.sh --no-start      # 只落盘，不装服务
```

## 选项

| 选项 | 说明 |
| --- | --- |
| `--user` / `--system` | 作用域，默认 `--user`；`--system` 需要 root |
| `--from PATH` | 本地安装包目录或 `.tar.gz`（离线安装） |
| `--version V` | 发布版本，默认 `4.1.1` |
| `--base-url URL` | 发布下载根地址；也可用 `MAIO_BASE_URL` |
| `--sha256 HEX` | 安装包摘要；建议离线安装时固定 |
| `--with-web` | 额外安装图形化组件 |
| `--worker` | 安装为子节点 agent，不跑协调者 |
| `--no-start` | 只落盘二进制，不安装/启动服务 |
| `--prefix PATH` | 覆盖二进制前缀（默认 `~/.local` 或 `/usr/local`） |

## 做了什么

1. 按 `uname -m` 选 `arm64` / `amd64` 包。
2. 解析安装包：`--from` → 本地目录/压缩包；否则从发布地址下载并尽量校验 `.sha256`。
3. 落盘 `maio` 和可选的 `ggml-rpc-server`、`llama-server`，在 `$PREFIX/bin/maio` 建软链。
4. 没有配置时执行 `maio init` 生成集群身份；已有配置保持不动。
5. 安装并启动 launchd 单元（用户级 LaunchAgent，系统级 LaunchDaemon）。
6. `--with-web` 时调用 `maio web install` 把嵌入的静态组件落盘。

## 路径

| 内容 | 用户级 | 系统级 |
| --- | --- | --- |
| 二进制 | `~/.local/bin/maio` | `/usr/local/bin/maio` |
| 运行时 | `~/Library/Application Support/maio/runtime` | `/usr/local/lib/maio` |
| 配置 | `~/Library/Application Support/maio/cluster.toml` | `/Library/Application Support/maio/cluster.toml` |
| 数据 | `~/Library/Application Support/maio` | `/Library/Application Support/maio` |
| 服务 | `~/Library/LaunchAgents/dev.maio.cluster.plist` | `/Library/LaunchDaemons/dev.maio.cluster.plist` |
| 日志 | `~/Library/Logs/maio` | `/Library/Logs/maio` |

## 验证

```bash
maio version
maio id
maio status
launchctl print "gui/$(id -u)/dev.maio.cluster"    # 用户级
sudo launchctl print system/dev.maio.cluster       # 系统级
```

## 卸载

```bash
# 用户级（会一并删掉模型，先备份）
maio service uninstall
rm -rf ~/.local/bin/maio "$HOME/Library/Application Support/maio" "$HOME/Library/Logs/maio"

# 系统级
sudo maio --system service uninstall
sudo rm -rf /usr/local/lib/maio /usr/local/bin/maio "/Library/Application Support/maio" /Library/Logs/maio
```

## 说明

- 安装包只带 CPU 版运行时；Apple Silicon 要用 Metal，把 Metal 版 `llama-server`
  放进 `$PREFIX/lib/maio/bin/`，或在配置里指向已有路径。
- 从网络下载的包若被 Gatekeeper 拦，到“系统设置 → 隐私与安全性”允许一次即可。
- 系统级安装只影响 `/usr/local` 与 `/Library`，不会改用户目录。
- 安装失败时二进制可能已落盘；看错误信息，或加 `--no-start` 分步排查。
