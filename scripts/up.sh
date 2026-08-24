#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PGURL="${YUFENG_PG_URL:-postgres://localhost:5432/yufeng?sslmode=disable}"
BRAIN_ADDR="${YUFENG_BRAIN_ADDR:-127.0.0.1:9050}"
BRAIN_ADMIN="${YUFENG_BRAIN_ADMIN:-127.0.0.1:19090}"
EDGE_ADDR="${YUFENG_EDGE_ADDR:-127.0.0.1:18080}"
EDGE_ADMIN="${YUFENG_EDGE_ADMIN:-127.0.0.1:19091}"
DATA_DIR="${YUFENG_DATA_DIR:-.tmp/up-data}"
ADMIN_USER="${YUFENG_ADMIN_USER:-admin}"
ADMIN_PASS="${YUFENG_ADMIN_PASS:-Admin12345}"
UNIT_BOOTSTRAP_TOKEN="${YUFENG_UNIT_BOOTSTRAP_TOKEN:-dev-unit-bootstrap-token}"
ASSET_ID="${YUFENG_ASSET_ID:-edge-e2e}"
BIN=.tmp/up-bin
mkdir -p "$BIN" "$DATA_DIR"

go build -tags yufeng_dev -o "$BIN/yufeng-brain" ./cmd/yufeng-brain
go build -tags yufeng_dev -o "$BIN/yufeng-edge" ./cmd/yufeng-edge
go build -tags yufeng_dev -o "$BIN/yfctl" ./cmd/yfctl

make demo-init >/dev/null
if [ ! -s .demo/source-hmac.key ]; then
  umask 077
  head -c 32 /dev/urandom > .demo/source-hmac.key
fi
printf '%s\n' "$UNIT_BOOTSTRAP_TOKEN" >.demo/unit-bootstrap.token
chmod 600 .demo/unit-bootstrap.token

cleanup() {
  kill "${BRAIN_PID:-}" "${EDGE_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

BRAIN_ARGS=(-dsn "$PGURL" -addr "$BRAIN_ADDR" -admin-addr "$BRAIN_ADMIN" \
  -dev-insecure \
  -bootstrap-admin-user "$ADMIN_USER" -bootstrap-admin-pass "$ADMIN_PASS" \
  -unit-bootstrap-token "$UNIT_BOOTSTRAP_TOKEN" \
  -signing-key .demo/dev.key.hex)
if [ "${YUFENG_DEMO_TRIAGE:-1}" != "0" ]; then
  BRAIN_ARGS+=(-demo-triage)
fi
if [ "${YUFENG_DEMO_TRIAGE:-1}" = "0" ]; then
  BRAIN_ARGS+=(-nats-port "${YUFENG_NATS_PORT:-4322}")
fi
"$BIN/yufeng-brain" "${BRAIN_ARGS[@]}" >.tmp/up-brain.log 2>&1 &
BRAIN_PID=$!

for i in $(seq 1 30); do
  if curl -fsS "http://$BRAIN_ADMIN/readyz" >/dev/null 2>&1; then break; fi
  sleep 1
done

"$BIN/yufeng-edge" -addr "$EDGE_ADDR" -admin-addr "$EDGE_ADMIN" \
  -upstream builtin \
  -pubkey .demo/pubkey.hex -brain "http://$BRAIN_ADDR" -unit "$ASSET_ID" \
  -dev-insecure \
  -bootstrap-token-file .demo/unit-bootstrap.token \
  -source-hmac-key .demo/source-hmac.key \
  -data-dir "$DATA_DIR" -spool-dir "$DATA_DIR/spool" \
  >.tmp/up-edge.log 2>&1 &
EDGE_PID=$!

# 等 edge 完成注册并创建资产后，再走发布管道。
sleep 3
if [ "${YUFENG_SKIP_PUBLISH:-0}" != "1" ]; then
  TOKEN="$("$BIN/yfctl" login -brain "http://$BRAIN_ADDR" -username "$ADMIN_USER" -password "$ADMIN_PASS")"
  for i in $(seq 1 10); do
    if "$BIN/yfctl" publish -brain "http://$BRAIN_ADDR" -token "$TOKEN" -asset "$ASSET_ID" -payload .demo/artifacts/demo-rules.json; then
      break
    fi
    sleep 2
  done
fi

echo "brain: http://$BRAIN_ADDR"
echo "edge:  http://$EDGE_ADDR"
echo "攻击请求: curl 'http://$EDGE_ADDR/api/items?id=1+UNION+SELECT+pw'"
echo "正常请求: curl 'http://$EDGE_ADDR/api/items?page=2'"
echo "日志: .tmp/up-brain.log .tmp/up-edge.log"
echo "Ctrl-C 退出"
wait
