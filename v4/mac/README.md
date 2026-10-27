# maio v4 on macOS

The macOS product builds a universal binary (Apple silicon + Intel), a signed
`.pkg` installer and a `.dmg`. It uses `launchd` — a LaunchAgent for per-user
installs, a LaunchDaemon for machine-wide installs.

## Build

On macOS with Go 1.24+, Xcode command-line tools and an RPC-enabled llama.cpp:

```bash
# optional: llama.cpp with Metal + RPC
cmake -S llama.cpp -B build-rpc -DGGML_METAL=ON -DGGML_RPC=ON
cmake --build build-rpc -j

VERSION=4.0.0 RPC_BIN=build-rpc/bin/ggml-rpc-server LLAMA_BIN=build-rpc/bin/llama-server \
  ./mac/build.sh
./mac/packaging/build-pkg.sh          # .pkg
./mac/packaging/build-dmg.sh          # .dmg
```

`SIGN_ID="Developer ID Installer: ..."` signs the package. The binary itself
can be signed/notarized separately before packaging.

From Linux/macOS without Xcode, produce per-architecture tarballs:

```bash
./mac/build-cross.sh
```

## Install

```bash
sudo installer -pkg maio-cluster-4.0.0.pkg -target /
# or
tar -xzf maio-mac-arm64.tar.gz -C maio
bash maio/install.sh --user          # LaunchAgent
sudo bash maio/install.sh            # LaunchDaemon
```

After install:

```bash
maio --system cluster join-code
maio --system cluster nodes
launchctl print system/dev.maio.cluster
```

Paths:

```
~/Library/Application Support/maio/cluster.toml        (user)
/Library/Application Support/maio/cluster.toml         (system)
~/Library/Application Support/maio/models/             (user models)
/Library/Application Support/maio/models/              (system models)
~/Library/LaunchAgents/dev.maio.cluster.plist
/Library/LaunchDaemons/dev.maio.cluster.plist
```

## Notes

- Apple silicon uses unified memory. The agent reports one Metal device whose
  capacity is the machine's free unified memory, so a Mac can be a worker
  without a discrete GPU.
- The RPC server and the frontend must both be built with `-DGGML_RPC=ON`.
  Metal RPC support requires a current llama.cpp; CPU fallback always works.
- macOS may prompt for incoming connections (Application Firewall). Allow
  `maio` for private networks; ports are TCP 47800-47802 and UDP 47801.
- `maio link usb` uses `adb` from Android platform-tools, same as Linux.
