# V3 验证记录

本文件记录 V3（服务器级）在本机上的实际验证结果。所有命令均可在仓库根目录复现。

## 环境

| 项 | 值 |
| --- | --- |
| 日期 | 2026-10-27 |
| 系统 | Ubuntu 26.04 LTS (Resolute Raccoon), kernel 7.0.0-38-generic |
| 架构 | x86-64 |
| CPU | 12 逻辑核 |
| 内存 | 31 GiB |
| GPU | 2 × Tesla V100-SXM2-16GB, driver 580.178.04 |
| Go | go1.27.1 linux/amd64 |
| llama-server | 0.7.0-dev (build 0), CUDA sm_70 |

## 1. 单元测试与竞态检测

```bash
make test       # 全部包通过
make race       # go test -race ./... 全部通过
make vet        # 无输出
```

覆盖：auth、config、engine、gguf、hardware、lockfile、logging、metrics、naming、
queue、ratelimit、registry、resident、sdnotify、server、systemd、toml、vram。

## 2. 端到端冒烟

```bash
make smoke
```

结果：全部检查通过，包括：

- 安装/本地登记/删除/`cp`；
- 错误码 2/3/4/5/6/7/10；
- 服务 API：401（无 Key）、`/api/version`、`/api/tags`、`/healthz`、`/metrics`；
- doctor（无 llama-server 时按预期返回 9）；
- V3 CLI：`config`、`token`、`completion`、`status`、`stats`、`ps`。

## 3. 并发压测与 200 MiB 内存目标

```bash
DURATION=10 CONCURRENCY=32 make soak
```

实测：

| 指标 | 结果 |
| --- | --- |
| 请求数 | 8427 / 10 秒 |
| 状态码 | 全部 200 |
| 延迟 | p50 27.2 ms, p95 68.7 ms, p99 75.8 ms |
| 控制面峰值 RSS | **21.8 MiB**（目标 < 200 MiB） |
| 峰值 FD | 190 |
| 峰值 goroutine | 241 |
| panic | 0 |
| SIGTERM 退出 | 60 秒内，退出码 0 |
| 残留引擎 | 无 |

补充：`DURATION=30 CONCURRENCY=128` 复测——27484 请求全部 200，峰值 RSS **27.9 MiB**，
p95 196 ms，0 panic。

说明：soak 使用 `scripts/mock-llama-server.sh`，不消耗 GPU，但覆盖真实 HTTP 服务、
鉴权、队列、流式响应、指标和优雅关闭路径。24 小时级别：
`DURATION=86400 CONCURRENCY=128 make soak`（本次未连续跑 24h，脚本已提供）。

## 4. 鉴权

`auth = "auto"`, `host = "0.0.0.0`，首次启动：

- 自动生成 `maio_...` 256 位 Key，文件权限 `0600`；
- 无 Key 请求 `/api/tags` → `401`；
- `Authorization: Bearer <key>` → `200`；
- `maio token --show` 可读取完整 Key；默认输出打码。

## 5. 限流

配置 `rate_limit = 2`, `rate_burst = 2`：

```text
1:200 2:200 3:429 4:429
HTTP/1.1 429 Too Many Requests
Retry-After: 29
```

## 6. 请求体上限

`max_body_mb = 1`，发送 2 MiB 请求体：

```text
status=413
{"code":"MAIO-E001","error":"request body exceeds the 1 MiB limit",
 "hint":"send a smaller request, or raise max_body_mb in maio.conf", ...}
```

## 7. 有界队列与背压

`max_concurrency = 2`, `max_queue = 2`, mock 延迟 800ms，同时发 6 个请求：

```text
200 200 429 200 429 200
```

4 个被受理（2 个立即执行、2 个排队），2 个队列满立即 429；总耗时约 2.4 秒。
审计日志出现 `http.queued` 与 `the inference queue is full`。

## 8. 单实例锁

同一 `state_dir` 启动第二个 `maio serve`：

```text
exit=4
maio: MAIO-E003: another maio service is already running (pid ..., lock .../run/maio.lock)
```

## 9. 常驻模型恢复

- `maio load tiny` → `resident.json` 写入 `tiny`，`/api/ps` 显示 `resident=true`；
- SIGTERM 关闭服务，确认引擎进程全部退出；
- 重新启动服务，`/api/ps` 自动恢复 `tiny`（新 PID，`resident=true`）。

## 10. 引擎崩溃自愈

- 加载并 Pin `tiny`；
- `kill -9` mock 引擎 PID；
- supervisor 在 16.6 秒内拉起新 PID；
- `/api/stats`：`engine_restarts=1`, `engine_restart_failures=0`。

## 11. 真实 GPU 推理

使用用户现有 `llama-server` 与 Qwen3.5-9B：

```bash
MAIO_CONF=/home/fuka/.config/maio/maio.conf \
  ./bin/maio run qwen3.5-9b "Reply with exactly: hello from maio v3" \
  --ctx 4096 --parallel 1
```

输出：`hello from maio v3`，退出码 0。

## 12. 轻量化 llama.cpp

裁剪现有 CUDA 构建：

```bash
LLAMA_BUILD=/home/fuka/AI/qwen3.8-27b/runtime/llama.cpp-master/build \
LLAMA_LIB=.../cuda-12.9/lib64 \
OUT=/tmp/maio-llama-bundle bash scripts/bundle-llama.sh
```

结果：

- 仅 1 个 `llama-server` + 8 个必要 `.so`（含符号剥离）；
- 体积 81 MiB → **72 MiB**；
- `llama-server --version` 从裁剪目录启动成功；
- 用该裁剪运行时跑 Qwen3.5-9B：`bundled llama works`，退出码 0。

源码最小构建脚本 `scripts/build-llama-minimal.sh` 只构建 `llama-server` 目标，
关闭非 Linux 后端、测试、示例、Web UI，并将 llama/ggml 静态链接。
本机 CPU 最小构建实测：单文件 **14 MiB**，`llama-server --version` 正常，
`ldd` 仅依赖 libc/libstdc++/libgomp/libm/libgcc，没有 libllama/libggml。

Ubuntu 26.04 自带 GCC 15 与 CUDA 12.9 host compiler 不兼容（即使加
`-allow-unsupported-compiler`，libstdc++ 15 头文件仍会报错）。脚本会先检测并给出三种选择：
安装 `g++-14`、`CUDA=0` 或复用已有 CUDA 构建（本机 CUDA 推理采用第三种裁剪运行时）。

## 13. systemd unit

使用 `systemd-analyze verify` 验证生成的 unit（可执行文件换为 `/bin/true` 以避免路径检查）：

```text
verify_exit=0
```

unit 包含 `Type=notify`、`WatchdogSec=30`、`Restart=always`、`User=maio`、
`ProtectSystem=strict`、`CapabilityBoundingSet=`、`ReadWritePaths` 等。

## 14. 升级与回滚

```bash
cp bin/maio /tmp/maio-candidate
maio upgrade /tmp/maio-candidate --force --no-restart
maio rollback --no-restart
```

结果：备份写入 `<state_dir>/upgrade/maio-3.0.0-<timestamp>`，替换成功；
rollback 恢复成功；`maio version` 仍为 3.0.0。

## 15. 默认用户级 / 系统级显式（3.1.0）

- `maio version` → `scope: user`，配置 `~/.config/maio/maio.conf`；
- `maio --system version` 非 root → exit 5，提示 `sudo maio --system version`；
- `maio --system --user ...` → exit 2（互斥）；
- `maio boot 1 --dry-run` → 用户 unit `~/.config/systemd/user/maio.service`，
  `systemctl --user enable --now`；
- 生成 unit 测试：系统 unit `ExecStart=... --system serve`，用户 unit
  `ExecStart=... --user serve`；用户 unit 不写 `User=`；
- 两套作用域使用不同配置/状态/端口，可同时存在。

说明：3.1.0 只改变作用域选择与默认值，队列、鉴权、内存和 supervisor 等核心
实现与 3.0.0 相同；上面的测试结果仍适用。

## 16. 已知限制

- 24h 连续压测脚本已提供，但本次未在本机连续运行 24 小时；
- CUDA 运行时（`libcudart`/`libcublas`/`libcublasLt`）不随轻量运行时打包，
  需要 NVIDIA 驱动/工具包提供；
- 主板“来电自启”需在 BIOS/UEFI 中设置，操作系统无法代改；
- 非 Linux 平台不在支持范围。
