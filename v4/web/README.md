# maio web console

纯 TS/CSS 的可选控制台。产物通过 `go:embed` 打进 `maio` 二进制，再由
`maio web install` 落盘到数据目录；未安装时协调者 `/` 只返回 CLI 提示。

## 目录

```
web/
├── src/
│   ├── app.ts        # DOM + fetch + SSE，无框架
│   ├── index.html
│   └── style.css
├── assets/           # tsc 产物（提交进 Git，构建时被 embed）
├── build.sh          # tsc + 复制 HTML/CSS
├── tsconfig.json
└── web.go            # go:embed + Install/Remove
```

## 构建

```bash
./build.sh                 # 需要 tsc
# 或
make web
```

`assets/` 已提交，普通 `go build` 不依赖 Node/TypeScript。

## 安装

```bash
maio web install           # 用户级
sudo maio --system web install
maio web install --force   # 覆盖
maio web remove
```

安装后访问 `http://<coordinator>:<port>/`，用集群 Token 登录。资源位置
`<data_dir>/web`，详情见 `install/web/README.md`。

## 页面

- 左侧：节点状态（当前只有一个入口）。
- 顶部：在线 / 总节点数，例如 `1/1`。
- 节点下拉：选择节点后显示详情。
- 五块指标从左到右：负载、CPU、内存、显存、流量，全部带圆形占用图。
- 详细配置：节点、CPU/内存/磁盘、GPU、RPC、链路、模型。

## 安全

- `default-src 'self'` CSP；无内联脚本/样式。
- 登录换 HMAC 签名 HttpOnly Cookie（`SameSite=Strict`），7 天有效；退出清除。
- 所有数据 API 走 `c.auth`，Cookie 与 Bearer 均可；未登录不返回数据。
- 页面输出统一 `esc()`，聊天用 `textContent`，避免把节点名/插件描述当 HTML。
