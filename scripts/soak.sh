#!/usr/bin/env bash
# maio V3 soak test: concurrent inference through the real HTTP service, a mock
# llama-server, a bounded queue and the 200 MiB control-plane memory target.
#
#   DURATION=60 CONCURRENCY=64 bash scripts/soak.sh
#   DURATION=86400 CONCURRENCY=128 bash scripts/soak.sh   # 24h-class run
#
# It needs no GPU, no root and no real model.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/maio"
DURATION="${DURATION:-60}"
CONCURRENCY="${CONCURRENCY:-64}"
TMP="$(mktemp -d)"
PORT=$((24000 + $$ % 2000))
KEY="soak-0123456789abcdef0123456789abcdef"
SPID=""
FAIL=0

cleanup() {
  if [ -n "$SPID" ] && kill -0 "$SPID" 2>/dev/null; then
    kill -TERM "$SPID" 2>/dev/null || true
    for _ in $(seq 1 100); do
      kill -0 "$SPID" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL "$SPID" 2>/dev/null || true
  fi
  # Only processes whose command line contains this temporary directory.
  ps -eo pid,cmd | grep -F "$TMP" | grep -E '[m]ock-llama' | awk '{print $1}' | xargs -r kill -KILL 2>/dev/null || true
  if [ "${KEEP_TMP:-0}" = "1" ]; then
    echo "kept test directory: $TMP" >&2
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

[ -x "$BIN" ] || { echo "missing $BIN; run: make build" >&2; exit 1; }
case "$DURATION" in ''|*[!0-9]*) echo "DURATION must be an integer" >&2; exit 1;; esac
case "$CONCURRENCY" in ''|*[!0-9]*) echo "CONCURRENCY must be an integer" >&2; exit 1;; esac

mkdir -p "$TMP/models/tiny" "$TMP/state" "$TMP/etc"
python3 - "$TMP/models/tiny/model.gguf" <<'FIXTURE'
import struct, sys
def s(x):
    b = x.encode()
    return struct.pack('<Q', len(b)) + b
def kv_u32(k, v):
    return s(k) + struct.pack('<I', 4) + struct.pack('<I', v)
def kv_str(k, v):
    return s(k) + struct.pack('<I', 8) + s(v)
out = b'GGUF' + struct.pack('<I', 3) + struct.pack('<Q', 0) + struct.pack('<Q', 8)
out += kv_str('general.architecture', 'llama') + kv_str('general.name', 'soak')
out += kv_u32('general.file_type', 15) + kv_u32('llama.context_length', 4096)
out += kv_u32('llama.block_count', 32) + kv_u32('llama.attention.head_count', 32)
out += kv_u32('llama.attention.head_count_kv', 8) + kv_u32('llama.embedding_length', 4096)
open(sys.argv[1], 'wb').write(out)
FIXTURE
cat > "$TMP/models/tiny/maio.toml" <<'EOF'
name   = "tiny"
format = "gguf"
path   = "model.gguf"
EOF
cat > "$TMP/etc/maio.conf" <<EOF
boot = 0
models_root = "$TMP/models"
state_dir = "$TMP/state"
host = "127.0.0.1"
port = $PORT
llama_bin = "$ROOT/scripts/mock-llama-server.sh"
llama_lib = ""
ctx_size = 4096
auth = "on"
api_key = "$KEY"
rate_limit = 0
max_concurrency = 32
max_queue = 1024
queue_timeout = 60
idle_unload = 0
access_log = 0
parallel = 4
memory_limit_mb = 180
EOF
export MAIO_CONF="$TMP/etc/maio.conf"
export XDG_CONFIG_HOME="$TMP/xdg"
export MOCK_DELAY_MS="${MOCK_DELAY_MS:-20}"
export MOCK_QUIET=1

echo "starting maio service on 127.0.0.1:$PORT"
"$BIN" serve >"$TMP/serve.log" 2>&1 &
SPID=$!
ready=0
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then ready=1; break; fi
  if ! kill -0 "$SPID" 2>/dev/null; then break; fi
  sleep 0.1
done
if [ "$ready" -ne 1 ]; then
  echo "service did not become ready:" >&2
  cat "$TMP/serve.log" >&2
  exit 1
fi
# Warm the model once so the load phase measures steady-state serving.
if ! curl -fsS -m 30 -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"tiny","prompt":"warm","stream":false}' \
  "http://127.0.0.1:$PORT/api/generate" >/dev/null; then
  echo "warm-up inference failed" >&2
  cat "$TMP/serve.log" >&2
  exit 1
fi

echo "running ${DURATION}s soak at concurrency $CONCURRENCY"
set +e
python3 "$ROOT/scripts/soak.py" \
  --base "http://127.0.0.1:$PORT" --key "$KEY" --model tiny \
  --duration "$DURATION" --concurrency "$CONCURRENCY" --report "$TMP/report.json" \
  >"$TMP/load.log" 2>&1
LOAD_RC=$?
set -e
if [ "$LOAD_RC" -ne 0 ] || [ ! -f "$TMP/report.json" ]; then
  echo "load generator failed (exit $LOAD_RC):" >&2
  cat "$TMP/load.log" >&2
  exit 1
fi

# Sample the final state before shutdown.
curl -fsS -m 10 -H "Authorization: Bearer $KEY" "http://127.0.0.1:$PORT/api/stats" >"$TMP/final.json" || true

# Graceful shutdown: SIGTERM must drain and stop every engine.
kill -TERM "$SPID"
for _ in $(seq 1 600); do
  if ! kill -0 "$SPID" 2>/dev/null; then break; fi
  sleep 0.1
done
if kill -0 "$SPID" 2>/dev/null; then
  echo "FAIL: the service did not exit within 60s of SIGTERM" >&2
  FAIL=1
fi
set +e
wait "$SPID"
SRV_RC=$?
set -e
SPID=""
if [ "$SRV_RC" -ne 0 ]; then
  echo "FAIL: service exit code $SRV_RC" >&2
  FAIL=1
fi
if ps -eo pid,cmd | grep -F "$TMP" | grep -qE '[m]ock-llama'; then
  echo "FAIL: a mock engine survived service shutdown" >&2
  FAIL=1
fi

python3 - "$TMP/report.json" "$TMP/final.json" <<'PY' || FAIL=1
import json, sys
report = json.load(open(sys.argv[1]))
final = {}
try:
    final = json.load(open(sys.argv[2]))["metrics"]
except Exception:
    pass
problems = []
if report["error_count"]:
    problems.append(f'{report["error_count"]} transport/5xx errors: {report["errors"][:5]}')
if report["requests"] <= 0:
    problems.append("no requests completed")
if report["max_panics"]:
    problems.append(f'{report["max_panics"]} recovered panics')
if report["max_rss_bytes"] > 200 << 20:
    problems.append(f'max control-plane RSS {report["max_rss_bytes"]/1048576:.1f} MiB > 200 MiB')
if final.get("panics", 0):
    problems.append(f'{final["panics"]} panics in final stats')
print()
print("soak summary")
print(f"  requests      : {report['requests']} in {report['duration_s']}s at concurrency {report['concurrency']}")
print(f"  statuses      : {report['statuses']}")
print(f"  latency ms    : {report['latency_ms']}")
print(f"  max RSS       : {report['max_rss_bytes']/1048576:.1f} MiB (target < 200 MiB)")
print(f"  max fds       : {report['max_open_fds']}")
print(f"  max goroutines: {report['max_goroutines']}")
print(f"  panics        : {report['max_panics']}")
print(f"  final RSS     : {final.get('rss_bytes', 0)/1048576:.1f} MiB, "
      f"requests {final.get('requests_completed', 0)}, "
      f"auth failures {final.get('auth_failures', 0)}, "
      f"queue rejected {final.get('queue_rejected', 0)}")
if problems:
    for p in problems:
        print(f"  FAIL: {p}")
    sys.exit(1)
print("  PASS: no crashes, no panics, under the 200 MiB control-plane target")
PY

if [ "$FAIL" -ne 0 ]; then
  echo
  echo "service log tail:" >&2
  tail -20 "$TMP/serve.log" >&2 || true
  exit 1
fi
echo "soak passed"
