# Linux 官方安装器

`install.sh` 安装 maio 内核 CLI，并可选装图形化 Web 组件。默认**用户级**，不加 sudo。

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
| `--version V` | 发布版本，默认 `4.1.0` |
| `--base-url URL` | 发布下载根地址；也可用 `MAIO_BASE_URL` |
| `--sha256 HEX` | 安装包摘要；建议离线安装时固定 |
| `--with-web` | 额外安装图形化组件 |
| `--worker` | 安装为子节点 agent，不跑协调者 |
| `--no-start` | 只落盘二进制，不安装/启动服务 |
| `--prefix PATH` | 覆盖二进制前缀（默认 `~/.local` 或 `/usr/local`） |

## 做了什么

1. 按 `uname -m` 选 `amd64` / `arm64` 包。
2. 解析安装包：`--from` → 本地目录/压缩包；否则从发布地址下载并尽量校验 `.sha256`。
3. 落盘 `maio` 和可选的 `ggml-rpc-server`、`llama-server`，在 `$PREFIX/bin/maio` 建软链。
4. 系统级：创建 `maio` 用户/组，准备 `/etc/maio`、`/var/lib/maio`、`/var/log/maio`。
5. 没有配置时执行 `maio init` 生成集群身份；已有配置保持不动。
6. 安装并启动 systemd 单元（用户级 `systemctl --user`，系统级 `systemctl`）。
7. `--with-web` 时调用 `maio web install` 把嵌入的静态组件落盘。

## 路径

| 内容 | 用户级 | 系统级 |
| --- | --- | --- |
| 二进制 | `~/.local/bin/maio` | `/usr/local/bin/maio` |
| 运行时 | `~/.local/lib/maio` | `/usr/local/lib/maio` |
| 配置 | `~/.config/maio/cluster.toml` | `/etc/maio/cluster.toml` |
| 数据 | `~/.local/share/maio` | `/var/lib/maio` |
| 服务 | `systemctl --user` | `systemctl` |

## 验证

```bash
maio version
maio id
maio status
systemctl --user status maio     # 或 sudo systemctl status maio
```

无桌面会话的服务器用用户级 systemd 时，需要让用户服务在登出后继续运行：

```bash
sudo loginctl enable-linger "$USER"
```

## 卸载

```bash
# 用户级
maio service uninstall
rm -rf ~/.local/lib/maio ~/.local/bin/maio ~/.config/maio ~/.local/share/maio

# 系统级
sudo maio --system service uninstall
sudo rm -rf /usr/local/lib/maio /usr/local/bin/maio /etc/maio /var/lib/maio /var/log/maio
sudo userdel maio 2>/dev/null || true
sudo groupdel maio 2>/dev/null || true
```

## 说明

- 安装包只带 CPU 版运行时；要用 CUDA / ROCm，把对应的 `llama-server`、`ggml-rpc-server`
  放进 `$PREFIX/lib/maio/bin/`，或在配置里指向已有路径。
- `install.sh` 不处理 `.deb`；deb 安装继续走 `v4/linux/packaging/build-deb.sh`。
- 安装失败时二进制可能已落盘；看错误信息，或加 `--no-start` 分步排查。
