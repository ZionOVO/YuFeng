#!/bin/sh
# 过滤后的 go test 故障脚本，不是 compose 全栈 + 贾维斯证明。
# 人机交付活栈门禁只认 scripts/onboarding-live.sh（make compose-live）。可选探测已在跑的 compose，不代替活栈门禁。
# 每种注入都断言期望行为，不允许只打故障不校验。
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

echo "fault: tampered release cache must not load"
go test ./cmd/yufeng-edge -count=1 -timeout 60s -run 'TestReleaseCacheRejectsTampered|TestOfflineStartRequiresCache|TestCursorPersist'

echo "fault: incomplete/skip/disk-full generation keeps previous"
go test ./lib/edgecore -count=1 -timeout 60s -run 'TestGeneration'

echo "fault: outbox restart redelivers once; typed model result remains idempotent"
go test ./lib/brain -count=1 -timeout 180s -run 'TestOutboxDeliverRestart|TestClusterHundred|TestModelResultIngestionIsAtomicIdempotentAndBounded'

echo "fault: spool over capacity increments drop metric"
go test ./lib/edgeclient -count=1 -timeout 60s -run 'TestSpoolRejectsWhenOverCapacity|TestApplyUploadAck'

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  if docker compose -f deploy/compose.yaml ps --status running 2>/dev/null | grep -q brain; then
    echo "fault: restart postgres; brain must recover to ready"
    docker compose -f deploy/compose.yaml restart postgres
    i=0
    until curl -fsS http://127.0.0.1:19090/readyz >/dev/null 2>&1; do
      i=$((i+1))
      if [ "$i" -ge 40 ]; then
        echo "brain not ready after postgres restart"
        docker compose -f deploy/compose.yaml logs --tail=80 brain
        exit 1
      fi
      sleep 2
    done
    echo "fault: postgres restart recovered"
    echo "fault: stop brain; edge must keep serving last generation"
    docker compose -f deploy/compose.yaml stop brain
    code=$(curl -sS -o /dev/null -w "%{http_code}" "http://127.0.0.1:18080/api/items?page=2" || true)
    brain_container=$(docker compose -f deploy/compose.yaml ps -aq brain)
    if [ -z "$brain_container" ]; then
      echo "brain container is missing"
      exit 1
    fi
    # 流量角色初始化是一次性依赖；故障恢复必须启动原容器，不能重新装配并触碰秘密文件。
    docker start "$brain_container" >/dev/null
    i=0
    until curl -fsS http://127.0.0.1:19090/readyz >/dev/null 2>&1; do
      i=$((i+1))
      if [ "$i" -ge 40 ]; then echo "brain did not return"; exit 1; fi
      sleep 2
    done
    if [ "$code" != "200" ]; then
      echo "edge must stay 200 while brain is down, got $code"
      exit 1
    fi
    echo "fault: brain down/up recovered; edge stayed 200"
  else
    echo "fault: compose stack not running; skip live postgres restart"
  fi
else
  echo "fault: docker unavailable; skip live compose faults"
fi

echo "fault injection end-to-end ok"
