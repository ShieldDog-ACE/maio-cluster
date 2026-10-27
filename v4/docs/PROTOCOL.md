# V4 集群协议

版本：`Protocol = 1`。不同主版本不能加入同一集群（join 时拒绝）。

传输：HTTP/1.1 + JSON；作业事件用 SSE；RPC 是 llama.cpp 自己的二进制 TCP
协议；反向隧道是单行 JSON 握手后裸 TCP 转发。

## 1. 认证

除 `GET /v1/health`、`GET /v1/info` 外，所有请求都要：

```
Authorization: Bearer <cluster.token>
```

token 为 32 字节随机数的 base64url（43 字符），恒定时间比较。加入失败返回
401。发现信标不包含 token，只包含 HMAC。

token **没有过期时间**：`maio init --token <value>` 或 `maio token set <value>`
可写入固定长期值，只有 `maio token rotate` 才会替换。允许 32..128 个
`A-Z a-z 0-9 - _` 字符。

## 2. 端点

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/v1/health` | 存活；`{ok,version,protocol,time}` |
| GET | `/v1/info` | 集群公开信息（无 token） |
| POST | `/v1/join` | 节点加入 |
| POST | `/v1/heartbeat` | 心跳/模型/RPC/链路更新 |
| GET | `/v1/nodes` | 成员列表 |
| POST | `/v1/nodes/{id}/drain` | 标记不再接新作业 |
| POST | `/v1/agent/commands/next` | 长轮询一条命令（10 秒） |
| POST | `/v1/agent/commands/result` | 命令结果 |
| GET | `/v1/models` | 所有成员广告的模型 |
| GET | `/v1/models/{name}/file` | 下载协调者本地 GGUF（支持 Range） |
| POST | `/v1/models/distribute` | 把模型推到 worker |
| GET/POST | `/v1/plan` | 预览放置 |
| POST | `/v1/jobs` | 提交作业 |
| GET | `/v1/jobs` | 作业列表 |
| GET | `/v1/jobs/{id}` | 作业详情 |
| POST | `/v1/jobs/{id}/cancel` | 取消 |
| GET | `/v1/jobs/{id}/events` | SSE 事件 |

`/v1/agent/*` 需要额外头 `X-Maio-Node: node_...`；服务端用该头把结果投递给
正确的命令等待者。

## 3. 加入

```http
POST /v1/join
Authorization: Bearer <token>
Content-Type: application/json

{
  "node": {
    "id": "",                      // 首次为空；重连时带上次的 node_...
    "name": "worker-1",
    "role": "worker",              // 仅 loopback 可声明 coordinator
    "os": "linux", "arch": "amd64",
    "version": "4.0.0", "protocol": 1,
    "host": "192.168.1.22",
    "hardware": {
      "cpu_model": "...", "cpu_cores": 12,
      "mem_total": 34359738368, "mem_free": 30064771072,
      "gpus": [{"index":0,"name":"RTX 4090","backend":"cuda",
                "vram_total":25769803776,"vram_free":25000000000}]
    },
    "models": [{"name":"qwen2.5-0.5b","size_bytes":491400032,"layers":24}],
    "rpc": {"available": true, "binary": "/usr/lib/maio/bin/ggml-rpc-server",
            "devices": 1},
    "compute": true
  }
}
```

```json
{
  "ok": true,
  "node_id": "node_8e90773e304cdee70643669d",
  "cluster": {
    "id": "mc_96f429b617f1df88881b4abc",
    "name": "maio", "version": "4.0.0", "protocol": 1,
    "port": 47800, "nodes": 2, "tls": false
  },
  "heartbeat_ms": 3000
}
```

规则：

- `name` 1..64 字符；`protocol` 必须等于 1。
- 节点数达到 `cluster.max_nodes` 时拒绝新节点（已知 ID 的重连除外）。
- `role=coordinator` 只在来源 IP 为 loopback 时接受；协调者同时受
  `cluster.compute` 控制是否参与分片。
- 服务端把非私有 `host` 替换为 TCP 来源 IP，防止声明任意地址。

## 4. 心跳

每 `agent.heartbeat_seconds` 秒：

```json
{
  "node_id": "node_...",
  "state": "online",
  "load": 0.25,
  "models": [ ... ],
  "rpc": {"available": true, "devices": 1},
  "host": "192.168.1.22",
  "links": [{"interface":"enp3s0","ip":"192.168.1.22","prefix":24,"kind":"ethernet"}]
}
```

15 秒未收到心跳 → offline；再次心跳会恢复 online。`drain` 状态不会被心跳
覆盖。

## 5. 命令通道

worker 循环：

```http
POST /v1/agent/commands/next
X-Maio-Node: node_...
```

- 有命令：`200 {"id":"cmd_...","kind":"rpc.start","args":{...},"timeout_seconds":30}`
- 10 秒内没有：`204 No Content`
- 非成员：`404`

执行后：

```http
POST /v1/agent/commands/result
X-Maio-Node: node_...
{"id":"cmd_...","ok":true,"data":{"port":"50100","endpoint":"192.168.1.22:50100"}}
```

### 命令类型

| kind | args | data |
| --- | --- | --- |
| `ping` | — | `pong=1` |
| `rpc.start` | `port`（0=自动） | `port,endpoint,pid,devices` |
| `rpc.stop` | `port` 或空=全部 | — |
| `tunnel.open` | `target_port` | `endpoint=127.0.0.1:<本地>,port` |
| `tunnel.close` | `target_port` | — |
| `engine.start` | `model,ctx,threads` | `port,endpoint,pid,model` |
| `engine.stop` | `port` 或 `model` 或空 | — |
| `model.prepare` | `name,url,sha256` | `path,name,size` |
| `model.remove` | `name` | — |

命令一次只派发一条；agent 串行执行，结果 15 秒内回传。

## 6. 反向隧道

协调者监听 `cluster.port+2`。worker 在收到 `tunnel.open` 后：

```
TCP connect coordinator:tunnel_port
→ {"v":1,"mux":true,"node_id":"node_...","token":"...","target":50100}\n
← {"ok":true,"port":53421}\n
→ 分帧多路复用（见下）
```

`53421` 是协调者 127.0.0.1 上的监听端口；llama.cpp 可以像连真实 RPC 一样
向它打开任意多条 TCP 连接。

### 分帧

每帧 9 字节头 + 负载（大端）：

| 字段 | 长度 | 说明 |
| --- | --- | --- |
| type | 1 | 1=OPEN, 2=DATA, 3=CLOSE |
| stream | 4 | 逻辑流 ID |
| length | 4 | 负载长度（DATA 1..1 MiB；控制帧为 0） |

- 协调者为每条本地 TCP 连接分配一个 stream，发 `OPEN`；
- worker 收到 OPEN 后连接本机 `target` 端口，之后双向转发 `DATA`；
- 任一端关闭时发 `CLOSE`，另一端关闭对应 socket；后续同端口的连接重新
  `OPEN`，worker 重新 dial。
- 一条控制连接承载多个 stream，解决 llama.cpp RPC 每设备多条 TCP 连接的
  问题；旧的单连接转发会卡在 device-memory 查询阶段。

协调者校验：token、节点在成员表、目标端口在 `AllowTunnel` 白名单（只在
放置完成后写入）。白名单避免把隧道变成任意端口代理。

## 7. 发现信标

UDP 47801（`port+1`），JSON：

```json
{"v":1,"p":1,"c":"mc_...","n":"maio","h":"192.168.1.10","a":47800,"t":1793109000,"s":"<hmac>"}
```

HMAC-SHA256(token, `v|p|cluster|name|host|api_port|timestamp`)，base64url。
接收方拒绝超过 15 秒、协议不符、HMAC 不符的包。token 不出网。

## 8. 配对码

```
MAIO1-<base64url(JSON)>
```

JSON：`{"c":clusterID,"n":name,"h":host,"p":port,"t":token,"v":1}`。也接受
`maio://join?cluster=...&name=...&port=...&token=...`。配对码等同密码。

## 9. 作业与 SSE

```http
POST /v1/jobs
{"model":"qwen2.5-0.5b","prompt":"hello","mode":"auto","max_tokens":256}
→ 202
{"id":"job_...","model":"...","mode":"shard","state":"queued",...}
```

`mode`：`auto|shard|route|local`。`auto`：≥2 个可用 RPC 节点 → shard；否则
有远端持有模型 → route；否则 local。

SSE 事件：

```
data: {"type":"state","state":"running","job":{...}}

data: {"type":"plan","job":{"plan":{"placements":[...]}}}

data: {"type":"token","text":"你","tokens":1}

data: {"type":"done","state":"done","job":{...}}
```

`failed`/`canceled` 也会发 `done`，带 `error` 或状态。慢客户端会被丢弃事件，
不阻塞推理；`GET /v1/jobs/{id}` 始终能取 `output`。

## 10. 错误

```json
{"code":401,"error":"unauthorized","hint":"paste the current code ..."}
```

进程退出码：

| 码 | 含义 |
| --- | --- |
| 2 | 用法/参数 |
| 4 | 不存在 |
| 5 | 权限 |
| 6 | 网络 |
| 7 | 完整性（GGUF/SHA） |
| 8 | 冲突（已存在） |
| 9 | 硬件/容量 |
| 10 | 配置 |
| 11 | 不可用 |
| 13 | 认证 |
| 14 | 超时 |
| 130 | 取消 |

错误文本前缀 `MAIO-E000`..`E012`，带可执行的 hint。

## 11. 版本与兼容

- 协议主版本 1；新增可选 JSON 字段不升版本。
- 命令 `kind` 是开放集合：agent 遇到未知 kind 返回错误，不崩溃。
- 集群 ID 不匹配的节点可以加入（用于灾后重建），但发现信标 HMAC 要求 token
  一致；生产环境应保证 `cluster.id` 与 token 一起分发。
