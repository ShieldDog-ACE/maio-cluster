# Changelog

## v4.1.1 — 2026-10-28

### Added

- **`maio update`**：从 GitHub Releases 检查/下载/校验/原子替换 CLI 自身；
  `--check` 只检查，`--force` 覆盖；dpkg 安装时优先提示 apt 升级。
- **apt 仓库**：`linux/packaging/build-apt-repo.sh` 生成 flat repo
  （Packages/Release/deb/setup.sh），发布为 `ShieldDog-ACE/maio-apt`；
  `sudo sh setup.sh` 即 `apt install maio-cluster`。
- **默认 apt 安装路径**：README/安装脚本把 apt 作为 Linux 首选安装方式；
  装完后模型/插件/CLI 更新统一走 maio（`pull` / `install` / `update`）。

### Changed

- 安装器默认版本 4.1.1；Linux 构建同时产出 tar 与 deb/apt repo。

## v4.1.0 — 2026-10-28

V4.1 在 V4 的集群内核上增加包管理器、开源市场与可选 Web 控制台。

### Added

- **包管理器**：`maio pull <model>` 从市场下载模型；`maio install <plugin>`
  从 GitHub 安装插件；`maio plugins list/update/remove`；`maio market`、
  `maio search`。
- **开源市场**：`ShieldDog-ACE/maio-market` 的 `plugins.json` / `models.json`
  是唯一数据库；PR 合并即上架，客户端每次从远程清单解析，不内置本地副本。
- **插件安装器**：GitHub tarball 下载、64/128 MiB 与 2000 文件上限、路径逃逸/
  链接拒绝、`plugin.json` 校验、原子落盘、`.maio-plugin.json` 元数据。
- **Web 控制台**：纯 TS/CSS（`v4/web`），登录、总览、对话、模型、插件、市场、
  设置；资源嵌入二进制，`maio web install` 才落盘并提供 `/`。
- **Web 会话**：集群 Token 换 HMAC 签名 HttpOnly Cookie（7 天），Cookie 同时
  解锁 `/v1/nodes`、`/v1/jobs` 等集群 API；CSP、SameSite=Strict。
- **市场/包管理 API**：`/v1/market/*`、`/v1/plugins*`、`/v1/models/local`、
  `/v1/models/pull`、`/v1/system`、`/v1/cluster/join-code`。
- **安装器**：Linux/macOS `--with-web` 直接调用 `maio web install`；新增
  `install/` 官方命令行安装入口（Linux/macOS 用户级/系统级、离线 `--from`）。

### Changed

- 版本号 4.0.0 → 4.1.0；`--market` 全局参数与 `MAIO_MARKET_URL` 覆盖市场地址。
- Web 前端不再作为独立 tar 包分发，改由二进制内嵌、显式安装。
- README 增加包管理器、市场、Web 快速开始。

### Deprecated

- Android APK 不再演进；V4.1 以 Web 为正式终端。

## v4.0.0 — 2026-10-27

V4 是独立的分布式产品线，位于 `v4/`。V3 根目录保持原样。

### Added

- **三套平台产品**：`v4/linux/`、`v4/windows/`、`v4/mac/`，各自负责路径、服务
  管理、运行时查找与安装包；共享集群内核在 `v4/core/`。
- **多机分片推理**：基于 llama.cpp RPC。协调者规划层权重，worker 运行
  `ggml-rpc-server`，前端以 `--rpc/--device/-ts` 启动；GPU 显存或内存比例
  决定份额，容量不足提前拒绝。
- **集群控制面**：UDP HMAC 信标发现、静态 seed/配对码、token 鉴权加入、
  心跳/离线淘汰、长轮询命令通道、SSE 作业事件。
- **反向隧道**：worker 主动连协调者 47802，绕过 NAT/防火墙；节点与目标端口
  白名单，避免开放代理。
- **模型分发**：带 Bearer 的 GGUF 下载/断点续传；`cluster distribute`。
- **路由与本地回退**：`shard`（默认多机）、`route`（整请求转给持模型节点）、
  `local`（单机）。
- **四类 IP 链路**：Wi-Fi、网线、USB（RDP/共享/adb reverse）、热点；链路
  分类、广播地址、最佳通告地址、`maio link usb`。
- **Android 快速查看 APK**：无 Gradle 构建（aapt2/d8/apksigner），只读展示
  节点/GPU/模型/任务，支持配对码与 USB `127.0.0.1`。
- **安装包/构建**：Linux `.deb` + tar + systemd；Windows `.zip` + PowerShell
  + 计划任务；macOS tar + `.pkg/.dmg` 脚本 + launchd。
- **测试**：单元/竞态测试、两节点 e2e 脚本；具体环境验收由 `make verify` 和
  `scripts/e2e.sh` 复现，不在仓库保存测试日志。

### Fixed

- **Long-lived fixed token**: `maio init --token <value>` and
  `maio token set <value>` write a non-expiring cluster token; format is
  validated (32..128 URL-safe characters). The token is never sent to external
  model mirrors during downloads.

### Known limitations

- llama.cpp RPC 上游仍是 proof-of-concept，且 **RPC 端口没有认证**；只应在
  受信任局域网使用，公网请加 WireGuard/Tailscale。
- 随包运行时是 CPU 构建；CUDA/Metal 需要自行以 `-DGGML_RPC=ON` 构建。
- Windows/macOS 的 llama.cpp 运行时需在对应平台构建；Go 二进制已交叉编译。
- macOS `.pkg/.dmg` 需在 macOS 生成；Windows 计划任务不是 SCM 服务。
- APK 未在真机/模拟器运行验证；只读，不参与计算。
- 两个 agent 的 e2e 在同一主机不同数据目录完成，物理双机仍需现场验收。
