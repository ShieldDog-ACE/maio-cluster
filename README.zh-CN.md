# maio-cluster

<p align="center">
  <a href="README.md"><img alt="English" src="https://img.shields.io/badge/English-1f6feb?style=for-the-badge"></a>
  <a href="README.zh-CN.md"><img alt="简体中文" src="https://img.shields.io/badge/%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-d73a4a?style=for-the-badge"></a>
</p>
<p align="center"><sub>语言：点击徽章切换 · Language: click a badge to switch</sub></p>

为局域网打造的大模型推理服务后端，**V3 稳定版：服务器级**。

`maio` 是构建在 [llama.cpp](https://github.com/ggml-org/llama.cpp) 之上的
轻量命令行工具 + HTTP 服务。它负责注册、下载、删除 GGUF 模型，在本地跑推理，
并同时提供 **Ollama 兼容 API** 与 **OpenAI 兼容 API**。
控制面用 Go 编写，**零第三方依赖**，单二进制；只支持 **Linux x86-64**（Ubuntu 已实测）。

## V3 做了什么

V3 不再只是“能跑”，而是能长期挂在机房里：

- **并发与背压**：全局并发槽 + 有界等待队列；队列满直接 429，不会把内存打爆。
  每个请求可取消，流式响应逐块 flush。
- **鉴权**：默认 `auth = "auto"`。监听 `0.0.0.0` 时自动生成 256 位 API Key（0600），
  未带 Key 的请求返回 `401`。`maio token` 管理查看/轮换/设置/清除。
- **限流**：按客户端 IP 的令牌桶，默认 600 次/分钟、突发 120；超限返回 `429` + `Retry-After`。
- **内存目标**：控制面 Go 软内存上限默认 180 MiB，实际常驻通常 20–40 MiB。
  `make soak` 会断言 **控制面 RSS < 200 MiB**；模型与 KV 显存另算。
- **守护进程**：systemd `Type=notify` + `WatchdogSec=30` + `Restart=always`；
  启动完成才 READY，卡死会被看门狗重启。SIGTERM 时先 drain 请求，再停引擎。
- **崩溃自愈**：常驻模型（`maio load`）由 supervisor 每 10 秒检查，挂了自动拉起，
  指数退避；进程退出时释放全部引擎，重启后自动恢复常驻模型。
- **安全沙箱**：专用 `maio` 用户、`ProtectSystem=strict`、`NoNewPrivileges`、
  `CapabilityBoundingSet=`、`RestrictAddressFamilies`、私有 `/tmp` 等。
- **可观测**：`/healthz`、`/readyz`、`/metrics`（Prometheus 文本）、`/api/stats`（JSON），
  以及 `maio status`、`maio stats`、`maio doctor`。
- **完整命令行**：参考 ollama，补齐 `pull / list / show / cp / rm / run / serve / ps /
  load / stop / status / stats / service / restart / boot / config / token / doctor /
  logs / version / completion / upgrade / rollback`。
- **升级与回滚**：`maio upgrade <binary>` 原子替换并自动备份，重启失败自动回滚；
  `maio rollback` 手动恢复。
- **llama.cpp 轻量化**：Linux-only 最小 `llama-server` 构建/裁剪脚本，去掉
  CLI、bench、quantize、测试、示例和 Web UI；支持静态链接 llama/ggml，
  CUDA 只编译目标架构（V100 默认 `sm_70`）。
- **开机自启**：安装包默认 `boot = 1`，systemd 开机启动；断电恢复见“有电就启动”。

## 环境要求

- **Linux x86-64**（不提供 macOS/Windows 版本）
- 一个 `llama-server`（可用 `make llama` 构建，或用 `make llama-bundle` 裁剪现有构建）
- GGUF 模型
- 可选：NVIDIA 驱动 + CUDA 运行时（GPU 推理）；`nvidia-smi` 用于显存探测
- 可选：Ubuntu 22.04+ / systemd 249+（V3 在 Ubuntu 26.04、Go 1.27、V100 上验证）

## 构建与测试

```bash
make build        # 产出 bin/maio（默认 3.0.0）
make test         # 单元测试
make race         # 竞态检测
make smoke        # 端到端冒烟（无需 GPU/root）
make soak         # 并发 + 200MB 内存目标压测（mock 引擎，无需 GPU）
make verify       # vet + test + smoke
```

完整的 V3 验证（含真实模型）见 [`docs/V3-VERIFICATION.zh-CN.md`](docs/V3-VERIFICATION.zh-CN.md)。

## 安装

### apt / Debian 包（推荐）

```bash
sudo bash deploy/apt-install.sh     # 构建 .deb、注册本地 apt 仓库、安装并启动
sudo apt install maio-cluster       # 之后可直接用
```

一次性安装：

```bash
make deb
sudo apt install ./dist/maio-cluster_3.0.0_amd64.deb
```

安装包会：

- 创建无特权系统用户 `maio`（加入 `video` / `render` 组）；
- 写入 `/usr/bin/maio`、`/etc/maio/maio.conf`（`boot = 1`、`run_as = "maio"`）；
- 写入加固后的 systemd unit；
- 立即 `enable --now`，开机自启。

### 源码安装

```bash
make build
sudo bash ./maio-cluster              # 安装并开机自启
sudo bash ./maio-cluster --no-start   # 只安装
sudo bash ./maio-cluster --boot 0     # 安装但关闭自启
```

### 无 root / 用户级服务

```bash
maio boot 1 --user
sudo loginctl enable-linger "$USER"   # 未登录也启动
```

用户级服务使用用户自己的配置与状态目录，不创建 `maio` 系统用户。

### 有电就启动

“来电自动开机”由主板固件决定，操作系统无法远程修改。需要两步：

1. BIOS/UEFI 中开启 **Restore on AC Power Loss / AC Back = Power On**；
2. 安装时保持 `boot = 1`（默认），并确认 `systemctl is-enabled maio` 为 `enabled`。

市电恢复后机器上电，systemd 启动 `maio.service`；若上次是异常掉电，`Restart=always`
也会在进程崩溃时自动拉起。可用 `maio status` 验证。

## llama.cpp 轻量化（Linux x86-64）

两种方式，任选其一：

### 1. 从源码构建最小 `llama-server`

```bash
make llama                    # 默认 CUDA=1, CUDA_ARCH=70, NATIVE=1
# 或：
CUDA=0 make llama             # 纯 CPU
CUDA_ARCH=70 CUDA_ROOT=/usr/local/cuda-12.9 make llama
```

产物：`dist/maio-llama/bin/llama-server`。

脚本会关闭 Vulkan/SYCL/OpenCL/Metal/RPC、测试、示例、Web UI、curl/openssl，
只构建 `llama-server` 目标；`BUILD_SHARED_LIBS=OFF` 把 llama/ggml 静态链接进单文件。
CUDA 运行时（`libcudart`、`libcublas`、`libcublasLt`）仍由 NVIDIA 驱动/工具包提供，
避免把 800MB+ 的 CUDA 库塞进“轻量”运行时。Ubuntu 26.04 的 GCC 15 会自动加
`-allow-unsupported-compiler`。

### 2. 裁剪已有的 llama.cpp 构建（无需编译器）

```bash
LLAMA_BUILD=/path/to/llama.cpp/build make llama-bundle
```

只复制 `llama-server` 及其真正加载的 7 个 `.so`，剥离符号后约 72MB（原 `bin/` 81MB），
不含 CLI/bench/quantize 等。脚本会实际执行 `llama-server --version` 验证产物可用。

打包成独立 deb：

```bash
make llama-bundle
make deb-llama                # dist/maio-llama_3.0.0_amd64.deb -> /opt/maio/llama
```

然后在 `/etc/maio/maio.conf` 设置：

```toml
llama_bin = "/opt/maio/llama/bin/llama-server"
llama_lib = "/opt/maio/llama/lib:/usr/local/cuda/lib64"
```

## 配置

全局配置：`/etc/maio/maio.conf`（扁平 TOML，一台机器一份，**不是** `maio.toml`）。
无 root 时回退到 `~/.config/maio/maio.conf` 和用户状态目录；`MAIO_CONF` 覆盖。

### 服务与安全

| 键 | 含义 | 默认 |
| --- | --- | --- |
| `host` / `port` | 监听地址 / 端口 | `127.0.0.1` / `11434` |
| `auth` | `auto` / `on` / `off` | `auto` |
| `api_key` | 显式 Key（优先用 `maio token` 的 0600 文件） | 空 |
| `allow_insecure` | 非回环地址允许 `auth=off` | `0` |
| `trust_proxy` | 信任 `X-Forwarded-For`（仅反代后面） | `0` |
| `cors` | `1`=允许跨域浏览器访问（默认关闭） | `0` |
| `tls_cert` / `tls_key` | 可选 TLS 终止 | 空 |
| `rate_limit` / `rate_burst` | 每客户端每分钟请求数 / 突发 | `600` / `120` |
| `max_concurrency` | 同时执行的推理请求数 | `8` |
| `max_queue` | 允许等待的请求数 | `64` |
| `queue_timeout` | 等待槽位超时（秒，0=永久） | `300` |
| `request_timeout` | 单请求总超时（秒，0=不限） | `0` |
| `max_body_mb` | 请求体上限 | `16` |
| `idle_unload` | 未常驻模型空闲卸载（秒，0=不卸） | `300` |
| `access_log` | 每个 HTTP 请求写一行审计 | `1` |

### 引擎与资源

| 键 | 含义 | 默认 |
| --- | --- | --- |
| `llama_bin` / `llama_lib` | `llama-server` / 额外库目录 | 自动探测 |
| `ctx_size` | 上下文；`0`=模型默认 | `0` |
| `parallel` | llama-server 槽位（`-np`） | `2` |
| `threads` | CPU 线程；`0`=引擎默认 | `0` |
| `kv_type` | KV 缓存类型 `f16` / `q8_0` / `q4_0` | `f16` |
| `flash_attn` | `auto` / `on` / `off` | `auto` |
| `mmap` | `1`=mmap 权重 | `1` |
| `llama_log_verbosity` | llama-server 日志级别 0–5（2=仅警告/错误） | `3` |
| `memory_limit_mb` | Go 软内存上限（控制面） | `180` |
| `max_procs` | GOMAXPROCS；`0`=全部核心 | `0` |
| `max_open_files` | RLIMIT_NOFILE 目标 | `1048576` |
| `restore` | 启动时恢复常驻模型 | `1` |
| `run_as` | systemd `User=`；安装包用 `maio` | 空=root |

未知键、类型错误、非法枚举都会带行号报错。`maio config` 可查看/修改/校验。

每个模型一份：`<models_root>/<name>/maio.toml`，三个字段：

```toml
name   = "qwen3.5-9b"
format = "gguf"
path   = "models/Qwen3.5-9B-Q4_K_M.gguf"
```

## 命令

```text
# 模型
sudo maio pull|install <name> [root] [--url URL|PATH] [--sha256 HEX]
maio list [--wide] [--json]
maio show <name> [--json]
maio cp <source> <destination>
maio rm <name> [-y]
maio run <name> [prompt] [--ctx N] [--parallel N] [--kv f16|q8_0|q4_0] [--force]

# 服务
maio serve [--host ADDR] [--port N] [--api-key KEY] [--no-auth]
maio ps [--json]
maio load <name>                     # 常驻，重启后自动恢复
maio stop <name> | --all
maio status [--json] [--user]
maio stats [--json]
maio service <start|stop|restart|status|enable|disable|logs> [--user]
maio restart [--user]
sudo maio boot 1|0 [--user]

# 安全与配置
maio token [show] [--show] [--json]
maio token rotate|set|clear
maio config [show] [--json]
maio config path|get|set|edit|keys
maio doctor [--json]
maio logs [--tail N] [--json] [--path]
maio version [--json]
maio completion bash|zsh|fish
maio upgrade <binary> [--no-restart]
maio rollback [--no-restart]
```

### 退出码

| 码 | 含义 |
| --- | --- |
| `0` | 成功 |
| `2` | 用法 / 参数错误 |
| `3` | 找不到 |
| `4` | 冲突 |
| `5` | 权限 / I/O |
| `6` | 下载失败 |
| `7` | 校验失败 |
| `8` | 引擎失败 |
| `9` | 硬件 / doctor 失败 |
| `10` | 配置错误 |
| `11` | 服务 / systemd 错误 |
| `12` | 未授权 |
| `13` | 限流 |
| `14` | 队列满/忙 |
| `15` | 超时 |
| `130` | 中断 |

每条错误打印稳定错误码（如 `MAIO-E006`）和 `hint:`。

## 服务器行为

### 并发、排队与背压

一次推理请求先经过 `max_concurrency` 计数信号量；名额满后进入 FIFO 等待队列
（最多 `max_queue` 个）。队列也满时立即返回：

```json
{"error":"the inference queue is full","code":"MAIO-E015","hint":"retry shortly, ..."}
```

等待超过 `queue_timeout` 返回 503；客户端断开时等待立即取消，不会占住槽位。
首字生成后的流式响应逐块 flush，不把整段回复缓存在内存里。

### 显存与多槽位

`parallel` 个槽位共享同一份权重，但 **KV 缓存按槽位成倍增长**。启动前按
“权重 + KV×槽位 + 开销”做预算；放不下时自动降槽位（并告警），仍放不下才拒绝。
`kv_type = "q8_0"` 可把 KV 约减半，代价是少量质量。`maio doctor` 会显示每个模型的预算。

### 常驻与空闲卸载

- `maio load <name>`：加载并 Pin，写入 `<state_dir>/run/resident.json`；
  服务重启后自动恢复（`restore = 1`）。
- 普通请求加载的模型：空闲 `idle_unload` 秒后卸载；常驻模型不卸载。
- 请求体里的 `keep_alive` 也支持：`-1`/`"forever"` Pin，`0` 用完即卸，正数延长空闲时间。
- supervisor 每 10 秒检查常驻模型健康；崩溃后按 2s→60s 指数退避自动重启。

### 鉴权与限流

```bash
maio token --show          # 显示完整 Key（默认打码）
maio token rotate          # 生成新 Key（0600）
maio token set <key>
maio token clear
```

客户端：

```bash
curl -H "Authorization: Bearer $KEY" http://127.0.0.1:11434/api/tags
# 也接受 X-API-Key: $KEY
```

`auth = "auto"` 时：回环监听可不鉴权；非回环监听首次启动自动生成 Key 并落盘。
`auth = "off"` 只允许回环地址，除非显式 `allow_insecure = 1`。

### 内存目标

目标是**控制面**常驻内存 < 200 MiB，而不是整个 llama.cpp 推理内存：

- Go 软上限 `memory_limit_mb = 180`；
- 请求体流式处理、流式响应、固定大小 buffer pool；
- `maio stats` 直接显示 RSS；`make soak` 会断言峰值 < 200 MiB。

模型权重、KV 缓存和 CUDA 上下文属于 `llama-server` 子进程，单独计入显存/系统内存。

## HTTP API

默认 `http://127.0.0.1:11434`。

### Ollama 兼容

- `GET /api/version`
- `GET /api/tags`
- `GET /api/ps`
- `POST /api/show`
- `POST /api/generate`（NDJSON 流）
- `POST /api/chat`（NDJSON 流）
- `POST /api/embed`、`POST /api/embeddings`
- `POST /api/load`、`POST|DELETE /api/unload`（`{"all":true}` 卸载全部）

### OpenAI 兼容

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/completions`
- `POST /v1/embeddings`

OpenAI 请求按原样透传给 llama-server，保留全部参数与流式行为。

### 运维

- `GET /healthz`（无需鉴权）
- `GET /readyz`
- `GET /metrics`（Prometheus；需鉴权）
- `GET /api/stats`（JSON；需鉴权）

错误统一为 JSON：`error`、`code`、`message`、`hint`。

## 安全与 systemd

安装包生成的 unit 包含：

```ini
Type=notify
NotifyAccess=main
Restart=always
WatchdogSec=30
KillMode=mixed
OOMPolicy=stop
User=maio
Group=maio
SupplementaryGroups=video render
CapabilityBoundingSet=
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
SystemCallArchitectures=native
ReadWritePaths=-/var/lib/maio
ReadOnlyPaths=-/var/lib/maio/models
```

自定义模型目录在 `/home` 下时，若 `run_as = "maio"`，需确保该目录对 `maio` 用户可读
（或把 `run_as` 设为空以 root 运行）。

## 运维

```bash
maio status                 # 服务 + 健康 + 内存 + 鉴权
maio stats                  # 请求/队列/重启/token/RSS/FD/goroutine
maio doctor                 # 配置、硬件、引擎、unit 加固、常驻与预算
maio service logs --tail 200
maio logs --tail 50
maio upgrade ./bin/maio     # 原子替换 + 备份 + 重启，失败自动回滚
maio rollback
```

日志：

- 审计：`<state_dir>/log/maio.log`（JSON Lines，10MB 轮转）
- 引擎：`<state_dir>/log/<model>.log`（8MB 轮转）

## 压测

```bash
make soak                                  # 默认 60s / 64 并发
DURATION=3600 CONCURRENCY=128 make soak    # 1 小时
DURATION=86400 CONCURRENCY=128 make soak   # 24 小时
```

`scripts/soak.sh` 用 mock `llama-server` 驱动完整 HTTP 路径，断言：

- 0 崩溃、0 panic、0 传输错误；
- 控制面峰值 RSS < 200 MiB；
- SIGTERM 后 60 秒内退出且无残留引擎。

短跑实测（本机，32 并发 10 秒）：8427 请求全部 200，峰值 RSS 21.8 MiB，
0 panic。24h 级别请用上面的 `DURATION=86400` 在目标机跑。

## 目录结构

```text
main.go
internal/cli        命令入口、全部子命令、API 客户端
internal/config     maio.conf / maio.toml、校验与用户级回退
internal/download   断点续传、校验、磁盘检查、本地复制
internal/engine     llama-server 进程管理、显存预算、多槽位、日志轮转
internal/errs       稳定错误码、退出码与修复建议
internal/gguf       GGUF 元数据解析
internal/hardware   CPU/内存/磁盘/NVIDIA GPU 探测
internal/index      名字 -> 直链索引
internal/logging    JSONL 审计日志
internal/naming     模型名规范化
internal/registry   模型目录扫描
internal/server     HTTP 服务、鉴权、限流、队列、指标、supervisor
internal/auth       API Key 解析/生成/常量时间比较
internal/metrics    Prometheus 文本 + JSON 指标
internal/queue      有界并发/等待队列
internal/ratelimit  每 IP 令牌桶
internal/resident   常驻模型持久化
internal/sdnotify   systemd READY/WATCHDOG/STOPPING
internal/lockfile   单实例 flock
internal/tune       内存上限、GOMAXPROCS、RLIMIT_NOFILE
internal/systemd    系统级/用户级 unit 与生命周期
internal/toml       带行号的最小 TOML 读取器
internal/vram       显存预算（槽位/KV 类型）
deploy/             安装包、apt 仓库、加固 unit、llama 运行时包
scripts/            smoke、soak、mock 引擎、llama 构建/裁剪
docs/               路线图、V3 验证记录
```

## 许可证

MIT，见 [LICENSE](LICENSE)。llama.cpp 运行时遵循其上游许可证。
