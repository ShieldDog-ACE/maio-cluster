# Changelog

All notable changes are recorded here. Versions follow the project roadmap
(`v1.0`, `v2.0`, `v3.0`, ...).

## v3.0.0 — 2026-10-27

### Added

- **Server-grade HTTP service**
  - global inference semaphore + bounded FIFO queue (`max_concurrency`, `max_queue`,
    `queue_timeout`) with 429/503 backpressure;
  - per-client-IP token bucket (`rate_limit`, `rate_burst`) with `Retry-After`;
  - API-key authentication (`auth = auto|on|off`), generated `maio_...` keys, constant-time
    comparison, `maio token` show/rotate/set/clear;
  - optional TLS termination (`tls_cert`, `tls_key`);
  - optional trusted-proxy client IP (`trust_proxy`);
  - Prometheus `/metrics`, JSON `/api/stats`, `/healthz`, `/readyz`;
  - request IDs, structured access log, panic recovery, body-size limits;
  - graceful drain on SIGTERM and a single-instance flock.
- **Daemon behaviour**
  - systemd `Type=notify`, `WatchdogSec=30`, `Restart=always`, `OOMPolicy=stop`;
  - hardened unit: dedicated `maio` user, `ProtectSystem=strict`, `NoNewPrivileges`,
    empty capabilities, private `/tmp`, restricted address families;
  - resident models persisted in `run/resident.json` and restored on start;
  - supervisor restarts failed resident engines with 2s→60s exponential backoff;
  - engine log rotation and stale runtime-state cleanup.
- **Engine tuning**
  - `parallel` slots (`-np`), `kv_type` (`f16`/`q8_0`/`q4_0`), `threads`, `flash_attn`,
    `mmap`;
  - VRAM budget includes KV × slots and auto-reduces slots when the requested count does
    not fit;
  - serialized engine starts so concurrent requests cannot oversubscribe VRAM.
- **CLI (ollama-complete)**
  - new `pull`, `cp`, `load`, `status`, `stats`, `service`, `restart`, `config`, `token`,
    `completion`, `upgrade`, `rollback`;
  - `maio status/stats --json`, `maio config get/set/edit/keys`, shell completion;
  - `maio run` flags for slot count, KV type and threads.
- **Lightweight llama.cpp (Linux x86-64)**
  - `scripts/build-llama-minimal.sh`: server-only static build, non-Linux backends off,
    CUDA arch selectable (default sm_70), GCC/CUDA preflight;
  - `scripts/bundle-llama.sh`: prune an existing build to `llama-server` + required libs
    (81 MiB → 72 MiB stripped), verified by running `--version`;
  - `deploy/build-llama-deb.sh` + `make deb-llama` package `/opt/maio/llama`.
- **Testing and operations**
  - `scripts/soak.sh` + `scripts/soak.py` with a mock engine, concurrency, graceful
    shutdown and a hard < 200 MiB control-plane RSS assertion;
  - `make verify`, `make race`, `make soak`;
  - `docs/V3-VERIFICATION*.md` with the real test log;
  - packaged service user creation, state-dir ownership and hardened unit.

### Changed

- Default version is `3.0.0`.
- Default systemd unit runs as `maio`, not root.
- `/api/generate` and `/api/chat` pass Ollama options, `keep_alive`, usage/timings and
  vision images to llama.cpp.
- `/api/ps`, `/api/show`, `/api/load`, `/api/unload` and OpenAI-compatible endpoints added.
- `maio stop --all` now also clears resident state through the service.
- `maio rm` unloads a running model first and removes its resident entry.
- Error list extended with 401/429/busy/timeout codes (12–15).

### Fixed

- `resident.json` was mistakenly treated as an engine instance by `maio ps`.
- Concurrent cold loads could both pass the VRAM check and OOM.
- Engine logs and instance state could grow without bound on a 24/7 host.
- `/api/generate` streaming final chunk now includes usage and an empty response field.

## v2.0.0 — 2026-10-27

- English errors with stable codes and hints; line-numbered config errors.
- Hardware probing, GGUF capability detection and pre-load VRAM budget.
- `maio doctor`, JSONL audit log, default start-on-boot.
- apt/deb packaging and local apt repository.

## v1.0.0 — 2026-10-26

- Model register/download/delete, local inference, Ollama-compatible minimal API,
  `list/show/run/serve/ps/stop/boot` loop.
