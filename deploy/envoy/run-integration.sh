#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
compose_file="$repo_dir/deploy/envoy/compose.integration.yaml"
project_name="yufeng-envoy-integration"
gateway_url="http://127.0.0.1:${YUFENG_ENVOY_PORT:-10000}"

cleanup() {
  docker compose -p "$project_name" -f "$compose_file" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

status() {
  curl -sS --max-time 3 -o /dev/null -w '%{http_code}' "$@"
}

assert_status() {
  want=$1
  shift
  got=$(status "$@")
  if [ "$got" != "$want" ]; then
    echo "status=$got want=$want request=$*" >&2
    exit 1
  fi
}

wait_gateway() {
  attempts=0
  while [ "$attempts" -lt 60 ]; do
    if [ "$(status "$gateway_url/healthz" 2>/dev/null || true)" = "200" ]; then
      return
    fi
    attempts=$((attempts + 1))
    sleep 1
  done
  docker compose -p "$project_name" -f "$compose_file" logs --tail=100 >&2
  echo "envoy integration gateway did not become ready" >&2
  exit 1
}

docker compose -p "$project_name" -f "$compose_file" up -d --build
wait_gateway
docker compose -p "$project_name" -f "$compose_file" exec envoy /usr/local/bin/envoy --version

assert_status 200 "$gateway_url/allow"
assert_status 403 "$gateway_url/deny"
assert_status 200 -X POST "$gateway_url/body-required"
assert_status 403 -X POST --data-binary deny "$gateway_url/body-required"
assert_status 200 -X POST \
  -H 'Content-Type: text/plain' \
  -H 'Authorization: Bearer business-secret' \
  -H 'Cookie: business=session' \
  --data-binary payload \
  "$gateway_url/required-headers?first=1&first=2"
partial_status=$(dd if=/dev/zero bs=65537 count=1 2>/dev/null | tr '\000' x | status -X POST --data-binary @- "$gateway_url/body-required")
if [ "$partial_status" != "403" ]; then
  echo "partial body status=$partial_status want=403" >&2
  exit 1
fi

docker compose -p "$project_name" -f "$compose_file" restart yufeng-authz >/dev/null
wait_gateway
i=0
while [ "$i" -lt 19 ]; do
  assert_status 200 "$gateway_url/allow"
  i=$((i + 1))
done
assert_status 200 "$gateway_url/slow"
assert_status 200 "$gateway_url/allow"
assert_status 503 "$gateway_url/allow"

sleep 11
assert_status 200 "$gateway_url/allow"
sleep 31
assert_status 200 "$gateway_url/allow"
assert_status 200 "$gateway_url/allow"

docker compose -p "$project_name" -f "$compose_file" stop yufeng-authz >/dev/null
assert_status 503 "$gateway_url/allow"

echo "envoy external authorization integration passed"
