#!/usr/bin/env bash
# maio V1 smoke test: install / list / show / register / delete / API.
# It uses a temporary directory and a local HTTP source, so no GPU and no real model are needed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/maio"
TMP="$(mktemp -d)"
HTTP_PORT=$((20000 + $$ % 1000))
API_PORT=$((21000 + $$ % 1000))
HTTP_PID=""
API_PID=""

cleanup() {
  [ -n "$API_PID" ] && kill "$API_PID" 2>/dev/null || true
  [ -n "$HTTP_PID" ] && kill "$HTTP_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

pass() { printf '  [OK] %s\n' "$1"; }
fail() { printf '  [FAIL] %s\n' "$1" >&2; exit 1; }

echo "== 1. fixtures =="
mkdir -p "$TMP/models" "$TMP/state" "$TMP/etc" "$TMP/fixtures"
python3 - "$TMP/fixtures/tiny.gguf" <<'FIXTURE'
import hashlib, sys
data = b"GGUF" + bytes(range(256)) * 4
open(sys.argv[1], "wb").write(data)
open(sys.argv[1] + ".sha256", "w").write(hashlib.sha256(data).hexdigest() + "  tiny.gguf\n")
FIXTURE
pass "tiny.gguf fixture created"

cat > "$TMP/etc/maio.conf" <<EOF
boot        = 0
models_root = "$TMP/models"
state_dir   = "$TMP/state"
host        = "127.0.0.1"
port        = $API_PORT
mirror      = "https://www.modelscope.cn"
llama_bin   = ""
llama_lib   = ""
ctx_size    = 0
EOF
export MAIO_CONF="$TMP/etc/maio.conf"

echo "== 2. basic commands =="
"$BIN" version >/dev/null || fail "maio version"
"$BIN" --help | grep -q "maio sudo install" || fail "help is missing install"
"$BIN" list | grep -q "no models yet" || fail "empty listing message"
pass "version / help / list"

echo "== 3. install from a local HTTP source =="
( cd "$TMP/fixtures" && exec python3 -m http.server "$HTTP_PORT" >/dev/null 2>&1 ) &
HTTP_PID=$!
ready=0
for _ in $(seq 1 50); do
  if curl -fsS -o /dev/null "http://127.0.0.1:$HTTP_PORT/tiny.gguf" 2>/dev/null; then ready=1; break; fi
  sleep 0.2
done
[ "$ready" -eq 1 ] || fail "local HTTP source did not start (port $HTTP_PORT)"

"$BIN" install "DeepSeek v4.1" "$TMP/models" --url "http://127.0.0.1:$HTTP_PORT/tiny.gguf" >/dev/null \
  || fail "install with name normalization"
[ -f "$TMP/models/deepseek-v4.1/maio.toml" ] || fail "deepseek-v4.1/maio.toml was not created"
grep -q 'name   = "deepseek-v4.1"' "$TMP/models/deepseek-v4.1/maio.toml" || fail "name was not normalized"
grep -q 'format = "gguf"' "$TMP/models/deepseek-v4.1/maio.toml" || fail "format field is missing"
[ -f "$TMP/models/deepseek-v4.1/tiny.gguf" ] || fail "model file was not written"
pass "install + name normalization (DeepSeek v4.1 -> deepseek-v4.1)"

"$BIN" list | grep -q "deepseek-v4.1" || fail "list does not show the model"
"$BIN" show deepseek-v4.1 | grep -q "format : gguf" || fail "show output is wrong"
pass "list / show"

echo "== 3b. local registration (copy / in place / duplicate guard) =="
mkdir -p "$TMP/localcopy"
cp "$TMP/fixtures/tiny.gguf" "$TMP/localcopy/copy-model.gguf"
"$BIN" install copy-model "$TMP/models" --url "$TMP/localcopy/copy-model.gguf" >/dev/null || fail "local copy registration"
[ -f "$TMP/models/copy-model/maio.toml" ] && [ -f "$TMP/models/copy-model/copy-model.gguf" ] || fail "local copy was not placed"

mkdir -p "$TMP/inplace/keep-model"
cp "$TMP/fixtures/tiny.gguf" "$TMP/inplace/keep-model/x.gguf"
"$BIN" install keep-model "$TMP/inplace" --url "$TMP/inplace/keep-model/x.gguf" >/dev/null || fail "in-place registration"
grep -q 'path   = "x.gguf"' "$TMP/inplace/keep-model/maio.toml" || fail "in-place registration did not write a relative path"

set +e
"$BIN" install keep-model "$TMP/inplace" --url "$TMP/inplace/keep-model/x.gguf" >/dev/null 2>&1
code=$?
set -e
[ "$code" -eq 4 ] || fail "duplicate registration should exit 4, got $code"
pass "local registration (copy / in place / duplicate guard)"

echo "== 4. permission policy (non-root install to a system path must fail) =="
set +e
"$BIN" install perm-test /var/lib/maio/models --url "http://127.0.0.1:$HTTP_PORT/tiny.gguf" >/dev/null 2>"$TMP/err.log"
code=$?
set -e
[ "$code" -eq 5 ] || fail "non-root install to a system path should exit 5, got $code"
grep -q "requires root" "$TMP/err.log" || fail "missing permission hint"
pass "non-root install points at maio sudo install"

echo "== 5. service API =="
"$BIN" serve --port "$API_PORT" >"$TMP/serve.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 30); do
  curl -s -o /dev/null "http://127.0.0.1:$API_PORT/api/version" && break
  sleep 0.2
done
curl -s "http://127.0.0.1:$API_PORT/api/version" | grep -q "0.1.0-dev" || fail "/api/version"
curl -s "http://127.0.0.1:$API_PORT/api/tags" | grep -q "deepseek-v4.1" || fail "/api/tags"
pass "/api/version + /api/tags"

echo "== 6. delete =="
"$BIN" rm deepseek-v4.1 -y >/dev/null || fail "rm"
[ ! -d "$TMP/models/deepseek-v4.1" ] || fail "model directory was not deleted"
"$BIN" list | grep -q "deepseek-v4.1" && fail "deleted model is still listed"
pass "rm"

echo
echo "all checks passed"
