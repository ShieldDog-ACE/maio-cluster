# maio v4.1 · distributed

A LAN-first distributed inference cluster built on llama.cpp RPC. One machine
coordinates and runs the frontend; every other machine computes a share of the
model layers. There are three independent product trees (Linux, Windows,
macOS). V4.1 adds a package manager (plugins via `install`, models via `pull`),
an open market, and an optional web console; the Android APK is no longer
evolving.

V4 is not an adaptation layer on top of V3. V3 stays untouched at the repo
root; V4 starts fresh under `v4/`:

```
v4/
├── core/      shared protocol, discovery, placement, agent, jobs
├── linux/    Linux adapter + systemd + .deb/.tar.gz
├── windows/  Windows adapter + Task Scheduler + .zip/PowerShell
├── mac/      macOS adapter + launchd + .pkg/.dmg/.tar.gz
├── web/      optional TS/CSS console (embedded, installed with maio web install)
├── android/  legacy read-only APK (no compute)
├── docs/     architecture, protocol, transports, verification, market
└── scripts/  end-to-end test, llama.cpp RPC build
```

The three platform directories contain only OS-specific paths, service
management, runtime lookup and packaging. The cluster protocol and scheduler
live once in `core/`.

## Install (Linux, apt)

```bash
curl -fsSL https://cdn.jsdelivr.net/gh/ShieldDog-ACE/maio-apt@apt-v4.1.2/setup.sh | sudo sh
```

This adds a flat apt repository and installs `maio-cluster`. After that,
models/plugins/CLI updates go through maio itself:

```bash
sudo maio --system pull qwen2.5-0.5b
sudo maio --system install hello
maio update --check
```

## V4.1 in one minute

```bash
maio market                  # list market plugins/models
maio install hello           # install a plugin from GitHub
maio pull qwen2.5-0.5b       # download a model from the market
maio web install             # optional web console
maio update --check          # check the CLI itself
```

Market: <https://github.com/ShieldDog-ACE/maio-market>. Plugins live in each
author's GitHub repo; a one-entry PR to `plugins.json` is the whole listing
process. See [`docs/MARKET.md`](docs/MARKET.md).

## Quick start (two Linux machines)

Machine A:

```bash
maio id                # machine ID, node ID and registered token
sudo apt install ./dist/maio-cluster_4.1.1_amd64.deb
sudo maio --system token set '<long-lived-token>'   # optional fixed token
sudo maio --system cluster join-code
sudo maio --system pull qwen2.5-0.5b
sudo maio --system web install          # optional console
```

Machine B:

```bash
sudo apt install ./dist/maio-cluster_4.1.1_amd64.deb
sudo maio --system cluster join 'MAIO1-...'
sudo systemctl restart maio
```

Machine A:

```bash
sudo maio --system cluster plan qwen2.5-0.5b
sudo maio --system run qwen2.5-0.5b "hello" --mode shard
```

The plan prints each node's layer share (for a 24-layer model on two equal
nodes: 12 + 12). `--mode shard` starts one `ggml-rpc-server` per worker and a
frontend `llama-server --rpc ... --device RPC0,RPC1 -ts 12,12`. Workers run the
matrix multiplies; the frontend only tokenizes, samples and moves tensors.

Windows: unzip and run `.\install.ps1 -System` (or add `-Worker -JoinCode`).
macOS: install the `.pkg`, then `sudo maio --system cluster join 'MAIO1-...'`.

## Transports

| Link | Setup | Notes |
| --- | --- | --- |
| Wi-Fi LAN | same router | disable AP isolation; allow TCP 47800-47802, UDP 47801 |
| Ethernet | same switch or direct cable | link-local 169.254/16 or a static /24 |
| USB cable | USB tethering (RNDIS); APK uses `adb reverse` | `maio link usb` configures the APK ports |
| Hotspot | phone/travel router | same as Wi-Fi; AP isolation must be off |

All four are IP links, so discovery, HTTP and RPC are identical. Discovery is
an HMAC-signed UDP broadcast; static seeds and the `MAIO1-...` join code work
when broadcast is blocked.

## Build

```bash
./scripts/build-rpc.sh --cuda          # or --cpu / --metal
RPC_BIN=dist/runtime/bin/ggml-rpc-server \
LLAMA_BIN=dist/runtime/bin/llama-server \
  make package-linux

make windows        # dist/maio-windows-amd64.zip
make mac            # dist/maio-mac-{arm64,amd64}.tar.gz
make apk            # needs JAVA_HOME + ANDROID_SDK
make verify         # vet + race + two-node e2e
```

Artifacts:

```
dist/maio-cluster_4.1.1_amd64.deb
dist/linux-amd64/{maio,bin/llama-server,bin/ggml-rpc-server,install.sh}
dist/maio-windows-amd64.zip
dist/maio-mac-arm64.tar.gz, dist/maio-mac-amd64.tar.gz
android/dist/maio-viewer.apk
```

macOS `.pkg`/`.dmg` must be produced on macOS (`pkgbuild`, `hdiutil`).
The Windows zip is cross-compiled and its PowerShell installer targets
Task Scheduler, avoiding the Windows SCM 1053 problem.

## Security

- A 256-bit cluster token is the only join credential; the join code contains
  it, so treat the code as a password. Tokens do not expire; `maio token set`
  writes a fixed value and `maio token rotate` replaces it.
- Discovery beacons carry an HMAC, never the token.
- Model distribution is bearer-authenticated.
- The reverse tunnel validates token, node ID and a per-node target-port
  allowlist, so it cannot become an open proxy.
- **llama.cpp RPC has no authentication** upstream. Keep 50100+ on a trusted
  LAN, never expose it to the internet; force the reverse tunnel if isolation
  is needed. Optional TLS keys exist (`security.tls_cert/key`) but the client
  does not pin certificates yet; put the cluster behind WireGuard/Tailscale
  for untrusted networks.
- Linux package runs as the `maio` user; Windows uses a SYSTEM scheduled task;
  macOS uses a LaunchDaemon.

## Testing

The repository keeps repeatable tests, not machine-specific test logs:

```bash
make verify          # vet + race + two-node membership/placement
RPC_BIN=/path/ggml-rpc-server LLAMA_BIN=/path/llama-server \
E2E_MODEL=/path/model.gguf ./scripts/e2e.sh
```

See [TESTING.md](docs/TESTING.md). Environment limits: physical two-machine
validation, Windows/macOS RPC runtimes, Android device testing and macOS
`.pkg`/`.dmg` generation still require the corresponding hardware or OS.

## Docs

- [Architecture](docs/ARCHITECTURE.md)
- [Protocol](docs/PROTOCOL.md)
- [Transports](docs/TRANSPORTS.md)
- [Testing](docs/TESTING.md)
- [V3 → V4 migration](docs/MIGRATION.md)
