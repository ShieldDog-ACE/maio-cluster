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

V1 is implemented: **register / download / delete** plus local inference, a minimal HTTP API and a
boot switch. The long-term goal is full hardware control and a GUI — see [docs/ROADMAP.md](docs/ROADMAP.md).

## Requirements

- Linux x86_64
- A `llama.cpp` build providing `llama-server` (CUDA build recommended for GPU inference)
- GGUF models

## Build

```bash
make build   # produces bin/maio
make test    # unit tests
make smoke   # end-to-end smoke test (no GPU required)
```

## Install

```bash
make build

# from a source checkout (installs /usr/local/bin/maio and writes /etc/maio/maio.conf)
sudo bash ./maio-cluster

# packaged form (installer/package name = repository name)
sudo install maio-cluster

# then edit /etc/maio/maio.conf and point llama_bin at your llama-server
```

## Configuration

Global config: `/etc/maio/maio.conf` (flat TOML, one per machine — **not** `maio.toml`).

| Key | Meaning | Default |
| --- | --- | --- |
| `boot` | start on boot: `1` = yes, `0` = no | `0` |
| `models_root` | default download root | `/var/lib/maio/models` |
| `state_dir` | runtime state and logs | `/var/lib/maio` |
| `host` | listen address | `127.0.0.1` |
| `port` | listen port | `11434` |
| `mirror` | download mirror (fast CDN preferred) | `https://www.modelscope.cn` |
| `llama_bin` | path to `llama-server`; empty = auto-detect | empty |
| `llama_lib` | extra library dirs, colon separated; empty = auto | empty |
| `ctx_size` | context length; `0` = model default | `0` |

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
sudo install maio-cluster                        install maio itself
maio sudo install <name> [root]                  download a model into <root>/<name>/
maio sudo install <name> [root] --url <url>      install from a direct link
maio sudo install <name> [root] --url <path>     register an existing local GGUF
maio list                                        list installed models
maio show <name>                                 show config and file path
maio rm <name>                                   delete a model
maio run <name> [prompt]                         chat / one-shot inference
maio serve                                       start the LAN service
maio ps                                          list loaded models
maio stop <name>                                 unload a model
maio sudo boot 1|0                               start on boot: on/off
maio version                                     print version
```

## HTTP API

The service speaks a minimal subset of the Ollama API:

- `GET /api/version`
- `GET /api/tags`
- `POST /api/generate` (NDJSON stream)
- `POST /api/chat` (NDJSON stream)

Default endpoint: `http://127.0.0.1:11434`.

## Layout

```text
main.go
internal/cli        command entry and subcommands
internal/config     maio.conf and maio.toml
internal/download   resumable download, checksums, local copy
internal/engine     llama-server process management
internal/index      name -> direct URL index
internal/naming     model name normalization
internal/registry   model directory scanning
internal/server     Ollama-compatible HTTP API
internal/systemd    boot autostart
internal/toml       minimal TOML reader
deploy/install.sh
scripts/smoke.sh
index.toml
docs/               roadmap
```

## License

MIT — see [LICENSE](LICENSE).
