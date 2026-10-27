# 版本路线

<p align="center">
  <a href="ROADMAP.md"><img alt="English" src="https://img.shields.io/badge/English-1f6feb?style=for-the-badge"></a>
  <a href="ROADMAP.zh-CN.md"><img alt="简体中文" src="https://img.shields.io/badge/%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-d73a4a?style=for-the-badge"></a>
</p>
<p align="center"><sub>语言：点击徽章切换 · Language: click a badge to switch</sub></p>

每到一个版本都必须提交并打 tag（`v1.0`、`v2.0`…），不保存不进下一版。

| 版本 | 主题 | 必须包含 | 完成判据 |
| --- | --- | --- | --- |
| **V1** | 能用：注册 / 下载 / 删除 | 注册（下载并写 `maio.toml`；本地已有 GGUF 原地登记）、下载（镜像、断点续传、校验）、删除（`rm` 连目录）、`list/show/run/serve/ps/stop/boot` 最小闭环 | 真机装上模型并跑出字；`rm` 后目录与配置干净；`make smoke` 通过 |
| **V2** | 好懂：报错 + 硬件掌握 | 统一英文错误码与可执行提示；配置校验定位到行；硬件探测（GPU/显存/CPU/内存/磁盘）；加载前显存预算（拒绝而不是 OOM）；GGUF 能力探测（上下文/量化/视觉）；`maio doctor`；JSONL 审计日志与 `maio logs`；默认开机自启；只保留 `sudo maio` 一种提权写法 | 失败场景能说清原因并给可执行修复；硬件信息与 `nvidia-smi` 一致；预算不足时给替代参数；`make smoke` 覆盖错误码、doctor 与日志 |
| **V3** ✅ | 能扛：服务器级稳定与安全 | systemd notify/watchdog + 崩溃自愈；有界并发队列与背压；API Key 鉴权；按 IP 限流；审计日志 + Prometheus 指标；常驻模型与 supervisor 自动重启；原子升级/回滚；加固 unit 与专用用户；Linux x86-64 轻量 llama-server；控制面 200 MiB 目标 | `make smoke` 与 `make race` 通过；未授权请求 401；队列/限流返回 429；重启后常驻模型自动恢复；`make soak` 峰值 < 200 MiB 且 0 panic（24h 命令已提供，见 `docs/V3-VERIFICATION.zh-CN.md`） |
| **V4** | 能连：分布式与局域网 | mDNS 节点发现；节点注册；远程安装与模型分发；跨机推理路由；多机放置策略 | 两台机器一次装好；A 机可调用 B 机模型；断网重连不影响本机服务 |
| **V5** | 完整版：完全掌控 | 硬件配置全部可控（卡/显存/线程/上下文/批大小/量化）；图形化界面；中英双语报错与命令/配置语法错误提醒（定位到行/列） | 不手改文件即可完成全部调优；界面覆盖日常操作；错误中英双语且能定位 |

升级原则：每个版本只加该版本必须包含的能力；不提升稳定性、安全性、速度的更新一律不做。

V3 已打 tag `v3.1.0`（v3.0.0 是首个服务器级版本）。验证记录见
[`docs/V3-VERIFICATION.zh-CN.md`](V3-VERIFICATION.zh-CN.md)；24 小时压测命令为
`DURATION=86400 CONCURRENCY=128 make soak`。
