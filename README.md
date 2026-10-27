# maio-cluster

<p align="center">
  <a href="README.md"><img alt="English" src="https://img.shields.io/badge/English-1f6feb?style=for-the-badge"></a>
  <a href="README.zh-CN.md"><img alt="简体中文" src="https://img.shields.io/badge/%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-d73a4a?style=for-the-badge"></a>
</p>
<p align="center"><sub>Language: click a badge to switch · 语言：点击徽章切换</sub></p>

A large-model inference service backend for distributed LANs.
**V3 stable: server grade.**

`maio` is a lightweight, ollama-like command-line tool and HTTP service built on
[llama.cpp](https://github.com/ggml-org/llama.cpp). It registers, downloads and deletes
GGUF models, runs local inference, and exposes both an **Ollama-compatible** and an
**OpenAI-compatible** API. The control plane is written in Go with **zero third-party
dependencies** and ships as a single binary. It targets **Linux x86-64 only** (tested on
Ubuntu 26.04, Go 1.27, Tesla V100).

## What V3 adds

- **Concurrency and backpressure** — a global inference semaphore plus a bounded FIFO
  waiting queue. A full queue returns 429 immediately instead of exhausting memory.
  Requests are cancellable and streaming responses are flushed chunk by chunk.
- **Authentication** — `auth = "auto"` by default. A non-loopback listener gets a generated
  256-bit API key (0600); unauthenticated requests receive `401`. `maio token` shows,
  rotates, sets and clears it.
- **Rate limiting** — per-client-IP token bucket, 600 requests/minute with a burst of 120
  by default. Excess requests get `429` with `Retry-After`.
- **Memory target** — the Go soft limit is 180 MiB by default; the control plane typically
  stays at 20–40 MiB. `make soak` asserts **control-plane RSS < 200 MiB**. Model and KV
  memory are separate.
- **Daemon behaviour** — systemd `Type=notify`, `WatchdogSec=30`, `Restart=always`. READY
  is sent only after the listener is up; a wedged process is restarted by the watchdog.
  SIGTERM drains requests and then stops every engine.
- **Crash recovery** — resident models (`maio load`) are checked by a supervisor every
  10 seconds and restarted with exponential backoff. Shutdown releases all engines;
  resident models are restored on the next start.
- **Sandboxing** — dedicated `maio` user, `ProtectSystem=strict`, `NoNewPrivileges`,
  empty capability set, restricted address families, private `/tmp`, and more.
- **Observability** — `/healthz`, `/readyz`, `/metrics` (Prometheus text), `/api/stats`
  (JSON), plus `maio status`, `maio stats` and `maio doctor`.
- **Complete CLI** — ollama-style `pull / list / show / cp / rm / run / serve / ps /
  load / stop / status / stats / service / restart / boot / config / token / doctor /
  logs / version / completion / upgrade / rollback`.
- **Upgrade and rollback** — `maio upgrade <binary>` replaces the binary atomically,
  keeps a backup, restarts the service, and rolls back automatically if the restart
  fails. `maio rollback` restores the previous binary.
- **Lightweight llama.cpp** — Linux-only minimal `llama-server` build and bundle scripts
  that drop CLI, benchmarks, quantize, tests, examples and the Web UI. llama/ggml can be
  linked statically; CUDA is compiled for the target architecture (sm_70 by default).
- **Start on boot** — the package enables `maio.service` by default; see “Power-on start”.

## Requirements

- **Linux x86-64** (no macOS or Windows builds)
- A `llama-server` binary (`make llama` to build, or `make llama-bundle` to prune an
  existing build)
- GGUF models
- Optional: NVIDIA driver + CUDA runtime for GPU inference; `nvidia-smi` for VRAM probing
- Optional: systemd 249+ (V3 was verified on Ubuntu 26.04)

## Build and test

```bash
make build        # bin/maio (3.0.0 by default)
make test         # unit tests
make race         # race detector
make smoke        # end-to-end smoke test (no GPU, no root)
make soak         # concurrency + 200 MiB RSS target (mock engine, no GPU)
make verify       # vet + test + smoke
```

See [`docs/V3-VERIFICATION.md`](docs/V3-VERIFICATION.md) for the full V3 verification log.

## Install

### apt / Debian package (recommended)

```bash
sudo bash deploy/apt-install.sh     # build .deb, register a local apt repo, install, start
sudo apt install maio-cluster       # afterwards
```

One-off install:

```bash
make deb
sudo apt install ./dist/maio-cluster_3.0.0_amd64.deb
```

The package:

- creates the unprivileged system user `maio` (member of `video` / `render`);
- installs `/usr/bin/maio`, `/etc/maio/maio.conf` (`boot = 1`, `run_as = "maio"`);
- installs the hardened systemd unit;
- immediately runs `systemctl enable --now`.

### Source checkout

```bash
make build
sudo bash ./maio-cluster              # install and start on boot
sudo bash ./maio-cluster --no-start   # install only
sudo bash ./maio-cluster --boot 0     # install, no start on boot
```

### Rootless / per-user service

```bash
maio boot 1 --user
sudo loginctl enable-linger "$USER"   # start without a login session
```

The user service uses the user's own config and state directory and does not create the
system `maio` account.

### Power-on start

“Start when power returns” is a firmware policy, not something an OS service can set.
Two steps are required:

1. enable **Restore on AC Power Loss / AC Back = Power On** in the BIOS/UEFI;
2. keep `boot = 1` (the default) and check `systemctl is-enabled maio` → `enabled`.

After mains power returns the machine boots and systemd starts `maio.service`.
`Restart=always` also covers an abnormal crash. Verify with `maio status`.

## Lightweight llama.cpp (Linux x86-64)

### 1. Build a minimal `llama-server` from source

```bash
make llama                    # CUDA=1, CUDA_ARCH=70, NATIVE=1
CUDA=0 make llama             # CPU-only
CUDA_ARCH=70 CUDA_ROOT=/usr/local/cuda-12.9 make llama
```

Output: `dist/maio-llama/bin/llama-server`.

The script disables Vulkan/SYCL/OpenCL/Metal/RPC, tests, examples, the Web UI and
curl/openssl, and builds only the `llama-server` target. `BUILD_SHARED_LIBS=OFF` links
llama/ggml into one binary. CUDA runtime libraries (`libcudart`, `libcublas`,
`libcublasLt`) remain external, so the “lightweight” runtime does not carry 800 MB+ of
CUDA. On GCC > 14 with CUDA 12.x the script stops with an actionable message (install
`g++-14`, use `CUDA=0`, or reuse an existing CUDA build).

### 2. Prune an existing llama.cpp build (no compiler)

```bash
LLAMA_BUILD=/path/to/llama.cpp/build make llama-bundle
```

Only `llama-server` and the 7 shared libraries it actually loads are copied. Symbols are
stripped; the result is about 72 MB (from an 81 MB `bin/`) with no CLI, benchmark or
quantize tools. The script runs `llama-server --version` from the copied tree to prove the
bundle works.

Package it as a deb:

```bash
make llama-bundle
make deb-llama                # dist/maio-llama_3.0.0_amd64.deb -> /opt/maio/llama
```

Then in `/etc/maio/maio.conf`:

```toml
llama_bin = "/opt/maio/llama/bin/llama-server"
llama_lib = "/opt/maio/llama/lib:/usr/local/cuda/lib64"
```

## Configuration

Global config: `/etc/maio/maio.conf` (flat TOML, one per machine — **not** `maio.toml`).
Without root, maio falls back to `~/.config/maio/maio.conf` and a user state directory.
`MAIO_CONF` overrides.

### Service and security

| Key | Meaning | Default |
| --- | --- | --- |
| `host` / `port` | listen address / port | `127.0.0.1` / `11434` |
| `auth` | `auto` / `on` / `off` | `auto` |
| `api_key` | explicit key (prefer the 0600 file managed by `maio token`) | empty |
| `allow_insecure` | permit `auth=off` on a non-loopback listener | `0` |
| `trust_proxy` | trust `X-Forwarded-For` (only behind a proxy) | `0` |
| `cors` | `1` = allow cross-origin browser access (off by default) | `0` |
| `tls_cert` / `tls_key` | optional TLS termination | empty |
| `rate_limit` / `rate_burst` | requests per minute per client / burst | `600` / `120` |
| `max_concurrency` | inference requests executing at once | `8` |
| `max_queue` | requests allowed to wait | `64` |
| `queue_timeout` | seconds to wait for a slot (0 = forever) | `300` |
| `request_timeout` | whole-request timeout in seconds (0 = none) | `0` |
| `max_body_mb` | request body limit | `16` |
| `idle_unload` | seconds before an unpinned model unloads (0 = never) | `300` |
| `access_log` | one JSONL audit line per HTTP request | `1` |

### Engine and resources

| Key | Meaning | Default |
| --- | --- | --- |
| `llama_bin` / `llama_lib` | `llama-server` / extra library dirs | auto-detect |
| `ctx_size` | context length; `0` = model default | `0` |
| `parallel` | llama-server slots (`-np`) | `2` |
| `threads` | CPU threads; `0` = engine default | `0` |
| `kv_type` | KV cache `f16` / `q8_0` / `q4_0` | `f16` |
| `flash_attn` | `auto` / `on` / `off` | `auto` |
| `mmap` | `1` = mmap weights | `1` |
| `llama_log_verbosity` | llama-server log level 0–5 (2 = warnings/errors only) | `3` |
| `memory_limit_mb` | Go soft memory limit (control plane) | `180` |
| `max_procs` | GOMAXPROCS; `0` = all cores | `0` |
| `max_open_files` | RLIMIT_NOFILE target | `1048576` |
| `restore` | restore resident models on start | `1` |
| `run_as` | systemd `User=`; the package uses `maio` | empty = root |

Unknown keys, wrong types and invalid enums are reported with the exact line. `maio config`
can show, validate and edit them.

Per-model config: `<models_root>/<name>/maio.toml`, exactly three fields:

```toml
name   = "qwen3.5-9b"
format = "gguf"
path   = "models/Qwen3.5-9B-Q4_K_M.gguf"
```

## Commands

```text
# models
sudo maio pull|install <name> [root] [--url URL|PATH] [--sha256 HEX]
maio list [--wide] [--json]
maio show <name> [--json]
maio cp <source> <destination>
maio rm <name> [-y]
maio run <name> [prompt] [--ctx N] [--parallel N] [--kv f16|q8_0|q4_0] [--force]

# service
maio serve [--host ADDR] [--port N] [--api-key KEY] [--no-auth]
maio ps [--json]
maio load <name>                     # pin; restored after a restart
maio stop <name> | --all
maio status [--json] [--user]
maio stats [--json]
maio service <start|stop|restart|status|enable|disable|logs> [--user]
maio restart [--user]
sudo maio boot 1|0 [--user]

# security and configuration
maio token [show] [--show] [--json]
maio token rotate|set|clear
maio config [show] [--json]
maio config path|get|set|edit|keys
maio doctor [--json]
maio logs [--tail N] [--json] [--path]
maio version [--json]
maio completion bash|zsh|fish
maio upgrade <binary> [--no-restart]
maio rollback [--no-restart]
```

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | success |
| `2` | usage / bad arguments |
| `3` | not found |
| `4` | conflict |
| `5` | permission / I/O |
| `6` | download |
| `7` | verification |
| `8` | engine |
| `9` | hardware / doctor |
| `10` | config |
| `11` | service / systemd |
| `12` | unauthorized |
| `13` | rate limited |
| `14` | busy / queue full |
| `15` | timeout |
| `130` | interrupted |

Every error prints a stable code such as `MAIO-E006` plus a `hint:` line.

## Server behaviour

### Concurrency, queue and backpressure

A request acquires one of `max_concurrency` inference slots. When all are busy it waits in
a FIFO queue of at most `max_queue`. When that is full:

```json
{"error":"the inference queue is full","code":"MAIO-E015","hint":"retry shortly, ..."}
```

Waiting longer than `queue_timeout` returns 503. A disconnected client cancels its wait
immediately and does not hold a slot. Streaming output is flushed per chunk and never
buffered as a whole in the control plane.

### VRAM and slots

`parallel` slots share one copy of the weights but **multiply the KV cache**. Before start,
maio compares weights + KV×slots + overhead against free VRAM. It automatically reduces the
number of slots (with a warning) when the requested number does not fit, and only refuses
when even one slot does not fit. `kv_type = "q8_0"` roughly halves KV memory at a small
quality cost. `maio doctor` shows the budget for every model.

### Resident models and idle unload

- `maio load <name>` loads and pins a model, writing `<state_dir>/run/resident.json`;
  it is restored after a service restart (`restore = 1`).
- Models loaded by ordinary requests unload after `idle_unload` seconds; pinned models
  never do.
- Request `keep_alive` is supported: `-1`/`"forever"` pins, `0` unloads after the response,
  a positive duration extends the idle deadline.
- A supervisor checks resident engines every 10 seconds and restarts a failed one with
  2s→60s exponential backoff.

### Authentication and rate limiting

```bash
maio token --show          # full key (masked by default)
maio token rotate          # new 0600 key
maio token set <key>
maio token clear
```

Client:

```bash
curl -H "Authorization: Bearer $KEY" http://127.0.0.1:11434/api/tags
# X-API-Key: $KEY is accepted too
```

With `auth = "auto"` a loopback listener may run without a key; a non-loopback listener
generates one on first start. `auth = "off"` is only allowed on loopback unless
`allow_insecure = 1` is set explicitly.

### Memory target

The target is **control-plane** RSS < 200 MiB, not total inference memory:

- Go soft limit `memory_limit_mb = 180`;
- streamed request/response bodies and a fixed-size buffer pool;
- `maio stats` reports RSS; `make soak` asserts the peak stays below 200 MiB.

Weights, KV cache and the CUDA context live in the `llama-server` child process and count
against GPU/system memory separately.

## HTTP API

Default: `http://127.0.0.1:11434`.

### Ollama-compatible

- `GET /api/version`
- `GET /api/tags`
- `GET /api/ps`
- `POST /api/show`
- `POST /api/generate` (NDJSON stream)
- `POST /api/chat` (NDJSON stream)
- `POST /api/embed`, `POST /api/embeddings`
- `POST /api/load`, `POST|DELETE /api/unload` (`{"all":true}` unloads everything)

### OpenAI-compatible

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/completions`
- `POST /v1/embeddings`

OpenAI requests are passed through to llama-server, preserving all parameters and
streaming behaviour.

### Operations

- `GET /healthz` (no auth)
- `GET /readyz`
- `GET /metrics` (Prometheus; auth required)
- `GET /api/stats` (JSON; auth required)

Errors are JSON with `error`, `code`, `message` and `hint`.

## Security and systemd

The generated unit contains:

```ini
Type=notify
NotifyAccess=main
Restart=always
WatchdogSec=30
KillMode=mixed
OOMPolicy=stop
User=maio
Group=maio
SupplementaryGroups=video render
CapabilityBoundingSet=
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
SystemCallArchitectures=native
ReadWritePaths=-/var/lib/maio
ReadOnlyPaths=-/var/lib/maio/models
```

If your model directory is under `/home` and `run_as = "maio"`, make it readable by the
`maio` user, or leave `run_as` empty to run as root.

## Operations

```bash
maio status                 # service + health + memory + auth
maio stats                  # requests/queue/restarts/tokens/RSS/FDs/goroutines
maio doctor                 # config, hardware, engine, unit hardening, models, budget
maio service logs --tail 200
maio logs --tail 50
maio upgrade ./bin/maio     # atomic replace + backup + restart, auto-rollback on failure
maio rollback
```

Logs:

- audit: `<state_dir>/log/maio.log` (JSON Lines, 10 MB rotation)
- engine: `<state_dir>/log/<model>.log` (8 MB rotation)

## Soak test

```bash
make soak                                  # 60s / 64 concurrent by default
DURATION=3600 CONCURRENCY=128 make soak    # one hour
DURATION=86400 CONCURRENCY=128 make soak   # 24 hours
```

`scripts/soak.sh` drives the full HTTP path with a mock `llama-server` and asserts:

- zero crashes, zero panics, zero transport errors;
- peak control-plane RSS < 200 MiB;
- SIGTERM exits within 60 seconds and leaves no engine behind.

Short local run (32 concurrent, 10s): 8427 requests, all 200, peak RSS 21.8 MiB, 0 panics.
Use `DURATION=86400` on the target machine for a 24h-class run.

## Layout

```text
main.go
internal/cli         command entry, every subcommand, API client
internal/config      maio.conf / maio.toml, validation, per-user fallback
internal/download    resumable download, checksums, disk checks, local copy
internal/engine      llama-server lifecycle, VRAM budget, slots, log rotation
internal/errs        stable codes, exit codes, hints
internal/gguf        GGUF metadata parser
internal/hardware    CPU/RAM/disk/NVIDIA GPU probing
internal/index       name -> direct URL index
internal/logging     JSONL audit log
internal/naming      model name normalization
internal/registry    model directory scanning
internal/server      HTTP service, auth, rate limit, queue, metrics, supervisor
internal/auth        API key resolution/generation/constant-time compare
internal/metrics     Prometheus text + JSON metrics
internal/queue       bounded concurrency/waiting queue
internal/ratelimit   per-IP token bucket
internal/resident    resident-model persistence
internal/sdnotify    systemd READY/WATCHDOG/STOPPING
internal/lockfile    single-instance flock
internal/tune        memory limit, GOMAXPROCS, RLIMIT_NOFILE
internal/systemd     system/user units and lifecycle
internal/toml        minimal line-numbered TOML reader
internal/vram        VRAM budget (slots/KV type)
deploy/              packages, apt repo, hardened unit, llama runtime package
scripts/             smoke, soak, mock engine, llama build/bundle
docs/                roadmap and V3 verification
```

## License

MIT — see [LICENSE](LICENSE). The llama.cpp runtime follows its upstream license.
