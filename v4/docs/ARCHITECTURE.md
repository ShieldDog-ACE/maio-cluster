# V4 架构

## 1. 进程与角色

```
                    ┌─────────────────────────────┐
                    │ coordinator (cluster up)    │
                    │  HTTP API :47800            │
                    │  UDP beacon  :47801         │
                    │  reverse tunnel :47802      │
                    │  membership / planner / jobs│
                    │  model store  / llama front │
                    └──────────┬──────────────────┘
                               │ HTTP (token)
             ┌─────────────────┼─────────────────┐
             │                 │                 │
      ┌──────▼──────┐   ┌──────▼──────┐   ┌──────▼──────┐
      │ agent A     │   │ agent B     │   │ agent C     │
      │ heartbeat   │   │ heartbeat   │   │ heartbeat   │
      │ command poll│   │ command poll│   │ command poll│
      │ rpc-server  │   │ rpc-server  │   │ rpc-server  │
      └─────────────┘   └─────────────┘   └─────────────┘
```

- **coordinator** 只绑定一个 API 端口；所有成员通过出站 HTTP 与它通信。
- **agent** 没有入站服务。它长轮询 `/v1/agent/commands/next`，因此能穿过
  NAT、Windows 防火墙默认策略和 USB 反向端口，不需要在 worker 上开端口。
- **ggml-rpc-server** 由 agent 按作业启动，一个作业一个实例，结束即回收。
- **前端 llama-server** 由协调者启动。它加载 GGUF，把张量/计算分发到 RPC
  设备，自身可以不做矩阵乘（`--device RPC0,RPC1...`）。

三个平台只实现 `platform.Adapter`：

```go
type Adapter interface {
    Name() string
    Paths(system bool) config.Paths
    ServiceInstall(ServiceSpec) error
    ServiceUninstall(system bool) error
    ServiceStatus(system bool) (string, error)
    InstallRoot(system bool) string
}
```

## 2. 发现与加入

1. 协调者每 2 秒向每个网卡的定向广播地址 + 255.255.255.255 发 UDP 信标。
2. 信标字段：协议版本、集群 ID/名称、协调者地址和 API 端口、时间戳、HMAC。
   HMAC 密钥是集群 token；token 本身不出网。
3. worker 依次尝试：配置里的 `cluster.seeds` → 上次成功保存的 `link.json` →
   UDP 发现。任一 `GET /v1/health` 成功即采用。
4. `POST /v1/join` 带上节点信息与 Bearer token。协调者校验协议版本、节点数
   上限、节点名，分配 `node_<hex>`，把来源 IP 作为私有地址的回退。
5. worker 保存 `node.json` 与 `link.json`，开始心跳（默认 3 秒）和命令长轮询。

协调者重启后成员表是空的，但 worker 会在 1-3 秒内用相同节点 ID 重新 join。
worker 重启后从 `node.json` 恢复 ID，避免仪表盘出现幽灵节点。

## 3. 放置算法

输入：`ModelMeta{layers, size, context}`、所有节点、`PlanOptions{RequireRPC}`。

1. 过滤：非 online、`compute=false`、没有 RPC、可用容量 < 512 MiB。
2. 容量：
   - 有 GPU：`sum(free VRAM) * 0.92`；
   - 无 GPU：`free RAM * 0.60`（留 40% 给系统/前端/KV）。
3. 按容量降序取最多 8 台（不超过层数）。
4. 每台份额 `floor(layers * capacity / total)`，至少 1；舍入误差补给最大节点。
5. 总容量 `* 0.8` 仍小于模型文件大小时直接失败，提示加节点或换量化。
6. 输出 `-ts` 权重和预计层数。实际层边界由 llama.cpp 在张量分割时决定，
   `~12 layers` 是按权重折算的估计。

一台机器时仍可规划，但会给出“没有跨机分片”警告；`auto` 模式此时会选择
本地推理而不是 RPC。

## 4. 作业生命周期

```
queued → running → done
                 ├→ failed
                 └→ canceled
```

`POST /v1/jobs` 立即返回 202。runner 在 goroutine 中执行，状态和 token 通过
SSE (`/v1/jobs/{id}/events`) 推送；`Job.Output` 保留最近 256 KiB 供轮询查看。
取消会 cancel 作业 context，关闭 HTTP 流，触发清理。

### shard 执行

1. `BuildPlan` → `Plan`，写入作业（前端可看到 placement）。
2. 对每个 placement：
   - `rpc.start {port:0}`：agent 选空闲端口，启动 `ggml-rpc-server -H 0.0.0.0
     -p P -c`，等 TCP 可连接后返回 `endpoint`/`port`；
   - 协调者 700 ms 内直连 endpoint；失败则 `AllowTunnel(node, port)` +
     `tunnel.open`，worker 主动连协调者 47802，拿到协调者侧 127.0.0.1 端口；
     该端口按 9 字节帧头做多路复用，llama.cpp 的每条 RPC 连接对应一个 stream。
3. 启动前端：
   ```
   llama-server --model <gguf> --host 127.0.0.1 --port <free> \
     --rpc ep1,ep2 --device RPC0,RPC1 -ts 12,12 -ngl 99 -c <ctx> --no-webui
   ```
4. 轮询 `/health` 直到 200，超时 15 分钟；失败时日志尾部写进错误。
5. `POST /completion {stream:true}`，逐块 emit `token` 事件。
6. defer 清理：停止前端 → 对每个节点 `rpc.stop` / `tunnel.close`。

并发作业各自申请端口（`port:0`），不会共享或误杀彼此的 RPC 进程。

### route 执行

选择持有模型、online、容量最大的非本机节点；`engine.start` 在该节点启动
绑定 127.0.0.1 的普通 llama-server，返回端口；协调者通过反向隧道代理
`/completion`。响应流结束后 `engine.stop`。这条路径不要求 RPC 二进制。

### local 执行

协调者直接启动本地 llama-server（`-ngl` 来自配置），不经 RPC。适合单机或
`auto` 找不到第二台 RPC 节点时。

## 5. 故障与恢复

| 故障 | 行为 |
| --- | --- |
| worker 心跳丢失 | 9 秒标记 stale，15 秒 offline；挂起命令立即失败 |
| worker 10 分钟不回来 | 从成员表删除，关闭命令通道 |
| 命令超时 | 从 pending 删除，返回 `MAIO-E011`；作业清理已启动的 RPC |
| 直连 RPC 端口不通 | 自动反向多路复用隧道；握手/分帧失败则作业失败并清理 |
| 前端启动失败 | 读取 `llama-<job>.log` 尾部，停止全部 RPC |
| 协调者重启 | API 重新监听；worker 自动重 join；运行中的作业丢失（V4 不持久化作业） |
| 本机服务 | 协调者/agent 与本地 `serve` 无关；断网只影响集群作业，不影响本机模型文件 |

成员表、作业表只在内存。持久化的只有配置、集群身份、节点 ID、模型文件。

## 6. 目录与端口

| 作用 | Linux (system) | Windows (system) | macOS (system) |
| --- | --- | --- | --- |
| 配置 | `/etc/maio/cluster.toml` | `%ProgramData%\maio\cluster.toml` | `/Library/Application Support/maio/cluster.toml` |
| 模型 | `/var/lib/maio/models` | `%ProgramData%\maio\models` | `/Library/Application Support/maio/models` |
| 日志 | `/var/log/maio` | `%ProgramData%\maio\logs` | `/Library/Logs/maio` |
| 服务 | systemd `maio.service` | 计划任务 `maio` | LaunchDaemon `dev.maio.cluster` |

| 端口 | 协议 | 说明 |
| --- | --- | --- |
| 47800 | TCP | 协调者 API |
| 47801 | UDP | 发现信标 |
| 47802 | TCP | 反向隧道 |
| 50100+ | TCP | worker 的 ggml-rpc-server |

`cluster.port` 改变时，发现端口 = port+1、隧道端口 = port+2；也可用
`MAIO_DISCOVERY_PORT` 覆盖发现端口（用于同机测试）。

## 7. 代码地图

```
core/
  cluster/     coordinator, API, client, planner, jobs, tunnel, token
  agent/       join/heartbeat/poll, rpc/engine supervisor, tunnel client
  inference/   llama-server lifecycle, RPC shard runner, route/local runner
  discovery/   signed UDP beacon
  tunnelproto/ framed multiplexing (OPEN/DATA/CLOSE + stream)
  model/       maio.toml + GGUF store, resume download, SHA-256
  hardware/    Linux /proc, Windows GlobalMemoryStatusEx, macOS sysctl/Metal
  netinfo/     link classification, best advertise address, broadcasts
  config/      TOML load/save/validate, platform paths
  cli/         commands shared by the three mains
linux|windows|mac/
  cmd/maio     thin main wiring the adapter
  platform/    paths + service manager
  packaging/   deb, pkg, ps1, wxs-free zip
```

## 8. 为什么不用请求级“分布式”

“每台机器负责一部分硬件计算”要求同一模型的计算图被拆开。V4 用 llama.cpp
上游的 RPC 后端做层/张量级切分，而不是把不同 prompt 分给不同机器——后者
只是副本负载均衡，单次请求仍只用到一台机器。`route` 模式保留副本路由作为
降级路径，但 `shard` 才是 V4 的主路径。
