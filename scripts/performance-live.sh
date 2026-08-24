#!/bin/sh
# 模型旁路发布容量门禁；五个固定场景只在显式 live 模式运行定速时间窗。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mode=${1:-live}

if [ "$mode" = "static" ]; then
  go test ./scripts/performance-load ./deploy/testdata/upstream -count=1
  go test ./lib/edgecore -count=1 -run 'TestModelIngressDropsWhenFull|TestModelBypassRequestPathNeverWaitsForSidecar'
  go test ./components/modelside -count=1
  echo "performance static ok"
  exit 0
fi
if [ "$mode" != "live" ]; then
  echo "usage: $0 [static|live]" >&2
  exit 2
fi

result_dir=$(mktemp -d "${TMPDIR:-/tmp}/yufeng-model-bypass-performance.XXXXXX")
trap 'rm -r "$result_dir"' EXIT HUP INT TERM
scenario_report="$result_dir/scenarios.json"
budgets_report="$result_dir/budgets.json"

YUFENG_RUN_MODEL_BYPASS_PERFORMANCE=1 \
YUFENG_MODEL_BYPASS_REPORT="$scenario_report" \
  go test ./lib/edgecore -run '^TestModelBypassFiveScenarioCapacity$' -count=1 -v
go run ./scripts/performance-load -budgets > "$budgets_report"

python3 - "$scenario_report" "$budgets_report" "${YUFENG_PERFORMANCE_REPORT:-}" <<'PY'
import json
import pathlib
import platform
import sys

scenario_path, budget_path, output_path = sys.argv[1:4]
scenarios = json.loads(pathlib.Path(scenario_path).read_text(encoding="utf-8"))
budgets = json.loads(pathlib.Path(budget_path).read_text(encoding="utf-8"))
expected = [
    "bypass_disabled",
    "modelside_idle",
    "modelside_saturated",
    "brain_disconnected",
    "brain_disk_slow",
]
results = scenarios.get("scenarios") or []
if [result.get("name") for result in results] != expected:
    raise SystemExit("model bypass scenario set is incomplete")
target = int(budgets.get("edge_throughput_rps", 0))
p99_budget = int(budgets.get("model_bypass_p99_micros", 0))
if target != 2000 or p99_budget != 1000:
    raise SystemExit("frozen model bypass budgets are not 2000 requests per second and 1000 microseconds")
for result in results:
    if int(result.get("completed", 0)) < target:
        raise SystemExit(result["name"] + " did not complete the target request count")
    if float(result.get("throughput_requests_per_second", 0)) < target:
        raise SystemExit(result["name"] + " missed the throughput budget")
    if int(result.get("p99_increase_micros", 0)) > p99_budget:
        raise SystemExit(result["name"] + " exceeded the model bypass p99 budget")
if int(results[2].get("ingress_dropped", 0)) <= 0:
    raise SystemExit("saturated ModelSide did not exercise non-blocking Edge drops")
if int(results[3].get("result_upload_retries", 0)) <= 0 or int(results[3].get("result_depth", 0)) <= 0:
    raise SystemExit("disconnected Brain did not exercise the independent result queue")
if int(results[4].get("result_upload_retries", 0)) <= 0 or int(results[4].get("result_depth", 0)) <= 0:
    raise SystemExit("slow Brain disk did not exercise the independent result queue")

report = {
    "schema_version": "model-bypass-capacity/v1",
    "environment": {
        "operating_system": platform.system(),
        "architecture": platform.machine(),
        "go_version": budgets.get("go_version", ""),
    },
    "workload": {
        "request_path": "Canonicalize -> Inspect -> Gate -> non-blocking model ingress offer",
        "target_requests_per_second": target,
        "offered_requests_per_second": min(int(result["offered_requests_per_second"]) for result in results),
        "requests_per_scenario": min(int(result["completed"]) for result in results),
        "scenarios": expected,
    },
    "budgets": budgets,
    "model_bypass": scenarios,
    "throughput_budget_met": all(float(result["throughput_requests_per_second"]) >= target for result in results),
    "p99_budget_met": all(int(result["p99_increase_micros"]) <= p99_budget for result in results),
}
rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
print(rendered, end="")
if output_path:
    pathlib.Path(output_path).write_text(rendered, encoding="utf-8")
PY
