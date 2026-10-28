# 官方命令行安装（maio v4.1+）

本目录是 maio 的**官方命令行安装入口**。它只做一件事：把 maio 装到机器上，并把“装什么”拆成两层。

发布物：<https://github.com/ShieldDog-ACE/maio-cluster/releases/tag/v4.1.1>

| 层 | 装什么 | 装法 | 必需 |
| --- | --- | --- | --- |
| CLI 内核 | `maio` 单二进制、集群协议、llama.cpp 运行时 | `linux/install.sh`、`mac/install.sh` | 是 |
| 图形化 Web | 纯 TS/CSS 静态产物（对话、节点、入网、历史、安全开关） | `--with-web`，或单独跑 `web/install-web.sh` | 否 |

**图形化能力不随 CLI 默认安装。** 无头服务器只装 CLI；需要 Web 控制台时再执行一条命令。这样纯 CLI / 无头节点不会被迫拉取前端资源，也符合“图形化能力需要命令行额外安装”的约定。

## 目录

```
install/
├── README.md               # 本文件：安装契约与矩阵
├── install.sh              # 按平台分派到 linux/ 或 mac/
├── linux/
│   ├── README.md
│   └── install.sh          # Linux 官方安装器
├── mac/
│   ├── README.md
│   └── install.sh          # macOS 官方安装器
└── web/
    ├── README.md
    └── install-web.sh      # 图形化组件安装器（可选层）
```

## Linux 首选：apt

```bash
curl -fsSL https://cdn.jsdelivr.net/gh/ShieldDog-ACE/maio-apt@apt-v4.1.1/setup.sh | sudo sh
```

之后 CLI 更新用 `maio update`，模型用 `maio pull`，插件用 `maio install`。
本目录的 `linux/install.sh` 保留给无 apt 的系统或用户级安装。

## 快速开始

```bash
# 一条命令：自动识别 Linux / macOS，用户级（默认，不碰系统目录）
bash install/install.sh

# 系统级（root，systemd / launchd 系统服务）
sudo bash install/install.sh --system

# 也可以直接调平台安装器
bash install/linux/install.sh
bash install/mac/install.sh

# 只跑子节点（agent），不跑协调者
bash install/linux/install.sh --worker

# 装完 CLI 后，再装图形化组件
bash install/web/install-web.sh
```

离线安装（V4.1 产物即可用）：

```bash
bash install/linux/install.sh --from v4/dist/linux-amd64
bash install/mac/install.sh --from v4/dist/maio-mac-arm64.tar.gz
```

安装器会：落盘二进制 → 生成集群身份（已有配置则保留）→ 安装并启动平台服务。
`--no-start` 只落盘，不装服务；适合先检查再手动启动。

## 安装后启动的是什么模式？

- 默认服务是**协调者 + 本机 agent**（服务器形态），不是“个人单机模式”。
- 当前 CLI **没有** `personal` / `server` 模式开关；`--user` / `--system` 只决定
  文件与服务的权限边界，不决定运行角色。
- 只跑 worker 用 `--worker`；纯客户端调用用 `maio run`（仍需先有协调者）。
- V4.1 计划增加 `maio up --mode personal|server`，详见任务书第 8 节的调研与建议。

## 安装包命名

发布物按版本归档，安装器默认从 `v<version>/` 下取：

```
v4.1.1/
├── maio-linux-amd64.tar.gz     (+ .sha256)
├── maio-linux-arm64.tar.gz
├── maio-mac-arm64.tar.gz
└── maio-mac-amd64.tar.gz
```

CLI 包内布局：

```
maio                    # 必需
bin/ggml-rpc-server     # 可选，分片推理需要
bin/llama-server        # 可选，本机/前端推理需要
install.sh              # 可选，包内自带的平台安装器
README.md
```

Web 产物已嵌入 `maio` 二进制：安装器只调用 `maio web install`，把资源落盘到
数据目录的 `web/`，不再单独下载 `maio-web-*.tar.gz`。

下载地址可用 `--base-url` 或 `MAIO_BASE_URL` 覆盖；校验可用 `--sha256` 固定摘要，
安装器也会尝试读取同 URL 的 `.sha256`，没有则告警但不阻断。

## 路径矩阵

| 作用域 | 二进制 | 配置 | 数据 | 服务 |
| --- | --- | --- | --- | --- |
| Linux 用户 | `~/.local/bin/maio` | `~/.config/maio/cluster.toml` | `~/.local/share/maio` | systemd `--user` |
| Linux 系统 | `/usr/local/bin/maio` | `/etc/maio/cluster.toml` | `/var/lib/maio` | systemd 系统单元 |
| macOS 用户 | `~/.local/bin/maio` | `~/Library/Application Support/maio/cluster.toml` | 同左 | LaunchAgent |
| macOS 系统 | `/usr/local/bin/maio` | `/Library/Application Support/maio/cluster.toml` | 同左 | LaunchDaemon |

`--prefix` 只改二进制前缀，不改配置/数据位置；非常规部署请同时用 `--config` 管理配置。

## 安全边界

- 默认用户级：不写 `/usr`、不建系统用户、不需要 sudo。
- `--system` 才需要 root；Linux 会创建 `maio` 系统用户，配置 `root:maio 0640`。
- 集群 token 由 `maio init` 生成，落在配置文件里（`0600`）；不要提交进 Git。
- Web 控制台用集群 Token 登录，换取 HttpOnly 会话 Cookie；未安装或未登录时不暴露数据。

## 当前状态（V4.1）

- CLI 安装器可用，已用 v4.1.1 的 `v4/dist/linux-amd64` 与 `maio-linux-amd64.tar.gz` 实测用户级离线安装。
- V4.1 起 `--with-web` 直接调用 `maio web install`，Web 资源嵌入二进制，离线可装。
- V3 的 `make deb` 与 V4 的 `v4/dist/` 保持原样；本目录是新增入口，不替代它们。

## 相关文档

- 包管理器与市场：`../v4/docs/MARKET.md`
- 市场仓库：<https://github.com/ShieldDog-ACE/maio-market>
- 安装器选项：`linux/README.md`、`mac/README.md`
- 图形化组件：`web/README.md`
- 集群命令与协议：`../v4/README.md`
