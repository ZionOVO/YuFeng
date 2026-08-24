#!/bin/sh
# 人工部署数据面的断网与缓存恢复演练；复用现有目标，不创建、删除或重建 Edge。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [ -f "$root/.env" ] && [ -z "${YUFENG_SKIP_DOTENV:-}" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$root/.env"
  set +a
fi

control_compose=${COMPOSE_FILE:-deploy/compose.yaml}
data_compose=${YUFENG_EDGE_COMPOSE_FILE:-deploy/compose.edge-modelside.yaml}
test_compose=${YUFENG_TEST_COMPOSE_FILE:-deploy/compose.test.yaml}
edge_name=${YUFENG_EDGE_CONTAINER:-yufeng-edge-1}
edge_admin_port=${YUFENG_EDGE_ADMIN_PORT:-19092}
mode=${1:-live}
brain_stopped=0

control() {
  docker compose -f "$control_compose" -f "$test_compose" "$@"
}

data() {
  docker compose -f "$control_compose" -f "$data_compose" -f "$test_compose" "$@"
}

wait_brain() {
  attempts=0
  until curl -fsS http://127.0.0.1:19090/readyz >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 60 ]; then
      control logs --tail=80 brain
      return 1
    fi
    sleep 2
  done
}

wait_edge_admin() {
  attempts=0
  until curl -fsS "http://127.0.0.1:${edge_admin_port}/ready" >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 60 ]; then
      docker logs --tail=80 "$edge_name" || true
      return 1
    fi
    sleep 1
  done
}

event_count() {
  control exec -T postgres psql -U yufeng -d yufeng -Atc 'SELECT count(*) FROM events'
}

distinct_event_count() {
  control exec -T postgres psql -U yufeng -d yufeng -Atc 'SELECT count(DISTINCT event_id) FROM events'
}

spool_lines() {
  docker exec "$edge_name" sh -c 'find /var/lib/yufeng/telemetry -type f -name "events-*.ndjson" -exec cat {} \; 2>/dev/null | wc -l' | tr -d '[:space:]'
}

wait_spool_empty() {
  attempts=0
  while [ "$(spool_lines)" != "0" ]; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 90 ]; then
      docker exec "$edge_name" find /var/lib/yufeng/telemetry -type f -print || true
      return 1
    fi
    sleep 2
  done
}

request_attack() {
  headers=$1
  code=$(curl -sS -D "$headers" -o /dev/null -w '%{http_code}' 'http://127.0.0.1:18080/api/items?id=1%20UNION%20SELECT%20resilience')
  case "$code" in
    200|403) ;;
    *) echo "攻击请求返回异常状态：$code"; return 1 ;;
  esac
}

generation_id() {
  awk 'BEGIN { IGNORECASE=1 } /^X-Yufeng-Generation-Id:/ { gsub("\r", "", $2); print $2 }' "$1"
}

restore_services() {
  status=$?
  trap - EXIT HUP INT TERM
  if [ "$brain_stopped" -eq 1 ]; then
    control start brain >/dev/null 2>&1 || true
  fi
  if [ -n "${tmp_dir:-}" ] && [ -d "$tmp_dir" ]; then
    rm -r "$tmp_dir"
  fi
  exit "$status"
}

if [ "$mode" = "static" ]; then
  go test ./lib/edgecore -count=1 -run '^TestReleaseSetInvalidGenerationKeepsCurrent$'
  go test ./cmd/yufeng-edge -count=1 -run 'TestGenerationCacheFallsBackWhenCurrentIsTampered|TestOfflineStartRequiresCache'
  go test ./lib/edgeclient -count=1 -run '^TestApplyUploadAckMixed$'
  echo "resilience static ok"
  exit 0
fi
if [ "$mode" != "live" ]; then
  echo "usage: $0 [static|live]" >&2
  exit 2
fi
command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 || { echo "Docker 不可用" >&2; exit 2; }
control ps --status running brain | grep -q brain || { echo "请先完成控制面部署" >&2; exit 2; }
[ "$(docker inspect -f '{{.State.Running}}' "$edge_name" 2>/dev/null || true)" = "true" ] || { echo "请先由技术人员启动 Edge" >&2; exit 2; }

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/yufeng-resilience.XXXXXX")
trap restore_services EXIT HUP INT TERM
baseline_headers="$tmp_dir/baseline.headers"
offline_headers="$tmp_dir/offline.headers"
restart_headers="$tmp_dir/restart.headers"

wait_brain
wait_edge_admin
wait_spool_empty
request_attack "$baseline_headers"
wait_spool_empty
baseline_generation=$(generation_id "$baseline_headers")
[ -n "$baseline_generation" ] || { echo "基准请求缺少已验证世代标识"; exit 1; }
baseline_events=$(event_count)
[ "$baseline_events" = "$(distinct_event_count)" ] || { echo "事件账在演练前已存在重复标识"; exit 1; }

echo "fault: stop Brain; Edge keeps serving the last verified generation"
control stop brain
brain_stopped=1
request_attack "$offline_headers"
[ "$(generation_id "$offline_headers")" = "$baseline_generation" ] || { echo "Brain 离线后 Edge 世代发生变化"; exit 1; }
docker exec "$edge_name" wget -qO- 'http://testapp-a:8080/api/items?page=direct-origin-rollback' | grep -q '"name":"app-a"' || {
  echo "受控防御网络内无法直连原始上游"
  exit 1
}

echo "operator action: restart Edge while Brain is offline; signed caches restore service"
data restart edge >/dev/null
wait_edge_admin
request_attack "$restart_headers"
[ "$(generation_id "$restart_headers")" = "$baseline_generation" ] || { echo "离线重启未恢复最后已验证世代"; exit 1; }
attempts=0
until [ "$(spool_lines)" -ge 2 ]; do
  attempts=$((attempts + 1))
  [ "$attempts" -lt 30 ] || { echo "离线请求未进入遥测缓冲"; exit 1; }
  sleep 1
done
spool_file=$(docker exec "$edge_name" sh -c 'find /var/lib/yufeng/telemetry -type f -name "events-*.ndjson" | sort | head -n 1')
docker exec "$edge_name" cp "$spool_file" /tmp/resilience-duplicate.ndjson

echo "fault recovery: Brain returns; buffered telemetry is idempotent"
control start brain
brain_stopped=0
wait_brain
expected_events=$((baseline_events + 2))
attempts=0
until [ "$(event_count)" = "$expected_events" ] && [ "$(distinct_event_count)" = "$expected_events" ]; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 90 ]; then
    echo "遥测恢复补传数量不正确：want=$expected_events got=$(event_count) distinct=$(distinct_event_count)"
    exit 1
  fi
  sleep 2
done
wait_spool_empty
spool_dir=$(docker exec "$edge_name" dirname "$spool_file")
docker exec "$edge_name" cp /tmp/resilience-duplicate.ndjson "$spool_dir/events-duplicate.ndjson"
wait_spool_empty
[ "$(event_count)" = "$expected_events" ] && [ "$(distinct_event_count)" = "$expected_events" ] || {
  echo "重复补传写入了第二份事件"
  exit 1
}
echo "resilience live ok"
