# Changelog

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
