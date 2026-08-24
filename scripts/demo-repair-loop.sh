#!/bin/sh
# 演示修复循环验收脚本。
# 演示配置把影子观察与金丝雀门槛显式设为零；生产默认值由治理调度配置提供。
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [ -z "${YUFENG_TEST_DSN:-}" ]; then
  echo "YUFENG_TEST_DSN 未设置" >&2
  exit 1
fi
go test ./lib/brain -count=1 -timeout 120s -run 'TestDemoRepairLoopAllowsThenBlocksAttack'
