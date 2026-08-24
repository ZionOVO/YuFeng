#!/bin/sh
# 生产活路径：无策略攻击 200 且入队 DETECTED_UNMITIGATED（聚合一条）；
# 随后走 GrantService 提案 → enforce 403 → 退休放行，跑两遍。
# 依赖已启动的中台、边缘与 PostgreSQL；不走演示修复循环。
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

EDGE_ADDR="${YUFENG_EDGE_ADDR:-127.0.0.1:18080}"
PGURL="${YUFENG_PG_URL:-postgres://localhost:5432/yufeng?sslmode=disable}"
ASSET_ID="${YUFENG_ASSET_ID:-edge-e2e}"
JARVIS="${YUFENG_JARVIS_AGENT_ID:-jarvis-1}"
COMPOSE_FILE="${YUFENG_COMPOSE_FILE:-}"
ADMIN_USER="${YUFENG_ADMIN_USER:-admin}"
ADMIN_PASS="${YUFENG_ADMIN_PASS:-}"

if [ -z "$ADMIN_PASS" ]; then
  echo "YUFENG_ADMIN_PASS is required" >&2
  exit 2
fi

wait_http() {
  want=$1
  url=$2
  i=0
  code=000
  while [ "$i" -lt 20 ]; do
    code=$(curl -sS -o /dev/null -w '%{http_code}' "$url" || echo 000)
    if [ "$code" = "$want" ]; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "wait $url want $want got $code" >&2
  return 1
}

sql() {
  if [ -n "$COMPOSE_FILE" ]; then
    docker compose -f "$COMPOSE_FILE" exec -T postgres psql -U yufeng -d yufeng -Atc "$1"
  else
    psql "$PGURL" -Atc "$1"
  fi
}

code=$(curl -sS -o /dev/null -w '%{http_code}' "http://$EDGE_ADDR/api/items?id=1+UNION+SELECT+pw")
if [ "$code" != "200" ]; then
  echo "no-policy attack want 200 got $code" >&2
  exit 1
fi
code=$(curl -sS -o /dev/null -w '%{http_code}' "http://$EDGE_ADDR/api/items?page=2")
if [ "$code" != "200" ]; then
  echo "normal want 200 got $code" >&2
  exit 1
fi

# 遥测上传周期 1s，多等几拍。
i=0
while [ "$i" -lt 15 ]; do
  n=$(sql "SELECT count(*) FROM agent_instructions WHERE kind='EVENT_TRIAGE' AND agent_id='$JARVIS'")
  if [ "${n:-0}" -ge 1 ]; then
    break
  fi
  i=$((i + 1))
  sleep 1
done

reason=$(sql "SELECT payload->>'triageReason' FROM events
  WHERE asset_id='$ASSET_ID' AND payload->'detections' IS NOT NULL
    AND jsonb_array_length(payload->'detections') > 0
  ORDER BY occurred_at DESC LIMIT 1")
ref=$(sql "SELECT payload_ref FROM agent_instructions
  WHERE kind='EVENT_TRIAGE' AND agent_id='$JARVIS' ORDER BY created_at DESC LIMIT 1")
instr=$(sql "SELECT count(*) FROM agent_instructions
  WHERE kind='EVENT_TRIAGE' AND agent_id='$JARVIS'")

echo "attack=200 normal=200 triageReason=$reason payload_ref=$ref instructions=$instr"
if [ "$reason" != "TRIAGE_REASON_DETECTED_UNMITIGATED" ]; then
  echo "want DETECTED_UNMITIGATED, got $reason" >&2
  exit 1
fi
if [ "${instr:-0}" -lt 1 ] || [ -z "$ref" ]; then
  echo "missing EVENT_TRIAGE cluster payload_ref" >&2
  exit 1
fi
if [ -n "${YUFENG_BRAIN_ADDR:-}" ]; then
  if [ -z "${YUFENG_TLS_CA:-}" ] && [ -n "$COMPOSE_FILE" ] && [ -f .tmp/compose-tls/ca.crt ]; then
    YUFENG_TLS_CA=.tmp/compose-tls/ca.crt
    YUFENG_TLS_CERT=.tmp/compose-tls/client.crt
    YUFENG_TLS_KEY=.tmp/compose-tls/client.key
  fi
  curl_tls=""
  brain_http="$YUFENG_BRAIN_ADDR"
  case "$brain_http" in
    http://*|https://*) ;;
    *)
      if [ -n "${YUFENG_TLS_CA:-}" ]; then brain_http="https://$brain_http"; else brain_http="http://$brain_http"; fi
      ;;
  esac
  if [ -n "${YUFENG_TLS_CA:-}" ]; then
    curl_tls="--cacert $YUFENG_TLS_CA --cert $YUFENG_TLS_CERT --key $YUFENG_TLS_KEY"
  fi
  token=$(curl -sS $curl_tls "$brain_http/yufeng.auth.v1.AuthService/Login" \
    -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  events=$(curl -sS $curl_tls "$brain_http/yufeng.console.v1.ConsoleService/ListEvents" \
    -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
    -H "Authorization: Bearer $token" -d '{"pageSize":5}')
  echo "$events" | grep -q '"events"' || { echo "ConsoleService ListEvents failed: $events" >&2; exit 1; }
  echo "console.ListEvents ok"
fi

if [ "${YUFENG_SKIP_ENFORCE:-0}" != "1" ]; then
  if [ -z "${YUFENG_TLS_CA:-}" ] && [ -n "$COMPOSE_FILE" ] && [ -f .tmp/compose-tls/ca.crt ]; then
    YUFENG_TLS_CA=.tmp/compose-tls/ca.crt
    YUFENG_TLS_CERT=.tmp/compose-tls/client.crt
    YUFENG_TLS_KEY=.tmp/compose-tls/client.key
    export YUFENG_TLS_CA YUFENG_TLS_CERT YUFENG_TLS_KEY
  fi
  if [ -n "${YUFENG_TLS_CA:-}" ]; then
    BRAIN="${YUFENG_BRAIN_ADDR:-https://127.0.0.1:9050}"
  else
    BRAIN="${YUFENG_BRAIN_ADDR:-127.0.0.1:9050}"
  fi
  case "$BRAIN" in
    http://*|https://*) BRAIN_URL="$BRAIN" ;;
    *) BRAIN_URL="http://$BRAIN" ;;
  esac
  yfctl="${YFCTL:-}"
  if [ -z "$yfctl" ]; then
    mkdir -p .tmp
    yfctl=".tmp/yfctl-prod"
    go build -o "$yfctl" ./cmd/yfctl
  fi
  tls_flags=""
  if [ -n "${YUFENG_TLS_CA:-}" ]; then
    tls_flags="-tls-ca $YUFENG_TLS_CA -tls-cert $YUFENG_TLS_CERT -tls-key $YUFENG_TLS_KEY"
  fi
  pass=1
  while [ "$pass" -le 2 ]; do
    out=$("$yfctl" policy-enforce -brain "$BRAIN_URL" -username "$ADMIN_USER" -password "$ADMIN_PASS" -asset "$ASSET_ID" $tls_flags)
    echo "$out"
    rel=$(printf '%s\n' "$out" | sed -n 's/.*release=\([^ ]*\).*/\1/p')
    if [ -z "$rel" ]; then
      echo "policy-enforce missing release id" >&2
      exit 1
    fi
    wait_http 403 "http://$EDGE_ADDR/api/items?id=1+UNION+SELECT+pw"
    code=$(curl -sS -o /dev/null -w '%{http_code}' "http://$EDGE_ADDR/api/items?page=2")
    if [ "$code" != "200" ]; then
      echo "benign under enforce want 200 got $code" >&2
      exit 1
    fi
    traces=""
    key=""
    i=0
    while [ "$i" -lt 15 ]; do
      traces=$(sql "SELECT release_traces::text FROM events
        WHERE asset_id='$ASSET_ID' AND verdict='block'
          AND release_traces::text LIKE '%$rel%'
        ORDER BY occurred_at DESC LIMIT 1")
      key=$(sql "SELECT payload->'detections'->0->'key'->>'ruleId' FROM events
        WHERE asset_id='$ASSET_ID' AND verdict='block'
          AND release_traces::text LIKE '%$rel%'
        ORDER BY occurred_at DESC LIMIT 1")
      if [ -n "$traces" ] && [ -n "$key" ]; then
        break
      fi
      i=$((i + 1))
      sleep 1
    done
    echo "block traces=$traces key=$key"
    echo "$traces" | grep -q "$rel" || { echo "block event missing release_traces for $rel: $traces" >&2; exit 1; }
    if [ -z "$key" ]; then
      echo "block event missing detections[].key" >&2
      exit 1
    fi
    if [ -z "${curl_tls:-}" ] && [ -n "${YUFENG_TLS_CA:-}" ]; then
      curl_tls="--cacert $YUFENG_TLS_CA --cert $YUFENG_TLS_CERT --key $YUFENG_TLS_KEY"
    fi
    if [ -n "${BRAIN_URL:-}" ]; then
      jarvis_brain="$BRAIN_URL"
      raw=$(python3 -c 'import os; print(os.urandom(32).hex())')
      th=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(sys.argv[1].encode()).hexdigest())' "$raw")
      sql "INSERT INTO agent_tokens(token_hash, agent_id, kind, expires_at)
        VALUES('$th','$JARVIS','access', now()+interval '1 hour')" >/dev/null
      sql "UPDATE agent_instructions SET status='pending', lease_id='', lease_expires_at=NULL
        WHERE agent_id='$JARVIS' AND kind='EVENT_TRIAGE'" >/dev/null
      poll=$(curl -sS $curl_tls "$jarvis_brain/yufeng.agent.v1.AgentControlService/PollInstructions" \
        -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
        -H "Authorization: Bearer $raw" \
        -d "{\"agentId\":\"$JARVIS\",\"longPollSeconds\":2}")
      cap=$(printf '%s\n' "$poll" | python3 -c 'import json,sys,re
s=sys.stdin.read()
cap=""
try:
  o=json.loads(s)
  ins=o.get("instructions") or []
  if ins: cap=ins[0].get("capabilityToken") or ""
except Exception:
  m=re.search(r"capabilityToken\":\"([^\"]+)\"", s)
  if m: cap=m.group(1)
print(cap)')
      if [ -z "$cap" ]; then
        echo "missing jarvis capability token from poll: $poll" >&2
        exit 1
      fi
      promo=$(curl -sS $curl_tls "$jarvis_brain/yufeng.toolgateway.v1.ToolGatewayService/InvokeTool" \
        -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
        -H "Authorization: Bearer $raw" \
        -H "X-Yufeng-Capability: Bearer $cap" \
        -d "{\"toolName\":\"govern.promote_enforce\",\"argsJson\":\"{\\\"releaseId\\\":\\\"$rel\\\"}\"}")
      echo "jarvis promote=$promo"
      echo "$promo" | grep -qi 'permission_denied\|PermissionDenied\|not allowed' || {
        echo "jarvis promote_* want permission_denied, got $promo" >&2
        exit 1
      }
    fi
    "$yfctl" retire -brain "$BRAIN_URL" -username "$ADMIN_USER" -password "$ADMIN_PASS" -asset "$ASSET_ID" -release "$rel" $tls_flags
    wait_http 200 "http://$EDGE_ADDR/api/items?id=1+UNION+SELECT+pw"
    echo "enforce-retire pass $pass ok release=$rel"
    pass=$((pass + 1))
  done
fi
echo "production live ok"
