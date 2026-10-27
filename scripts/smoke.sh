#!/usr/bin/env bash
# maio V2 end-to-end smoke test: install / list / show / register / remove /
# API / doctor / logs. It uses a temporary directory and a local HTTP source,
# so no GPU, no root and no real model are required.
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

expect_exit() {
  local want="$1"; shift
  set +e
  "$@" >"$TMP/out.log" 2>"$TMP/err.log"
  local got=$?
  set -e
  [ "$got" -eq "$want" ] || { cat "$TMP/out.log" "$TMP/err.log" >&2; fail "expected exit $want, got $got: $*"; }
}

echo "== 1. fixtures =="
mkdir -p "$TMP/models" "$TMP/state" "$TMP/etc" "$TMP/fixtures"
python3 - "$TMP/fixtures/tiny.gguf" <<'FIXTURE'
import hashlib, struct, sys
def s(x):
    b = x.encode()
    return struct.pack('<Q', len(b)) + b
def kv_u32(key, value):
    return s(key) + struct.pack('<I', 4) + struct.pack('<I', value)
def kv_str(key, value):
    return s(key) + struct.pack('<I', 8) + s(value)
out = b'GGUF' + struct.pack('<I', 3) + struct.pack('<Q', 0) + struct.pack('<Q', 9)
out += kv_str('general.architecture', 'llama')
out += kv_str('general.name', 'tiny-test')
out += kv_u32('general.file_type', 15)
out += kv_u32('llama.context_length', 4096)
out += kv_u32('llama.block_count', 32)
out += kv_u32('llama.attention.head_count', 32)
out += kv_u32('llama.attention.head_count_kv', 8)
out += kv_u32('llama.embedding_length', 4096)
out += kv_str('llama.tokenizer.ggml.model', 'gpt2')
open(sys.argv[1], 'wb').write(out)
open(sys.argv[1] + '.sha256', 'w').write(hashlib.sha256(out).hexdigest() + '  tiny.gguf\n')
FIXTURE
pass "tiny GGUF fixture created"

cat > "$TMP/etc/maio.conf" <<EOF
boot        = 1
models_root = "$TMP/models"
state_dir   = "$TMP/state"
host        = "127.0.0.1"
port        = $API_PORT
mirror      = ""
llama_bin   = ""
llama_lib   = ""
ctx_size    = 0
auth        = "on"
api_key     = "0123456789abcdef0123456789abcdef"
rate_limit  = 0
EOF
export MAIO_CONF="$TMP/etc/maio.conf"
export XDG_CONFIG_HOME="$TMP/xdg"
KEY="0123456789abcdef0123456789abcdef"

echo "== 2. basic commands and the new sudo syntax =="
"$BIN" version >/dev/null || fail "maio version"
"$BIN" --help | grep -q "maio pull|install" || fail "help is missing the model commands"
expect_exit 2 "$BIN" sudo install x
grep -q "no longer supported" "$TMP/err.log" || fail "maio sudo migration message missing"
expect_exit 2 "$BIN" not-a-command
pass "version / help / maio sudo rejection / unknown command"

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

"$BIN" list | grep "deepseek-v4.1" >/dev/null || fail "list does not show the model"
"$BIN" list --wide | grep "4096" >/dev/null || fail "wide list is missing the context length"
"$BIN" show deepseek-v4.1 | grep "architecture: llama" >/dev/null || fail "show output is wrong"
"$BIN" show deepseek-v4.1 | grep "context     : 4096" >/dev/null || fail "show is missing the context"
pass "list / list --wide / show"

echo "== 4. error codes: missing model, duplicate, bad name, bad GGUF =="
expect_exit 3 "$BIN" show not-installed
expect_exit 3 "$BIN" run not-installed hi
expect_exit 2 "$BIN" install "bad\$name" "$TMP/models"
mkdir -p "$TMP/bad"
printf 'NOPE' > "$TMP/bad/bad.gguf"
expect_exit 7 "$BIN" install bad-gguf "$TMP/models" --url "$TMP/bad/bad.gguf"
grep -q "MAIO-E007" "$TMP/err.log" || fail "verification error code missing"
expect_exit 6 "$BIN" install missing-url "$TMP/models" --url "http://127.0.0.1:$HTTP_PORT/nope.gguf"
grep -q "MAIO-E006" "$TMP/err.log" || fail "download error code missing"
pass "not found / usage / verification / download error codes"

echo "== 5. local registration (copy / in place / duplicate guard) =="
mkdir -p "$TMP/localcopy"
cp "$TMP/fixtures/tiny.gguf" "$TMP/localcopy/copy-model.gguf"
"$BIN" install copy-model "$TMP/models" --url "$TMP/localcopy/copy-model.gguf" >/dev/null || fail "local copy registration"
[ -f "$TMP/models/copy-model/maio.toml" ] && [ -f "$TMP/models/copy-model/copy-model.gguf" ] || fail "local copy was not placed"

mkdir -p "$TMP/models/keep-model"
cp "$TMP/fixtures/tiny.gguf" "$TMP/models/keep-model/x.gguf"
"$BIN" install keep-model "$TMP/models" --url "$TMP/models/keep-model/x.gguf" >/dev/null || fail "in-place registration"
grep -q 'path   = "x.gguf"' "$TMP/models/keep-model/maio.toml" || fail "in-place registration did not write a relative path"

expect_exit 4 "$BIN" install keep-model "$TMP/models" --url "$TMP/models/keep-model/x.gguf"
pass "local registration (copy / in place / duplicate guard)"

"$BIN" cp copy-model copy-alias >/dev/null || fail "cp"
[ -f "$TMP/models/copy-alias/maio.toml" ] || fail "cp did not write maio.toml"
grep -q 'name   = "copy-alias"' "$TMP/models/copy-alias/maio.toml" || fail "cp name is wrong"
"$BIN" rm copy-alias -y >/dev/null || fail "rm of the copied model"
pass "cp (hardlink or copy) + cleanup"


echo "== 6. permission policy (non-root install to a system path must fail) =="
if [ "$(id -u)" -ne 0 ]; then
  expect_exit 5 "$BIN" install perm-test /var/lib/maio/models --url "http://127.0.0.1:$HTTP_PORT/tiny.gguf"
  grep -q "requires root" "$TMP/err.log" || fail "missing permission hint"
  grep -q "sudo maio install" "$TMP/err.log" || fail "permission hint does not mention sudo maio"
  pass "non-root install points at sudo maio install"
else
  pass "running as root; permission test skipped"
fi

echo "== 7. non-interactive confirmation and delete =="
expect_exit 2 "$BIN" rm deepseek-v4.1 < /dev/null
"$BIN" rm deepseek-v4.1 -y >/dev/null || fail "rm -y"
[ ! -d "$TMP/models/deepseek-v4.1" ] || fail "model directory was not deleted"
"$BIN" list | grep "deepseek-v4.1" >/dev/null && fail "deleted model is still listed"
pass "rm requires -y in scripts and deletes cleanly"

echo "== 8. service API =="
"$BIN" serve --port "$API_PORT" >"$TMP/serve.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 50); do
  curl -s -o /dev/null "http://127.0.0.1:$API_PORT/api/version" && break
  sleep 0.2
done
curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$API_PORT/api/tags" | grep -q 401 || fail "missing 401 for an unauthenticated request"
curl -s -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API_PORT/api/version" | grep -q "3.0.0" || fail "/api/version"
curl -s -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API_PORT/api/tags" | grep -q "copy-model" || fail "/api/tags"
curl -s -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API_PORT/api/tags" | grep -q "keep-model" || fail "/api/tags missing keep-model"
curl -s "http://127.0.0.1:$API_PORT/healthz" | grep -q '"ok"' || fail "/healthz"
curl -s -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API_PORT/metrics" | grep -q "maio_build_info" || fail "/metrics"
curl -s -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API_PORT/api/stats" | grep -q '"metrics"' || fail "/api/stats"
pass "/api/version + /api/tags + auth + healthz + metrics"

echo "== 9. doctor, logs and version JSON =="
set +e
"$BIN" doctor --json >"$TMP/doctor.json" 2>"$TMP/doctor.err"
DOC=$?
set -e
[ "$DOC" -eq 9 ] || fail "doctor should exit 9 when llama-server and the service are missing (got $DOC)"
python3 - "$TMP/doctor.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
assert d["version"], "missing version"
assert d["summary"]["failure"] >= 1, d["summary"]
names = {c["name"] for c in d["checks"]}
for want in ("gpu", "llama-server", "copy-model", "keep-model", "audit log"):
    assert want in names, (want, names)
PY
pass "doctor --json reports hardware, engine, models and logs"

"$BIN" version --json >"$TMP/version.json"
python3 - "$TMP/version.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
assert d["version"].startswith("3.0.0"), d
assert d["models_root"], d
PY
"$BIN" logs --path | grep "maio.log" >/dev/null || fail "logs --path"
"$BIN" logs --tail 200 | grep "install.done" >/dev/null || fail "install action missing from the audit log"
"$BIN" logs --tail 200 | grep "uninstall.done" >/dev/null || fail "uninstall action missing from the audit log"
"$BIN" logs --tail 5 --json | tail -n 1 | python3 -c 'import json,sys; json.loads(sys.stdin.read())' \
  || fail "logs --json is not JSON"
pass "version --json / logs / audit actions"

echo "== 10. config errors are reported, not ignored =="
cp "$TMP/etc/maio.conf" "$TMP/etc/bad.conf"
printf 'boott = 1\n' >> "$TMP/etc/bad.conf"
set +e
MAIO_CONF="$TMP/etc/bad.conf" "$BIN" list >/dev/null 2>"$TMP/badconf.err"
CODE=$?
set -e
[ "$CODE" -eq 10 ] || fail "unknown config key should exit 10 (got $CODE)"
grep -q "unknown key" "$TMP/badconf.err" || fail "unknown config key was not reported"
pass "unknown config keys are reported with exit code 10"

echo "== 11. V3 CLI surface =="
"$BIN" config show --json | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["values"]["max_concurrency"] == 8, d'
"$BIN" config get parallel | grep -q '^2$' || fail "config get parallel"
"$BIN" token --show | grep -q "$KEY" || fail "token --show"
"$BIN" token | grep -q '\*' || fail "token masking"
"$BIN" completion bash | grep -q '_maio' || fail "bash completion"
"$BIN" status --json | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["reachable"] is True, d'
"$BIN" stats --json | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["metrics"]["version"].startswith("3.0.0"), d'
"$BIN" ps --json | python3 -c 'import json,sys; assert isinstance(json.load(sys.stdin), list)'
pass "config / token / completion / status / stats / ps"

echo
echo "all checks passed"
