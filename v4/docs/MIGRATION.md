# V3 → V4 迁移

V4 是独立产品线：`v4/` 有独立 `go.mod`、二进制、配置、安装包和服务单元。
V3 根目录保持原样，两者可以并存，但不要在同一端口同时启动。

## 1. 结构

| | V3 | V4 |
| --- | --- | --- |
| 代码 | 仓库根 `internal/` | `v4/core/` + 三个平台目录 |
| 目标系统 | Linux（apt/systemd） | Linux、Windows、macOS 独立安装包 |
| 二进制 | `bin/maio` | 每平台 `dist/.../maio[.exe]` |
| 配置 | `maio.conf` | `cluster.toml` |
| 服务 | systemd `maio.service` | systemd / 计划任务 / launchd |
| 版本 | 3.1.0 | 4.0.0 |

## 2. 命令对照

| 场景 | V3 | V4 |
| --- | --- | --- |
| 安装自身 | `sudo apt install maio-cluster` | 同（V4 独立 deb） |
| 模型安装 | `maio install <name>` | `maio model install <name> --url ...` |
| 列表 | `maio list` | `maio model list` |
| 查看 | `maio show` | `maio model show` |
| 删除 | `maio rm` | `maio model rm` |
| 本地推理 | `maio run <name> <prompt>` | `maio run <name> <prompt> --mode local` |
| 局域网服务 | `maio serve` | `maio cluster up`（协调者） |
| 状态 | `maio status` | `maio status` / `maio cluster status` |
| 日志 | `maio logs` | `maio logs` / `journalctl -u maio` |
| 自启 | `maio boot 1` | `maio service install` |
| 诊断 | `maio doctor` | `maio doctor`（增加链路/RPC/成员） |

V4 的一次性模型命令统一到 `model` 子命令，避免 `install` 既装自己又装模型
的歧义。

## 3. 配置迁移

V3 `maio.conf` 与 V4 `cluster.toml` 不是同一格式。V4 首次使用：

```bash
maio init            # 生成 cluster.toml + 集群 ID + token
maio config show
```

可迁移的值：

| V3 | V4 |
| --- | --- |
| `models_root` | `agent.models_dir` |
| `state_dir` | `agent.data_dir` |
| `host` | `cluster.bind` |
| `port` | `cluster.port` |
| `llama_bin` | `agent.llama_bin` / `llama.server_bin` |
| `ctx_size` | `llama.ctx_size` |

V3 的 `boot`、`mirror`、`llama_lib` 在 V4 没有对应：自启由平台服务管理；
镜像在 `model install --url` 显式给；运行库由二进制同目录自动补。

## 4. 模型复用

V4 的 `maio.toml` 仍是三字段（`name/format/path`），目录结构相同。可以把
`agent.models_dir` 指向 V3 的 `models_root`：

```toml
[agent]
models_dir = "/data/models"
```

V4 只读该目录并写自己的模型（如果有）。不要在 V3 与 V4 同时删同一个模型。

## 5. 端口与共存

V3 默认 `11434`，V4 默认 `47800`（发现 47801、隧道 47802），互不冲突，可以
同时运行。若都要监听 47800，改 V4 `cluster.port`；发现/隧道端口自动跟随
`+1/+2`。

## 6. 需要重做的部分

- **systemd 单元**：V4 的单元使用 `--system --config /etc/maio/cluster.toml
  cluster up`，与 V3 的 `serve` 不同；不要复用旧 unit。
- **API 路径**：V3 的 Ollama 兼容 `/api/*` 不在 V4；V4 的 `/v1/*` 是集群 API
  （不是 OpenAI 兼容）。V4 的推理请求请走 `POST /v1/jobs` 或 CLI `run`。
- **密钥**：V3 的 API key 与 V4 的集群 token 是两套东西，不能互换。
- **并发/限流/队列**：V3 的服务级保护没有整体搬到 V4；V4 作业是显式提交，
  并发由 agent 的 `max_jobs` 与 RPC 端口分配约束。V4.1 再加队列/配额。

## 7. 升级建议

1. 先在测试机装 V4 deb，`maio doctor` 全绿。
2. 用配对码加入第二台，`cluster nodes` 看到 `rpc=true`、`online`。
3. 把一个小模型 `model install` 到协调者，`cluster plan` 确认分层。
4. 再迁移大模型；`cluster distribute` 只对 `route` 模式需要。
5. 观察 `journalctl -u maio` 与 `llama-*.log`，确认 RPC 清理无残留。
6. 满意后再停 V3 服务；V3 文件不要删，保留回滚。

## 8. 不迁移的 V3 功能

V3 的 apt 本地仓库、`.deb` 的 `maio-llama` 子包、`upgrade/rollback`、Ollama
完整 shim、指标/soak 脚本仍是 V3 资产。V4 的发布物是三个平台包 + APK，
不包含这些 V3 专用组件（避免把 Linux 假设带入 Windows/macOS）。
