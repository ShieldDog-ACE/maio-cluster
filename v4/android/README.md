# Android 快速查看 APK

`maio-viewer.apk` 是只读的集群查看器：节点、GPU/内存、模型、最近任务。它
不下载模型、不启动推理、不发送任意命令。

## 已构建产物

```
android/dist/maio-viewer.apk    (~21 KiB)
package: dev.maio.viewer
minSdk 24 / targetSdk 34
```

安装：

```bash
adb install -r android/dist/maio-viewer.apk
```

## 连接方式

| 方式 | 地址 |
| --- | --- |
| Wi-Fi / 网线 / 热点 | `http://<协调者局域网IP>:47800` |
| USB 数据线 | 电脑执行 `maio link usb`，APK 填 `http://127.0.0.1:47800` |
| 配对码 | 直接粘贴 `MAIO1-...`，自动填地址与 token |

`maio link usb` 会对每台已授权设备执行：

```bash
adb reverse tcp:47800 tcp:47800
adb reverse tcp:47801 tcp:47801
adb reverse tcp:47802 tcp:47802
```

## 构建（无 Gradle）

需要：

- JDK 17+（`JAVA_HOME`）
- Android SDK：`platforms;android-34`、`build-tools;34.0.0`
- 环境变量 `ANDROID_SDK`

```bash
export JAVA_HOME=/path/to/jdk-17
export ANDROID_SDK=/path/to/android-sdk
./android/build-apk.sh
```

脚本直接调用 `aapt2 compile/link`、`javac`、`d8`、`zipalign`、`apksigner`，
不依赖 Gradle 或 AndroidX。debug keystore 在 `build/debug.keystore` 生成。

### 从零准备 SDK

```bash
# 1) JDK 17：用系统包管理器安装（Debian/Ubuntu: openjdk-17-jdk-headless）
# 2) command-line tools
curl -LO https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip
unzip commandlinetools-linux-*.zip -d android-sdk/cmdline-tools
mv android-sdk/cmdline-tools/cmdline-tools android-sdk/cmdline-tools/latest
# 3) 接受许可并安装
yes | android-sdk/cmdline-tools/latest/bin/sdkmanager --sdk_root="$PWD/android-sdk" --licenses
android-sdk/cmdline-tools/latest/bin/sdkmanager --sdk_root="$PWD/android-sdk" \
  "platform-tools" "platforms;android-34" "build-tools;34.0.0"
```

如果 Java 报 `trustAnchors parameter must be non-empty`，给 JDK 补系统 CA
（或 `JAVA_TOOL_OPTIONS=-Djavax.net.ssl.trustStore=...`）。这是精简 JDK
包的常见问题，不是 APK 代码问题。

## 源码

- `app/src/main/java/dev/maio/viewer/MainActivity.java`：全部 UI 与 HTTP 逻辑
- `app/src/main/AndroidManifest.xml`：`usesCleartextTraffic="true"`（局域网 HTTP）
- `app/src/main/res/`：名称与图标

安全：APK 只做 GET；token 存在 SharedPreferences；没有后台服务、没有导出
组件（只有 launcher activity）。Android 9+ 默认禁止明文 HTTP，本项目显式
允许，因为目标是受信任局域网/USB。
