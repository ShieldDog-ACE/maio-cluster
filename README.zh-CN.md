# maio-cluster

<p align="center">
  <a href="README.md"><img alt="English" src="https://img.shields.io/badge/English-1f6feb?style=for-the-badge"></a>
  <a href="README.zh-CN.md"><img alt="简体中文" src="https://img.shields.io/badge/%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-d73a4a?style=for-the-badge"></a>
</p>
<p align="center"><sub>语言：点击徽章切换 · Language: click a badge to switch</sub></p>

为分布式局域网打造的大模型推理服务后端。

`maio` 是一个轻量、类 ollama 的命令行工具 + HTTP 服务，构建在
[llama.cpp](https://github.com/ggml-org/llama.cpp) 之上。它负责注册、下载、删除 GGUF 模型，
在本地跑推理，并向局域网暴露 Ollama 兼容 API。
控制面用 Go 编写，**零第三方依赖**，单二进制分发。

> **V2 起所有命令行报错与提示均为英文**，后续版本再加入语言包；仓库说明仍保持中英双语。

## 状态

V2 已实现：

- **英文提示 + 稳定错误码**（后续可加载语言包）。
- **报错可执行**：配置拼写/类型错误定位到行号；缺文件、GGUF 头损坏、HTTP 错误、磁盘不足、
  权限不足、端口占用、引擎崩溃、服务失败都会说明原因和下一步。
- **硬件掌握**：CPU / 内存 / 磁盘探测，`nvidia-smi` 读取 NVIDIA GPU，解析 GGUF 元数据
  （上下文、量化、视觉），并在加载前做**显存预算检查**。
- **`maio doctor`**：一条命令检查配置、硬件、`llama-server`、systemd、模型、显存预算和日志。
- **审计日志**：安装、下载、登记、删除、引擎启停、失败等以 JSON Lines 落盘，`maio logs` 查看。
- **默认开机自启**；安装包/安装器会立即启动服务。
- **统一命令写法**：`sudo maio ...`；旧的 `maio sudo ...` 会报错并给出正确写法。

## 环境要求

- Linux x86_64
- 提供 `llama-server` 的 llama.cpp 构建（GPU 推理建议 CUDA 版）
- GGUF 模型
- 可选：NVIDIA 驱动 + `nvidia-smi`（显存检测与 GPU 推理）

## 构建

```bash
make build   # 产出 bin/maio
make test    # 单元测试
make smoke   # 端到端烟雾测试（不需要 GPU、不需要 root）
make deb     # 产出 dist/maio-cluster_<version>_<arch>.deb
make repo    # 产出 dist/apt（本地 apt 仓库）
```

## 安装

### apt / Debian 包（推荐）

```bash
# 一次性：构建 .deb、配置本地 apt 仓库、安装并启动
sudo bash deploy/apt-install.sh

# 仓库注册后，任务书里的命令即可直接使用：
sudo apt install maio-cluster
```

不想配仓库时也可以一次性安装：

```bash
make deb
sudo apt install ./dist/maio-cluster_2.0.0_amd64.deb
```

如果已经有一个用户级服务占用 11434，先停掉它，否则系统服务无法绑定端口：

```bash
maio boot 0 --user
sudo bash deploy/apt-install.sh
```

安装包会写入 `/usr/bin/maio`、`/etc/maio/maio.conf`（`boot = 1`）和 systemd 服务，
并立即 enable + start。

### 源码安装

```bash
make build
sudo bash ./maio-cluster              # 安装并默认开机自启
sudo bash ./maio-cluster --no-start   # 只安装，不启动
sudo bash ./maio-cluster --boot 0     # 安装但关闭开机自启
```

`deploy/install.sh` 是 `./maio-cluster` 的兼容包装。

### 无 root / 用户级服务

```bash
# 使用用户可写的配置和状态目录，然后启用用户级服务
maio boot 1 --user
```

用户级服务在登录后启动；若要在开机时（未登录）启动：

```bash
sudo loginctl enable-linger "$USER"
```

## 配置

全局配置：`/etc/maio/maio.conf`（扁平 TOML，一台机器一份，**不是** `maio.toml`）。
无 root 时，若 `/var/lib/maio` 不可写，maio 会回退到 `~/.config/maio/maio.conf`
和用户状态目录；`MAIO_CONF` 可覆盖两者。

| 键 | 含义 | 默认 |
| --- | --- | --- |
| `boot` | 开机自启：`1` 是，`0` 否 | `1` |
| `models_root` | 默认下载根目录 | `/var/lib/maio/models` |
| `state_dir` | 运行态与日志目录 | `/var/lib/maio` |
| `host` | 监听地址 | `127.0.0.1` |
| `port` | 监听端口 | `11434` |
| `mirror` | 下载镜像（优先快速 CDN） | `https://www.modelscope.cn` |
| `llama_bin` | `llama-server` 路径；空=自动探测 | 空 |
| `llama_lib` | 额外库目录，冒号分隔；空=自动 | 空 |
| `ctx_size` | 上下文长度；`0`=模型默认 | `0` |

未知键和类型错误会带行号报错，不再静默忽略。

每个模型一份：`<models_root>/<name>/maio.toml`，只有三个字段。

```toml
name   = "qwen3.5-9b"
format = "gguf"
path   = "models/Qwen3.5-9B-Q4_K_M.gguf"
```

一个模型一个目录；拷贝目录到另一台机器即是完整迁移。
模型名全小写、空格转连字符：`DeepSeek v4.1` 规范化为 `deepseek-v4.1`。

## 命令

```text
sudo apt install maio-cluster                       安装 maio 自身（安装包）
sudo bash ./maio-cluster                            安装 maio 自身（源码）

sudo maio install <name> [root]                     下载模型到 <root>/<name>/
sudo maio install <name> [root] --url <url>         按直链安装
sudo maio install <name> [root] --url <path>        登记本地已有 GGUF
sudo maio install <name> [root] --sha256 <hex>      按 SHA-256 校验直链下载

maio list [--wide] [--json]                         列出已安装模型
maio show <name> [--json]                           查看配置、文件与能力
maio rm <name> [-y]                                 删除模型及其文件
maio run <name> [prompt] [--ctx N] [--force]        对话 / 单次推理

maio serve [--host ADDR] [--port N]                 启动 HTTP 服务
maio ps [--json]                                    查看已加载模型
maio stop <name> | --all                            卸载模型、释放显存
sudo maio boot 1|0                                  开机自启开关
maio boot 1|0 --user                                无 root 的用户级服务

maio doctor [--json]                                健康检查
maio logs [--tail N] [--json] [--path]              读取审计日志
maio version [--json]                               版本与路径
```

请使用 `sudo maio ...`，**不要**再用 `maio sudo ...`。

### 退出码

| 码 | 含义 |
| --- | --- |
| `0` | 成功 |
| `2` | 用法 / 参数错误 |
| `3` | 找不到（模型、文件、命令） |
| `4` | 冲突（已登记、目录非空） |
| `5` | 权限或文件系统 I/O |
| `6` | 下载失败 |
| `7` | 校验失败（SHA-256 / GGUF） |
| `8` | 引擎失败 |
| `9` | 硬件 / doctor 失败 |
| `10` | 配置错误 |
| `11` | 服务 / systemd 错误 |
| `130` | 被中断 |

每条错误还会打印稳定错误码（如 `MAIO-E006`）和 `hint:` 修复建议。

## 硬件与显存

`maio run`、`maio serve` 和 `maio doctor` 会读取 GGUF 头，得到架构、上下文、层数/头数、
量化和视觉能力。加载前会把 权重 + KV 缓存 + 开销 与空闲显存比较：

- 放不下时在 llama-server 启动**之前**拒绝；
- 报错会给出具体的 `--ctx` 建议值；
- `--force` 可跳过检查（仅建议了解真实内存布局时使用）；
- `maio doctor` 会显示每个模型在当前配置下的预算。

## 审计日志

日志为 JSON Lines，位于 `<state_dir>/log/maio.log`（或用户目录回退路径），记录：

- 每次 CLI 调用及退出码；
- 安装来源、目标、SHA-256、大小、耗时；
- 本地登记及原地/硬链接/复制方式；
- 删除的路径与大小；
- 引擎启停、端口、PID、上下文和显存估算；
- 服务启停与开机自启变更；
- 失败的错误码与修复建议。

```bash
maio logs --tail 50
maio logs --json
maio logs --path
```

引擎 stdout/stderr 仍在 `<state_dir>/log/<model>.log`。

## HTTP API

服务提供 Ollama API 的最小子集：

- `GET /api/version`
- `GET /api/tags`
- `POST /api/generate`（NDJSON 流）
- `POST /api/chat`（NDJSON 流）

默认地址：`http://127.0.0.1:11434`。

错误为 JSON，保留兼容的 `error` 字符串，并附加 `code`、`message`、`hint` 字段。
模型空闲 5 分钟后自动卸载。

## 目录结构

```text
main.go
internal/cli        命令入口、安装/列表/查看/删除/运行/服务、doctor、logs
internal/config     maio.conf / maio.toml、校验与用户级回退
internal/download   断点续传、校验、磁盘检查、本地复制
internal/engine     llama-server 进程管理、显存检查、安全停止
internal/errs       稳定错误码、退出码与修复建议
internal/gguf       GGUF 元数据解析（上下文/量化/视觉）
internal/hardware   CPU/内存/磁盘/NVIDIA GPU 探测
internal/index      名字 -> 直链索引
internal/logging    JSONL 审计日志
internal/naming     模型名规范化
internal/registry   模型目录扫描
internal/server     Ollama 兼容 HTTP API
internal/systemd    系统级与用户级服务
internal/toml       带行号的最小 TOML 读取器
internal/vram       显存预算估算
deploy/             安装包、apt 仓库与安装脚本
scripts/smoke.sh    端到端测试
index.toml
docs/               版本路线
```

## 许可证

MIT，见 [LICENSE](LICENSE)。
