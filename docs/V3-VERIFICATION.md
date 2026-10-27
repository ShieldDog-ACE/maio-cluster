# V3 verification log

This file records the actual V3 (server-grade) verification on the target machine.
Every command is reproducible from the repository root.

## Environment

| Item | Value |
| --- | --- |
| Date | 2026-10-27 |
| OS | Ubuntu 26.04 LTS (Resolute Raccoon), kernel 7.0.0-38-generic |
| Architecture | x86-64 |
| CPU | 12 logical cores |
| Memory | 31 GiB |
| GPU | 2 × Tesla V100-SXM2-16GB, driver 580.178.04 |
| Go | go1.27.1 linux/amd64 |
| llama-server | 0.7.0-dev (build 0), CUDA sm_70 |

## 1. Unit tests and race detector

```bash
make test       # all packages pass
make race       # go test -race ./... passes
make vet        # no output
```

Covered: auth, config, engine, gguf, hardware, lockfile, logging, metrics, naming,
queue, ratelimit, registry, resident, sdnotify, server, systemd, toml, vram.

## 2. End-to-end smoke

```bash
make smoke
```

All checks pass, including install/local registration/delete/`cp`, error codes
2/3/4/5/6/7/10, service API (401 without a key, `/api/version`, `/api/tags`,
`/healthz`, `/metrics`), doctor (exit 9 without a llama-server), and the V3 CLI
surface (`config`, `token`, `completion`, `status`, `stats`, `ps`).

## 3. Concurrency soak and the 200 MiB target

```bash
DURATION=10 CONCURRENCY=32 make soak
```

| Metric | Result |
| --- | --- |
| Requests | 8427 in 10 seconds |
| Statuses | all 200 |
| Latency | p50 27.2 ms, p95 68.7 ms, p99 75.8 ms |
| Peak control-plane RSS | **21.8 MiB** (target < 200 MiB) |
| Peak FDs | 190 |
| Peak goroutines | 241 |
| Panics | 0 |
| SIGTERM | exited within 60 s, status 0 |
| Surviving engines | none |

Re-run at `DURATION=30 CONCURRENCY=128`: 27484 requests, all 200, peak RSS
**27.9 MiB**, p95 196 ms, zero panics.

The soak uses `scripts/mock-llama-server.sh`, so it needs no GPU while exercising
the real HTTP service, authentication, queue, streaming, metrics and graceful
shutdown. For a 24h-class run:
`DURATION=86400 CONCURRENCY=128 make soak` (not run continuously for 24h here;
the harness is provided).

## 4. Authentication

`auth = "auto"`, `host = "0.0.0.0`, first start:

- generates a 256-bit `maio_...` key with mode `0600`;
- `/api/tags` without a key → `401`;
- `Authorization: Bearer <key>` → `200`;
- `maio token --show` returns the full key; the default display is masked.

## 5. Rate limiting

`rate_limit = 2`, `rate_burst = 2`:

```text
1:200 2:200 3:429 4:429
HTTP/1.1 429 Too Many Requests
Retry-After: 29
```

## 6. Request body limit

`max_body_mb = 1`, 2 MiB body:

```text
status=413
{"code":"MAIO-E001","error":"request body exceeds the 1 MiB limit",
 "hint":"send a smaller request, or raise max_body_mb in maio.conf", ...}
```

## 7. Bounded queue and backpressure

`max_concurrency = 2`, `max_queue = 2`, mock delay 800 ms, six simultaneous requests:

```text
200 200 429 200 429 200
```

Four were admitted (two running, two queued) and two were rejected immediately; total
~2.4 s. The audit log contains `http.queued` and `the inference queue is full`.

## 8. Single-instance lock

A second `maio serve` with the same `state_dir`:

```text
exit=4
maio: MAIO-E003: another maio service is already running (pid ..., lock .../run/maio.lock)
```

## 9. Resident model restore

- `maio load tiny` writes `tiny` to `resident.json`; `/api/ps` reports `resident=true`;
- SIGTERM shuts down the service and every engine;
- restarting the service restores `tiny` with a new PID and `resident=true`.

## 10. Engine crash self-healing

- load and pin `tiny`;
- `kill -9` the mock engine PID;
- the supervisor starts a new PID within 16.6 seconds;
- `/api/stats`: `engine_restarts=1`, `engine_restart_failures=0`.

## 11. Real GPU inference

Using the existing `llama-server` and Qwen3.5-9B:

```bash
MAIO_CONF=/home/fuka/.config/maio/maio.conf \
  ./bin/maio run qwen3.5-9b "Reply with exactly: hello from maio v3" \
  --ctx 4096 --parallel 1
```

Output: `hello from maio v3`, exit 0.

## 12. Lightweight llama.cpp

Pruning the existing CUDA build:

```bash
LLAMA_BUILD=/home/fuka/AI/qwen3.8-27b/runtime/llama.cpp-master/build \
LLAMA_LIB=.../cuda-12.9/lib64 \
OUT=/tmp/maio-llama-bundle bash scripts/bundle-llama.sh
```

Result:

- one `llama-server` plus the 8 required `.so` files, symbols stripped;
- 81 MiB → **72 MiB**;
- `llama-server --version` starts from the pruned tree;
- Qwen3.5-9B inference through the pruned runtime: `bundled llama works`, exit 0.

The from-source minimal build script builds only the `llama-server` target, disables
non-Linux backends, tests, examples and the Web UI, and links llama/ggml statically.
The CPU minimal build on this machine is a **14 MiB single file**; `llama-server --version`
works and `ldd` shows only libc/libstdc++/libgomp/libm/libgcc, with no libllama/libggml.

Ubuntu 26.04 ships GCC 15, which CUDA 12.9's host compiler rejects (libstdc++ 15 headers
still fail even with `-allow-unsupported-compiler`). The script detects this first and
offers three choices: install `g++-14`, use `CUDA=0`, or reuse an existing CUDA build
(what this machine did for GPU inference).

## 13. systemd unit

`systemd-analyze verify` on the generated unit (with `/bin/true` substituted to avoid
the path check):

```text
verify_exit=0
```

The unit contains `Type=notify`, `WatchdogSec=30`, `Restart=always`, `User=maio`,
`ProtectSystem=strict`, `CapabilityBoundingSet=`, `ReadWritePaths`, and more.

## 14. Upgrade and rollback

```bash
cp bin/maio /tmp/maio-candidate
maio upgrade /tmp/maio-candidate --force --no-restart
maio rollback --no-restart
```

The backup is written to `<state_dir>/upgrade/maio-3.0.0-<timestamp>`; replacement
succeeds, rollback succeeds, and `maio version` still reports 3.0.0.

## 15. Default user scope / explicit system scope (3.1.0)

- `maio version` → `scope: user`, config `~/.config/maio/maio.conf`;
- `maio --system version` as non-root → exit 5 with
  `sudo maio --system version`;
- `maio --system --user ...` → exit 2 (mutually exclusive);
- `maio boot 1 --dry-run` → user unit `~/.config/systemd/user/maio.service`,
  `systemctl --user enable --now`;
- generated unit tests: system `ExecStart=... --system serve`, user
  `ExecStart=... --user serve`; the user unit never sets `User=`;
- the two scopes use separate config/state/ports and can coexist.

3.1.0 only changes scope selection and defaults; queueing, auth, memory and the
supervisor are the same as 3.0.0, so the measurements above still apply.

## 16. Known limitations

- The 24h soak harness is provided but was not run continuously for 24h on this machine.
- CUDA runtime libraries (`libcudart`/`libcublas`/`libcublasLt`) are not bundled with
  the lightweight runtime; the NVIDIA driver/toolkit provides them.
- “Power on after AC loss” is a BIOS/UEFI setting; the OS cannot change it.
- Non-Linux platforms are out of scope.
