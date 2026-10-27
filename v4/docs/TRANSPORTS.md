# 四种链路

V4 的所有链路最终都是 **IPv4 上的 TCP/UDP**。协议不区分 Wi-Fi、网线、USB
共享还是热点；差异只在地址分配、广播可达性和防火墙。

```
应用层    maio 集群协议（HTTP 47800 / RPC 50100+ / 隧道 47802）
          │
链路层    Wi-Fi │ 以太网 │ USB RNDIS / adb reverse │ 热点
```

## 1. 选择建议

| 场景 | 首选 | 原因 |
| --- | --- | --- |
| 同一房间、多台机器 | 千兆/2.5G 网线 + 交换机 | 延迟最低、最稳定，RPC 张量传输最怕抖动 |
| 临时两台笔记本 | 手机热点或直连网线 | 不用改路由器 |
| 手机只看状态 | USB 数据线 + `adb reverse` | 不依赖 Wi-Fi，充电同时查看 |
| 安卓手机当热点/共享 | USB 共享网络或热点 | RNDIS 通常给 192.168.42.x |
| 跨房间 | Wi-Fi 5/6 | 注意 AP 隔离和 2.4G 拥塞 |

## 2. 局域网 Wi-Fi

1. 所有节点连同一个 SSID/路由器。
2. 关闭“AP 隔离/客户端隔离”（否则节点之间不能互访）。
3. 放行协调者入站：

```bash
# Linux ufw
sudo ufw allow 47800:47802/tcp
sudo ufw allow 47801/udp
# firewalld
sudo firewall-cmd --add-port=47800-47802/tcp --add-port=47801/udp --permanent
sudo firewall-cmd --reload
```

```powershell
# Windows（管理员）
netsh advfirewall firewall add rule name=maio-tcp dir=in action=allow protocol=TCP localport=47800-47802
netsh advfirewall firewall add rule name=maio-udp dir=in action=allow protocol=UDP localport=47801
```

macOS：系统设置 → 网络 → 防火墙 → 允许 `maio` 接受传入连接。

4. 验证：

```bash
maio link list
maio link test
maio cluster discover
```

`maio link list` 会把 `wl*`/`wlan*` 标为 `wifi`，`eth*`/`en*` 标为
`ethernet`，`usb*`/`rndis`/`enx*` 标为 `usb`，192.168.42/43、172.20.10 等
网段标为 `usb`/`hotspot`。

## 3. 网线

### 3.1 通过交换机/路由器

与 Wi-Fi 相同：同网段、 DHCP 或静态地址、放行端口。

### 3.2 两台机器直连

现代网卡支持 Auto-MDIX，普通网线即可。没有 DHCP 时系统会给
`169.254.x.x/16` 链路本地地址；`maio` 会把这类地址排在候选末尾，但仍可用：

```bash
# A
sudo ip addr add 10.10.0.1/24 dev enp3s0
# B
sudo ip addr add 10.10.0.2/24 dev enp3s0
```

然后 A 机 `maio cluster up`，B 机用 A 的 `10.10.0.1` 配对码加入。直连时
RPC 张量走这条线，不经过路由器，通常比 Wi-Fi 稳定。

> 两台 PC 之间仅用一根 USB 线并不能直接组成网络（除非是 USB4/雷电网络或
> USB 网卡）。V4 的“数据线”主要指手机 USB 共享与 `adb reverse`。

## 4. USB 数据线

### 4.1 Android APK 快速查看（adb reverse）

1. 手机开启开发者选项 → USB 调试，插线并授权电脑。
2. 电脑：

```bash
adb devices
maio link usb
# 等价于对每台设备：
# adb reverse tcp:47800 tcp:47800
# adb reverse tcp:47801 tcp:47801
# adb reverse tcp:47802 tcp:47802
```

3. APK 连接 `http://127.0.0.1:47800`，或粘贴 `MAIO1-...` 配对码。

`adb reverse` 把手机的 localhost 端口映射到电脑，不经过 Wi-Fi；拔线后失效。
`maio link usb` 会打印每个设备的结果。

### 4.2 手机 USB 网络共享（RNDIS）

手机设置 → 个人热点 → USB 共享网络。电脑会出现 `usb0`/`enx*`/“Remote NDIS”
网卡，通常拿到 `192.168.42.x`。此时手机与电脑同网段，APK 也可以直接用电脑
的 `192.168.42.x:47800`；不需要 `adb reverse`。

### 4.3 iPhone 个人热点（USB）

iPhone 通过 USB 共享会给电脑 `172.20.10.x`；`maio` 归类为 `hotspot`。电脑
与手机之间是 IP 链路，但 iOS 不能作为 maio worker（没有 Go 运行时/后台
服务），只适合让 APK 查看。

## 5. 热点

1. 手机或随身路由开热点；所有节点接入。
2. 关闭“客户端隔离/AP 隔离”（部分安卓热点默认关闭隔离，部分默认开启）。
3. 热点网关通常是 `192.168.43.1` 或 `192.168.137.1`；协调者地址以
   `maio link list` 为准。
4. 若热点只允许上网、不允许互访，改用网线或 USB 共享。

## 6. 防火墙与端口

| 端口 | 方向 | 用途 |
| --- | --- | --- |
| 47800/TCP | worker → 协调者 | API、模型分发 |
| 47801/UDP | 协调者 → 广播 | 发现 |
| 47802/TCP | worker → 协调者 | 反向隧道 |
| 50100+/TCP | 协调者 → worker（可选） | 直连 RPC；不通则走隧道 |

只放行 47800-47802 也能工作：RPC 直连失败会自动改走 47802 隧道。代价是
所有张量流量经协调者转发，带宽和 CPU 开销更高，因此优先让 50100+ 直连。

## 7. 诊断

```bash
maio link list        # 接口、地址、类型、广播地址
maio link test        # 到协调者的 HTTP 往返
maio cluster discover # 听 6 秒 UDP 信标
maio doctor           # 配置/硬件/RPC/llama/链路/协调者
maio cluster nodes    # 成员是否 online、RPC 是否可用
```

常见问题：

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `cluster discover` 无输出 | 广播被防火墙/AP 隔离拦截 | 用配对码或 `cluster.seeds` |
| 节点显示 `stale/offline` | 心跳被拦、进程退出 | 查 `journalctl -u maio` / `maio logs` |
| `no node can host` | 容量不足或没有 RPC | `maio doctor`；装 `ggml-rpc-server`；换小量化 |
| 作业卡在 running | 前端加载大模型/直连 RPC 慢 | 看 `llama-*.log` 尾部；等待或取消 |
| USB 下 APK 连不上 | adb 未授权/端口未 reverse | `adb devices`；重跑 `maio link usb` |

## 8. 地址选择

`maio` 的 `netinfo.BestAdvertise` 优先级：

1. 有线（ethernet）
2. USB
3. Wi-Fi
4. 热点
5. 其他私有地址
6. 链路本地（最后）

协调者启动时打印的配对码使用这个地址；如果多网卡选错，在
`cluster.advertise` 写死一个 IP 并重启。
