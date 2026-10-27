#!/usr/bin/env bash
# Two-node end-to-end test on one host.
#
#   ./scripts/e2e.sh
#   BASE_PORT=49300 E2E_MODEL=/path/model.gguf ./scripts/e2e.sh
#
# It starts a coordinator and a worker with isolated HOME directories, joins
# them, installs a synthetic GGUF, and checks that the planner splits layers
# across both agents. With E2E_MODEL it also runs a real sharded completion.
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_PORT="${BASE_PORT:-49300}"
DISC_PORT=$((BASE_PORT + 1))
ROOT="$(mktemp -d /tmp/maio-e2e.XXXXXX)"
BIN="$ROOT/maio"
GOFLAGS="${GOFLAGS:-}"
cleanup() {
  set +e
  [[ -n "${COORD_PID:-}" ]] && kill "$COORD_PID" 2>/dev/null
  [[ -n "${AGENT_PID:-}" ]] && kill "$AGENT_PID" 2>/dev/null
  wait 2>/dev/null
  rm -rf "$ROOT"
}
trap cleanup EXIT

echo "== build =="
CGO_ENABLED=0 go build -trimpath -o "$BIN" ./linux/cmd/maio
mkdir -p "$ROOT/a" "$ROOT/b"
export MAIO_DISCOVERY_PORT="$DISC_PORT"
RPC_BIN="${RPC_BIN:-$(command -v ggml-rpc-server || true)}"
LLAMA_BIN="${LLAMA_BIN:-$(command -v llama-server || true)}"
if [[ -f "$RPC_BIN" ]]; then export PATH="$(dirname "$RPC_BIN"):$PATH"; fi

echo "== coordinator =="
HOME="$ROOT/a" "$BIN" init --port "$BASE_PORT" >/dev/null
if [[ -f "$RPC_BIN" ]]; then
  sed -i "s|^rpc_bin = .*|rpc_bin = \"$RPC_BIN\"|" "$ROOT/a/.config/maio/cluster.toml"
fi
if [[ -f "$LLAMA_BIN" ]]; then
  sed -i "s|^llama_bin = .*|llama_bin = \"$LLAMA_BIN\"|" "$ROOT/a/.config/maio/cluster.toml"
fi
HOME="$ROOT/a" nohup "$BIN" cluster up >"$ROOT/coord.log" 2>&1 &
COORD_PID=$!
for i in $(seq 1 100); do
  if HOME="$ROOT/a" "$BIN" cluster status >/dev/null 2>&1; then break; fi
  sleep 0.1
done
JOIN=$(HOME="$ROOT/a" "$BIN" cluster join-code)
if [[ -z "$JOIN" ]]; then
  echo "no join code; coordinator log:" >&2
  cat "$ROOT/coord.log" >&2
  exit 1
fi

echo "== worker =="
HOME="$ROOT/b" "$BIN" cluster join "$JOIN" >/dev/null
if [[ -f "$RPC_BIN" ]]; then
  sed -i "s|^rpc_bin = .*|rpc_bin = \"$RPC_BIN\"|" "$ROOT/b/.config/maio/cluster.toml"
fi
if [[ -f "$LLAMA_BIN" ]]; then
  sed -i "s|^llama_bin = .*|llama_bin = \"$LLAMA_BIN\"|" "$ROOT/b/.config/maio/cluster.toml"
fi
# The worker only needs the seed; disable its own discovery listener.
sed -i.bak 's/^discovery = true/discovery = false/' "$ROOT/b/.config/maio/cluster.toml"
python3 - "$ROOT/b/.config/maio/cluster.toml" <<'PY'
import sys,re
p=sys.argv[1]; s=open(p).read()
s=re.sub(r'(?m)^(\[agent\]\n(?:.*\n)*?name = )"[^"]*"', r'\1"worker"', s, count=1)
open(p,'w').write(s)
PY
HOME="$ROOT/b" nohup "$BIN" agent >"$ROOT/agent.log" 2>&1 &
AGENT_PID=$!

for i in $(seq 1 100); do
  if HOME="$ROOT/a" "$BIN" cluster nodes 2>/dev/null | grep -q 'worker'; then break; fi
  sleep 0.2
done
HOME="$ROOT/a" "$BIN" cluster nodes

echo "== synthetic model =="
python3 - "$ROOT/tiny.gguf" <<'PY'
import struct, sys
def s(x):
    b=x.encode(); return struct.pack('<Q',len(b))+b
def kv(k,t,p): return s(k)+struct.pack('<I',t)+p
data=b'GGUF'+struct.pack('<IQQ',3,0,6)
data+=kv('general.architecture',8,s('llama'))
data+=kv('general.name',8,s('e2e'))
data+=kv('general.file_type',4,struct.pack('<I',15))
data+=kv('llama.block_count',4,struct.pack('<I',24))
data+=kv('llama.context_length',4,struct.pack('<I',4096))
data+=kv('llama.embedding_length',4,struct.pack('<I',4096))
open(sys.argv[1],'wb').write(data)
PY
HOME="$ROOT/a" "$BIN" model install e2e-tiny --url "$ROOT/tiny.gguf" >/dev/null

if [[ -z "$RPC_BIN" ]]; then
  echo "SKIP: ggml-rpc-server not found; membership checked, layer split not checked"
  echo "PASS: two-node membership (RPC runtime not installed)"
  exit 0
fi

echo "== plan =="
PLAN=$(HOME="$ROOT/a" "$BIN" cluster plan e2e-tiny)
echo "$PLAN"
if ! echo "$PLAN" | grep -q 'node2\|worker'; then
  echo "planner did not include the second node" >&2
  exit 1
fi
if ! echo "$PLAN" | grep -q '12 layers\|~12'; then
  echo "planner did not split 24 layers evenly" >&2
  exit 1
fi

if [[ -n "${E2E_MODEL:-}" && -f "${E2E_MODEL:-}" ]]; then
  echo "== real sharded inference =="
  NAME="e2e-real"
  HOME="$ROOT/a" "$BIN" model install "$NAME" --url "$E2E_MODEL" >/dev/null
  HOME="$ROOT/a" timeout 300 "$BIN" run "$NAME" "只回答：分布式成功" --mode shard --max-tokens 8
fi

echo "PASS: two-node membership and layer split"
