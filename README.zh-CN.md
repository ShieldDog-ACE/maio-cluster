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

## 状态

V1 已实现：**注册 / 下载 / 删除**，以及本地推理、最小 HTTP API、开机自启。
长期目标（硬件完全掌控 + 图形化）见 [docs/ROADMAP.zh-CN.md](docs/ROADMAP.zh-CN.md)。

## 环境要求

- Linux x86_64
- 提供 `llama-server` 的 llama.cpp 构建（GPU 推理建议 CUDA 版）
- GGUF 模型

## 构建

```bash
make build   # 产出 bin/maio
make test    # 单元测试
make smoke   # 端到端烟雾测试（不需要 GPU）
```

## 安装

```bash
make build

# 源码环境（安装 /usr/local/bin/maio，并写入 /etc/maio/maio.conf）
sudo bash ./maio-cluster

# 打包形态（安装器/安装包名 = 仓库名）
sudo install maio-cluster

# 然后编辑 /etc/maio/maio.conf，把 llama_bin 指向你的 llama-server
```

## 配置

全局配置：`/etc/maio/maio.conf`（扁平 TOML，一台机器一份，**不是** `maio.toml`）。

| 键 | 含义 | 默认 |
| --- | --- | --- |
| `boot` | 开机自启：`1` 是，`0` 否 | `0` |
| `models_root` | 默认下载根目录 | `/var/lib/maio/models` |
| `state_dir` | 运行态与日志目录 | `/var/lib/maio` |
| `host` | 监听地址 | `127.0.0.1` |
| `port` | 监听端口 | `11434` |
| `mirror` | 下载镜像（优先快速 CDN） | `https://www.modelscope.cn` |
| `llama_bin` | `llama-server` 路径；空=自动探测 | 空 |
| `llama_lib` | 额外库目录，冒号分隔；空=自动 | 空 |
| `ctx_size` | 上下文长度；`0`=模型默认 | `0` |

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
sudo install maio-cluster                        安装 maio 自身
maio sudo install <name> [root]                  下载模型到 <root>/<name>/
maio sudo install <name> [root] --url <url>      按直链安装
maio sudo install <name> [root] --url <path>     登记本地已有 GGUF
maio list                                        列出已安装模型
maio show <name>                                 查看配置与文件路径
maio rm <name>                                   删除模型
maio run <name> [prompt]                         对话 / 单次推理
maio serve                                       启动局域网服务
maio ps                                          查看已加载模型
maio stop <name>                                 卸载模型
maio sudo boot 1|0                               开机自启开关
maio version                                     查看版本
```

## HTTP API

服务提供 Ollama API 的最小子集：

- `GET /api/version`
- `GET /api/tags`
- `POST /api/generate`（NDJSON 流）
- `POST /api/chat`（NDJSON 流）

默认地址：`http://127.0.0.1:11434`。

## 目录结构

```text
main.go
internal/cli        命令入口与子命令
internal/config     maio.conf 与 maio.toml
internal/download   断点续传、校验、本地复制
internal/engine     llama-server 进程管理
internal/index      名字 -> 直链索引
internal/naming     模型名规范化
internal/registry   模型目录扫描
internal/server     Ollama 兼容 HTTP API
internal/systemd    开机自启
internal/toml       最小 TOML 读取器
deploy/install.sh
scripts/smoke.sh
index.toml
docs/               版本路线
```

## 许可证

MIT，见 [LICENSE](LICENSE)。
