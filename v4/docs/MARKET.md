# maio market · 插件与模型

V4.1 的包管理器只认一个远程市场仓库：

```text
https://github.com/ShieldDog-ACE/maio-market
├── plugins.json   # 插件数据库
└── models.json    # 模型数据库
```

客户端（CLI 与 Web）默认读取 raw 文件，不在本地维护清单：

```text
https://raw.githubusercontent.com/ShieldDog-ACE/maio-market/main/plugins.json
https://raw.githubusercontent.com/ShieldDog-ACE/maio-market/main/models.json
```

镜像/内网部署可用 `MAIO_MARKET_URL` 或全局 `--market` 覆盖；也可以是本地目录
（`--market /srv/maio-market`），用于离线考场/内网。

## 1. 插件

### 1.1 上架

开发者在自己的 GitHub 仓库根目录放 `plugin.json`：

```json
{
  "name": "hello",
  "version": "0.1.0",
  "description": "一句话说明",
  "author": "your-github-name",
  "main": "main.sh",
  "web": { "panel": "panel.html" }
}
```

然后给 `maio-market` 发 PR，在 `plugins.json` 的 `plugins` 数组里加一条：

```json
{
  "name": "hello",
  "author": "your-github-name",
  "repo": "https://github.com/your-github-name/hello",
  "description": "一句话说明",
  "tags": ["tool"]
}
```

合并即上架。CI/维护者只做结构校验和一眼审核，没有服务器成本。

### 1.2 安装

```bash
maio install hello                 # 从市场解析 repo 并下载
maio install --repo https://github.com/you/hello   # 直接装任意 GitHub 仓库
maio plugins                       # 已安装
maio plugins update hello
maio plugins remove hello
```

安装器行为：

- 从 `https://codeload.github.com/<owner>/<repo>/tar.gz/refs/heads/<branch>` 下载；
- 限制 64 MiB 压缩包、128 MiB 解压、2000 个文件；
- 拒绝 `..`、绝对路径、符号链接/硬链接，防止逃逸；
- 校验 `plugin.json` 的 `name` 与市场条目一致；
- 原子落盘到 `<data_dir>/plugins/<name>`，写 `.maio-plugin.json`（repo/branch/sha/时间）；
- **不执行插件代码**。执行入口（`main`）留给后续版本或使用者自行决定。

### 1.3 Web

控制台的“市场”页列出 `plugins.json`，点击安装调用
`POST /v1/plugins/install`，与 CLI 走同一套 market/plugin 代码。

## 2. 模型

`models.json` 条目：

```json
{
  "name": "qwen2.5-0.5b",
  "author": "Qwen",
  "url": "https://.../qwen2.5-0.5b-instruct-q4_k_m.gguf",
  "quant": "Q4_K_M",
  "context": 32768,
  "size": 0,
  "sha256": "",
  "description": "说明"
}
```

```bash
maio pull qwen2.5-0.5b          # 从市场解析 URL 并下载
maio pull my-model --url https://.../x.gguf
maio pull my-model --url ./x.gguf --sha256 <hex>
```

`maio pull` 复用 V4 的 `model.Store`：断点续传、SHA-256 校验、GGUF 元数据解析、
原子写入 `<data_dir>/models/<name>/`。`maio model list|show|rm|path` 继续可用。

## 3. CLI 自身

```bash
maio update --check      # 当前版本 / 最新 release
maio update              # 下载、校验、原子替换
maio update --force      # 忽略 apt 检测，直接替换
```

Linux 用 apt 安装时，`maio update` 会检测 dpkg 归属并提示：

```bash
sudo apt update && sudo apt install --only-upgrade maio-cluster
```

模型和插件仍然只用 `maio pull` / `maio install`，不经过 apt。

## 4. 配置

| 方式 | 作用 |
| --- | --- |
| `MAIO_MARKET_URL=https://...` | 进程级覆盖 |
| `maio --market https://... market` | 单次覆盖 |
| `maio --market /srv/maio-market market` | 本地镜像目录 |

Web 服务在启动时读取 `MAIO_MARKET_URL`；未设置则用默认 raw 地址。市场响应缓存 30 秒，
`--refresh` 或 `?refresh=1` 强制刷新。

## 5. 安全

- 市场只提供元数据；插件包从 GitHub 拉取，安装前做路径/大小校验。
- 模型下载不携带集群 token（`model.Store` 只对协调者 URL 附加 token）。
- Web 登录使用集群 Token 换 HttpOnly Cookie；市场/插件/模型 API 全部要求认证。
- 插件默认不执行；如果后续增加执行，应单独做权限与沙箱评审。

## 6. 相关代码

| 路径 | 作用 |
| --- | --- |
| `core/market` | 读取、校验、缓存 plugins.json / models.json |
| `core/plugin` | 下载 GitHub tarball、安全解压、安装/更新/删除 |
| `core/cluster/web_api.go` | 市场、插件、模型、Web 会话 API |
| `web/` | 纯 TS/CSS 控制台（嵌入二进制） |
| `core/cli/pkg_cmd.go` | `install` / `pull` / `plugins` / `market` / `web` |
| `core/update` | `maio update` 的 release 检查、校验、自替换 |
