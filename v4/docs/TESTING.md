# 测试

V4 的测试是可重复执行的，不在仓库里保存某台机器的日志、IP、主机名或作业 ID。

## 1. 静态检查与单元测试

```bash
go vet ./...
go test ./...
go test -race ./...
```

覆盖：TOML、名称规范化、GGUF 头/校验、模型安装/删除、放置算法、配对码、
token 格式与恒定时间比较、协调者加入/命令/结果、隧道分帧。

## 2. 两节点 e2e

```bash
RPC_BIN=/path/ggml-rpc-server LLAMA_BIN=/path/llama-server ./scripts/e2e.sh
```

脚本使用临时 HOME 和独立端口，启动一个协调者 + 一个 worker，安装合成
GGUF，检查成员表与 24 层的 `12 + 12` 划分。没有 RPC 运行时会跳过分层检查，
只验证成员关系。

真实模型：

```bash
RPC_BIN=/path/ggml-rpc-server \
LLAMA_BIN=/path/llama-server \
E2E_MODEL=/path/qwen2.5-0.5b-q4_k_m.gguf \
  ./scripts/e2e.sh
```

强制走反向隧道（模拟 NAT/防火墙）：

```bash
MAIO_FORCE_TUNNEL=1 E2E_MODEL=/path/model.gguf ./scripts/e2e.sh
```

## 3. 发布物检查

```bash
# Linux deb
VERSION=4.0.0 RPC_BIN=... LLAMA_BIN=... ./linux/packaging/build-deb.sh

# Windows zip（Linux/macOS 交叉编译）
VERSION=4.0.0 ./windows/build-cross.sh

# macOS 双架构 tar
VERSION=4.0.0 ./mac/build-cross.sh

# Android APK
ANDROID_SDK=/path/android-sdk JAVA_HOME=/path/jdk-17 ./android/build-apk.sh
```

## 4. 现场验收

脚本不能替代物理环境：

- 两台真实机器（Wi-Fi/网线/USB 共享/热点）的加入、断线重连、分片出字；
- Windows/macOS 上带 `-DGGML_RPC=ON` 的运行时；
- Android 真机/模拟器；
- macOS `.pkg/.dmg` 的安装与 launchd 启动。

测试产生的临时目录和日志应由脚本清理（`scripts/e2e.sh` 使用 `trap` 删除）。
