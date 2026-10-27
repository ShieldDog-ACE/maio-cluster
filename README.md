# maio-cluster

<p align="center">
  <a href="README.md"><img alt="English" src="https://img.shields.io/badge/English-1f6feb?style=for-the-badge"></a>
  <a href="README.zh-CN.md"><img alt="简体中文" src="https://img.shields.io/badge/%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-d73a4a?style=for-the-badge"></a>
</p>
<p align="center"><sub>Language: click a badge to switch · 语言：点击徽章切换</sub></p>

A large-model inference service backend for distributed LANs.

`maio` is a lightweight, ollama-like command-line tool and HTTP service built on top of
[llama.cpp](https://github.com/ggml-org/llama.cpp). It registers, downloads and deletes GGUF models,
runs local inference, and exposes an Ollama-compatible API to the local network.
The control plane is written in Go with **zero third-party dependencies** and ships as a single binary.

## Status

V2 is implemented:

- **English messages and a stable error-code system** (language packs can be added later).
- **Actionable failures everywhere** — config type/typo errors point at the line, missing files,
  bad GGUF headers, HTTP errors, disk space, permissions, port conflicts, engine crashes and
  service failures all explain the cause and the next step.
- **Hardware awareness** — CPU/RAM/disk probing, NVIDIA GPU detection through `nvidia-smi`,
  GGUF metadata probing (context, quantization, vision) and a **VRAM budget check before loading**.
- **`maio doctor`** — one command that checks config, hardware, `llama-server`, systemd, models,
  VRAM budgets and the audit log.
- **Audit logging** — installs, downloads, registrations, deletions, engine starts/stops and
  failures are written as JSON Lines and readable with `maio logs`.
- **Start on boot is on by default** and the package/installer starts the service immediately.
- **One command syntax**: `sudo maio ...`. The old `maio sudo ...` form is rejected with a hint.

## Requirements

- Linux x86_64
- A `llama.cpp` build providing `llama-server` (CUDA build recommended for GPU inference)
- GGUF models
- Optional: NVIDIA driver + `nvidia-smi` for VRAM detection and GPU inference

## Build

```bash
make build   # produces bin/maio
make test    # unit tests
make smoke   # end-to-end smoke test (no GPU and no root required)
make deb     # produces dist/maio-cluster_<version>_<arch>.deb
make repo    # produces dist/apt (local apt repository)
```

## Install

### apt / Debian package (recommended)

```bash
# one-time: build the .deb, set up a local apt repository, install and start
sudo bash deploy/apt-install.sh

# after the repository is registered, the documented command works:
sudo apt install maio-cluster
```

A one-off install without a repository also works:

```bash
make deb
sudo apt install ./dist/maio-cluster_2.0.0_amd64.deb
```

If a per-user service is already using port 11434, stop it before installing the system
service, otherwise the new service cannot bind the port:

```bash
maio boot 0 --user
sudo bash deploy/apt-install.sh
```

The package installs `/usr/bin/maio`, `/etc/maio/maio.conf` (with `boot = 1`), and a systemd
service, then enables and starts it immediately.

### Source checkout

```bash
make build
sudo bash ./maio-cluster              # install and start on boot (default)
sudo bash ./maio-cluster --no-start   # install only
sudo bash ./maio-cluster --boot 0     # install with start on boot disabled
```

`deploy/install.sh` is a compatibility wrapper around `./maio-cluster`.

### Rootless / per-user service

```bash
# use a user-writable config and state directory, then enable the user unit
maio boot 1 --user
```

The per-user service starts after login. For boot-time start without a system service:

```bash
sudo loginctl enable-linger "$USER"
```

## Configuration

Global config: `/etc/maio/maio.conf` (flat TOML, one per machine — **not** `maio.toml`).
Without root, maio falls back to `~/.config/maio/maio.conf` and a user state directory when
`/var/lib/maio` is not writable. `MAIO_CONF` overrides both.

| Key | Meaning | Default |
| --- | --- | --- |
| `boot` | start on boot: `1` = yes, `0` = no | `1` |
| `models_root` | default download root | `/var/lib/maio/models` |
| `state_dir` | runtime state and logs | `/var/lib/maio` |
| `host` | listen address | `127.0.0.1` |
| `port` | listen port | `11434` |
| `mirror` | download mirror (fast CDN preferred) | `https://www.modelscope.cn` |
| `llama_bin` | path to `llama-server`; empty = auto-detect | empty |
| `llama_lib` | extra library dirs, colon separated; empty = auto | empty |
| `ctx_size` | context length; `0` = model default | `0` |

Unknown keys and wrong value types are reported with the exact line number instead of being
silently ignored.

Per-model config: `<models_root>/<name>/maio.toml`, exactly three fields.

```toml
name   = "qwen3.5-9b"
format = "gguf"
path   = "models/Qwen3.5-9B-Q4_K_M.gguf"
```

One model per directory; copying the directory to another machine is a complete migration.

Model names are lowercase with hyphens: `DeepSeek v4.1` is normalized to `deepseek-v4.1`.

## Commands

```text
sudo apt install maio-cluster                       install maio itself (package)
sudo bash ./maio-cluster                            install maio itself (source)

sudo maio install <name> [root]                     download a model into <root>/<name>/
sudo maio install <name> [root] --url <url>         install from a direct link
sudo maio install <name> [root] --url <path>        register an existing local GGUF
sudo maio install <name> [root] --sha256 <hex>      verify a direct download by SHA-256

maio list [--wide] [--json]                         list installed models
maio show <name> [--json]                           show config, file and capabilities
maio rm <name> [-y]                                 delete a model and its files
maio run <name> [prompt] [--ctx N] [--force]        chat / one-shot inference

maio serve [--host ADDR] [--port N]                 start the HTTP service
maio ps [--json]                                    list loaded models
maio stop <name> | --all                            unload model(s) and free VRAM
sudo maio boot 1|0                                  start on boot: on/off
maio boot 1|0 --user                                per-user service without root

maio doctor [--json]                                health check
maio logs [--tail N] [--json] [--path]              read the audit log
maio version [--json]                               version and paths
```

Use `sudo maio ...`, **not** `maio sudo ...`.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | success |
| `2` | usage / invalid arguments |
| `3` | not found (model, file, command) |
| `4` | conflict (already registered, non-empty directory) |
| `5` | permission or filesystem I/O |
| `6` | download failure |
| `7` | verification failure (SHA-256 / GGUF) |
| `8` | engine failure |
| `9` | hardware / doctor failure |
| `10` | config error |
| `11` | service / systemd error |
| `130` | interrupted |

Every error also prints a stable code such as `MAIO-E006` and a `hint:` line with the next step.

## Hardware and VRAM

`maio run`, `maio serve` and `maio doctor` read the GGUF header to learn the architecture,
context length, layer/head counts, quantization and vision capability. Before loading a model
maio compares the weights + KV cache + overhead estimate with free GPU memory:

- if it does not fit, the load is rejected **before** llama-server starts;
- the error suggests a concrete `--ctx` value;
- `--force` bypasses the check (for experts who know the real memory layout);
- `maio doctor` shows the estimate for every model at the configured context.

## Audit log

The log is JSON Lines at `<state_dir>/log/maio.log` (or the per-user fallback). It records:

- every CLI invocation and its exit code;
- install source, destination, SHA-256, size and duration;
- local registrations and whether they were in place, hardlinked or copied;
- deletions with path and size;
- engine start/stop, port, PID, context and VRAM estimate;
- service start/stop and boot changes;
- failures with their error code and hint.

```bash
maio logs --tail 50
maio logs --json
maio logs --path
```

Engine stdout/stderr remains in `<state_dir>/log/<model>.log`.

## HTTP API

The service speaks a minimal subset of the Ollama API:

- `GET /api/version`
- `GET /api/tags`
- `POST /api/generate` (NDJSON stream)
- `POST /api/chat` (NDJSON stream)

Default endpoint: `http://127.0.0.1:11434`.

Errors are JSON with a compatible `error` string plus `code`, `message` and `hint` fields.
Models idle for 5 minutes are unloaded automatically.

## Layout

```text
main.go
internal/cli        command entry, install/list/show/rm/run/serve, doctor, logs
internal/config     maio.conf / maio.toml, validation and per-user fallback
internal/download   resumable download, checksums, disk checks, local copy
internal/engine     llama-server process management, VRAM check, safe stop
internal/errs       stable error codes, exit codes and hints
internal/gguf       GGUF metadata parser (context/quantization/vision)
internal/hardware   CPU/RAM/disk/NVIDIA GPU detection
internal/index      name -> direct URL index
internal/logging    JSONL audit log
internal/naming     model name normalization
internal/registry   model directory scanning
internal/server     Ollama-compatible HTTP API
internal/systemd    system and per-user services
internal/toml       minimal TOML reader with line numbers
internal/vram       VRAM budget estimation
deploy/             package, apt repository and installer scripts
scripts/smoke.sh    end-to-end test
index.toml
docs/               roadmap
```

## License

MIT — see [LICENSE](LICENSE).
