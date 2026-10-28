# 图形化 Web 组件（可选层）

maio 的图形化能力是**独立于 CLI 的可选层**：V4.1 把纯 TS/CSS 产物嵌入 `maio`
二进制，但**不会自动对外提供页面**，必须显式执行一次安装：

```bash
maio web install                  # 用户级：落到 ~/.local/share/maio/web
sudo maio --system web install    # 系统级：落到 /var/lib/maio/web
```

安装后，协调者 `maio cluster up` 会在 `http://<host>:47800/` 提供控制台；
未安装时 `/` 返回一段提示，CLI 与集群 API 不受影响。

## 为什么这样做

- 无头服务器、纯 CLI 用户不需要前端资源，也不希望多开一个页面入口。
- Web 可以独立重装/回滚（`maio web install --force`），不牵动集群配置。
- 资源已嵌入二进制，离线机器也能安装，不需要再从网络拉前端包。

## 安装器/脚本

`install/linux/install.sh --with-web`、`install/mac/install.sh --with-web`
会在装完 CLI 后调用 `maio web install`。也可以单独运行本目录的薄包装：

```bash
bash install/web/install-web.sh
sudo bash install/web/install-web.sh --system
bash install/web/install-web.sh --force --bin ~/.local/bin/maio
```

它只是定位 `maio` 并转发 `web install`，不联网、不下载额外包。

## 命令

| 命令 | 作用 |
| --- | --- |
| `maio web install [--force]` | 把嵌入资源落盘到数据目录 |
| `maio web status` | 查看安装目录与访问地址 |
| `maio web remove` | 删除 Web 资源（CLI/模型不受影响） |

## 登录

控制台使用集群 Token 登录，换取 7 天有效的 HttpOnly 会话 Cookie。
Token 来自协调者：

```bash
maio token show
# 或在设置页/CLI 打印加入码
maio cluster join-code
```

页面不会把 Token 写入 localStorage；退出会清除 Cookie。Web 服务默认只监听
协调者的 `cluster.bind`（默认 `0.0.0.0`），局域网访问必须登录。

## 文件

安装目录：

```
<data_dir>/web/
├── index.html
├── app.js
├── style.css
└── .maio-web.json   # 版本、安装时间
```

`maio web remove` 删除整个目录；重新安装用 `--force`。
