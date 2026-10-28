# maio v4.1 · 分布式

> 基于 llama.cpp RPC 的局域网分布式推理集群。一台机器做协调与前端的张量调度，多台机器各承担一部分层（layer）的硬件计算；支持 Linux、Windows、macOS 三个独立产品目录。V4.1 增加包管理器（插件 `install` / 模型 `pull`）、开源市场与可选 Web 控制台，并下线 Android APK 方向。

V4 不是把 Windows/macOS 补丁塞进 V3。仓库里 **V3 原样保留在根目录**，V4 从 `v4/` 重新开始：

```
v4/
├── core/      共享集群内核（协议、发现、调度、代理、作业）
├── linux/     Linux 适配 + systemd + .deb/.tar.gz
├── windows/   Windows 适配 + 计划任务 + .zip/PowerShell 安装器
├── mac/       macOS 适配 + launchd + .pkg/.dmg/.tar.gz
├── web/       纯 TS/CSS 控制台（嵌入二进制，可选安装）
├── android/   V4 的只读 APK（V4.1 起不再演进）
├── docs/      架构、协议、链路、测试、迁移、市场
└── scripts/   端到端测试、llama.cpp RPC 构建
```

三个平台目录**只做各自的服务管理、路径、安装包与运行时查找**；集群协议和推理调度只维护一份，位于 `core/`。

## 1. V4 做了什么

| 能力 | 实现 |
| --- | --- |
| 节点发现 | UDP 广播信标 + HMAC 校验；也支持静态 seed、配对码 |
| 节点注册 | 256-bit 集群 token；协调者分配节点 ID；心跳淘汰 |
| 多机分片计算 | llama.cpp RPC：每个 worker 跑 `ggml-rpc-server`，前端 `llama-server` 用 `--rpc` + `--device RPC0..` + `-ts` 分配层 |
| 放置策略 | 按 GPU 空闲显存（无 GPU 时按空闲内存）比例分配层；容量不足直接拒绝 |
| 跨机路由 | `route` 模式把整次请求交给持有该模型的节点，适合没有 RPC 二进制或小模型 |
| 模型分发 | 协调者通过带鉴权的 HTTP 把 GGUF 推给 worker，支持断点续传与 SHA-256 |
| 断网/防火墙 | 直连失败自动走协调者反向 TCP 隧道（47802），worker 仍只需主动出站 |
| 四种链路 | 局域网 Wi-Fi、网线、USB 数据线/共享、手机热点——全部是 IP 链路，同一套协议 |
| 包管理器 | `maio install <plugin>` 从 GitHub 安装插件；`maio pull <model>` 从市场下载模型 |
| 开源市场 | `plugins.json` / `models.json` 放在 `ShieldDog-ACE/maio-market`，PR 合并即上架，零服务器成本 |
| Web 控制台 | 纯 TS/CSS，登录后看节点/对话/模型/插件/市场；资源嵌入二进制，`maio web install` 才落盘 |
| 快速查看 | V4 的 Android APK 保留但不演进；V4.1 以 Web 为正式终端 |

## 0. V4.1 新东西

```bash
maio market                       # 列出市场插件与模型
maio install hello                # 安装插件（GitHub 仓库）
maio pull qwen2.5-0.5b            # 从市场下载模型
maio web install                  # 安装可选 Web 控制台
maio web status                   # 看目录/地址
```

市场仓库：<https://github.com/ShieldDog-ACE/maio-market>。
插件开发者把仓库放在自己的 GitHub，给市场发一个只改 `plugins.json` 的 PR，
合并即上架；客户端始终从远程清单解析，不使用本地内置列表。
细节见 [`docs/MARKET.md`](docs/MARKET.md)。

Web 控制台默认不对外开放：`maio web install` 把嵌入资源落盘后，协调者的 `/`
才开始提供登录页；未安装时返回一段 CLI 提示。登录用集群 Token 换 HttpOnly Cookie。

## 1.1 查看本机身份

```bash
maio id
# 本机 ID:   mid_...
# 主机名:    ...
# 节点 ID:   node_...（未加入时显示提示）
# Token:     已注册的集群 token
# 配置文件:  ~/.config/maio/cluster.toml
```

`maio whoami` 是别名；`maio --json id` 输出机器可读字段。

## 2. 五分钟跑起来（Linux 两台）

### A 机（协调者）

```bash
sudo apt install ./dist/maio-cluster_4.1.0_amd64.deb
sudo maio --system token set '<你的长期token>'  # 固定长期 token（可选）
sudo maio --system cluster join-code     # 复制 MAIO1-... 配对码
sudo maio --system pull qwen2.5-0.5b      # 从 maio-market 下载
sudo maio --system web install            # 可选：Web 控制台
sudo maio --system cluster nodes
```

### B 机（worker）

```bash
sudo apt install ./dist/maio-cluster_4.1.0_amd64.deb
sudo maio --system cluster join 'MAIO1-...'
sudo systemctl restart maio
sudo maio --system service install --worker   # 只跑 agent（可选）
```

### 回到 A 机

```bash
sudo maio --system cluster nodes
# NAME        ROLE          STATE   RPC    GPU
# node-a      coordinator   online  true   cuda:...
# worker      worker        online  true   cuda:Tesla V100...

sudo maio --system cluster plan qwen2.5-0.5b
# node-a   50.0%  ~12 layers
# worker   50.0%  ~12 layers

sudo maio --system run qwen2.5-0.5b "只回答：分布式成功" --mode shard
```

看到两行 placement，说明 24 层被两台机器各分 12 层。前端只做 token 化/采样与张量调度，实际矩阵乘在各自的 `ggml-rpc-server` 上。

### Windows / macOS

```powershell
# Windows：解压 zip 后
.\install.ps1 -System
.\install.ps1 -System -Worker -JoinCode "MAIO1-..."
```

```bash
# macOS：pkg 或 tar
sudo installer -pkg maio-cluster-4.1.0.pkg -target /
sudo maio --system cluster join 'MAIO1-...'
```

详见 [`linux/README.md`](linux/README.md)、[`windows/README.md`](windows/README.md)、[`mac/README.md`](mac/README.md)。

## 3. Android APK（V4 保留，V4.1 不再演进）

已构建：`android/dist/maio-viewer.apk`（约 21 KiB，minSdk 24 / target 34）。

```bash
adb install -r android/dist/maio-viewer.apk
# USB 数据线：
maio link usb          # 对已授权设备执行 adb reverse 47800/47801/47802
# APK 里填 http://127.0.0.1:47800，或直接粘贴 MAIO1-... 配对码
```

APK 只显示节点、GPU/内存、模型、最近任务；不下载、不推理、不执行命令。没有 Android SDK 时按 [`android/README.md`](android/README.md) 下载 command-line tools 后 `make apk`。

## 4. 四种链路

| 链路 | 做法 | 关键点 |
| --- | --- | --- |
| 局域网 Wi-Fi | 两台机器连同一路由器 | 关掉 AP 隔离；放行 TCP 47800-47802 / UDP 47801 |
| 网线 | 直连或同一交换机 | 直连可用 169.254.x.x 链路本地地址，或手动同网段 /24 |
| USB 数据线 | 手机 USB 共享 → RNDIS；APK 用 `adb reverse` | `maio link usb` 自动配 APK 的 127.0.0.1 反向端口 |
| 热点 | 手机/随身路由开热点，所有节点接入 | 与 Wi-Fi 相同；部分热点默认客户端隔离，需要关闭 |

发现使用带 HMAC 的 UDP 广播（端口 47801）。防火墙拦住广播时，用配对码或 `cluster.seeds` 静态指定。四种链路都走同一套 HTTP/RPC，协议不分支。

## 5. 分布式怎么分工

1. 每个 worker 的 agent 上报硬件：CPU、内存、GPU 名称/显存、是否有 `ggml-rpc-server`。
2. 协调者按可用显存（GPU）或空闲内存的 60%（纯 CPU）计算权重。
3. `BuildPlan` 把 `block_count` 层按权重切分，保证每台至少 1 层，并把模型大小与总容量做 80% 余量校验。
4. 作业启动时：
   - 在每个选中节点上 `ggml-rpc-server`，拿到实际端口；
   - 协调者直连该端口；不通则让 worker 主动建立反向隧道，改用 `127.0.0.1:<本地端口>`；
   - 协调者启动 `llama-server --rpc ep1,ep2 --device RPC0,RPC1 -ts n1,n2 -ngl 99`；
   - 前端只用 RPC 设备，本地 GPU 不参与（协调者若也选中，则它自己的 rpc-server 作为一个设备）。
5. 作业结束/取消/失败都会回收 llama-server、rpc-server 和隧道；worker 掉线时作业失败并清理其余节点。

`route` 模式是降级路径：选一台已持有模型的节点启动普通 `llama-server`，通过隧道代理 HTTP 流式响应；适合 0.5B 之类小模型或没有 RPC 二进制的环境。

## 6. 构建

```bash
# 1) 构建带 RPC 的 llama.cpp（CPU/CUDA/Metal）
./scripts/build-rpc.sh --cuda          # 或 --cpu / --metal
# 2) 打 Linux deb + tar，并带上运行时
RPC_BIN=dist/runtime/bin/ggml-rpc-server \
LLAMA_BIN=dist/runtime/bin/llama-server \
  make package-linux
# 3) 其它平台
make windows            # dist/maio-windows-amd64.zip
make mac                # dist/maio-mac-{arm64,amd64}.tar.gz
make apk                # 需要 JAVA_HOME + ANDROID_SDK
make verify             # vet + race + 两节点 e2e
```

产物：

```
dist/maio-cluster_4.1.0_amd64.deb
dist/linux-amd64/{maio,bin/llama-server,bin/ggml-rpc-server,install.sh}
dist/maio-windows-amd64.zip
dist/maio-mac-arm64.tar.gz, dist/maio-mac-amd64.tar.gz
android/dist/maio-viewer.apk
```

macOS 的 `.pkg/.dmg` 必须在 macOS 上运行 `mac/packaging/build-pkg.sh`、`build-dmg.sh`（`pkgbuild`/`hdiutil` 不可跨平台）。Windows 的 `.zip` 已在 Linux 交叉编译验证；`install.ps1` 在 Windows PowerShell 5.1 下使用计划任务，避免 SCM 1053 问题。

## 7. 安全边界

- 集群 token 是唯一加入凭证，256-bit，恒定时间比较；配对码包含 token，按密码对待。
- token 默认**永不过期**；`maio token set <token>` 可写入固定值，`maio token rotate` 才会替换。
  长度 32..128，仅允许 URL-safe base64 字符（`A-Z a-z 0-9 - _`）。
- 发现信标只带 HMAC，不广播 token。
- 模型分发接口需要 token；worker 下载时带 Bearer。
- 反向隧道握手校验 token、节点 ID 和目标端口白名单，避免变成任意端口代理。
- llama.cpp RPC **本身没有认证**（上游明确标注 proof-of-concept）。V4 默认在受信任局域网使用；不要把 50100+ 暴露到公网。需要隔离时，让直连不可达，强制走隧道（RPC 端口只绑内网/本机）。
- 可选 TLS：`security.tls_cert` / `security.tls_key`；当前客户端未做证书固定，生产环境建议放在 WireGuard/Tailscale 之后。
- 系统服务用户：Linux 包使用 `maio` 用户；Windows 计划任务 SYSTEM；macOS LaunchDaemon root。

## 8. 验证方式

仓库只保留可重复的测试代码，不保存某台机器的测试日志。运行：

```bash
make verify          # go vet + race + 两节点成员/分层检查
```

带真实 RPC 运行时和模型时：

```bash
RPC_BIN=/path/ggml-rpc-server \
LLAMA_BIN=/path/llama-server \
E2E_MODEL=/path/model.gguf \
  ./scripts/e2e.sh
```

说明见 [测试说明](docs/TESTING.md)。环境限制：

- 两台物理机需要现场验收；同机多 agent 只能验证协议与调度。
- Windows/macOS 的真实 RPC 运行时需要各平台用 `-DGGML_RPC=ON` 构建。
- Android APK 需要真机或模拟器做界面验收。
- macOS `.pkg/.dmg` 需要 macOS 工具链。

## 9. 文档

- [架构](docs/ARCHITECTURE.md)
- [协议](docs/PROTOCOL.md)
- [四种链路](docs/TRANSPORTS.md)
- [测试说明](docs/TESTING.md)
- [包管理器与市场](docs/MARKET.md)
- [V3 → V4 迁移](docs/MIGRATION.md)

## 10. 与 V3 的关系

V3 在仓库根目录完全保留（Linux/apt/systemd/llama.cpp 本地推理）。V4 是独立产品线：`v4/` 自带 `go.mod`，可单独复制、构建、发布；不修改 V3 的配置、命令与安装包。V4 的 `maio.toml` 仍是三字段（`name/format/path`），模型目录可以复用 V3 的 `models_root`。

---

English: [README.en.md](README.en.md)
