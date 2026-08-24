#!/bin/sh
# Edge 模型输入缓存窗口发布容量门禁；真实 Coraza 负载矩阵只在显式 live 模式运行。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mode=${1:-live}

if [ "$mode" = "static" ]; then
  go test ./scripts/performance-load ./deploy/testdata/upstream -count=1
  go test ./lib/edgecore -count=1 -run 'TestModelIngressDropsWhenFull|TestModelIngressWindowNeverBlocksRequestPath'
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
  go test ./lib/edgecore -run '^TestModelIngressWindowCapacityMatrix$' -count=1 -v
go run ./scripts/performance-load -budgets > "$budgets_report"

python3 - "$scenario_report" "$budgets_report" "${YUFENG_PERFORMANCE_REPORT:-}" <<'PY'
import json
import pathlib
import platform
import sys

scenario_path, budget_path, output_path = sys.argv[1:4]
scenarios = json.loads(pathlib.Path(scenario_path).read_text(encoding="utf-8"))
budgets = json.loads(pathlib.Path(budget_path).read_text(encoding="utf-8"))
results = scenarios.get("scenarios") or []
if scenarios.get("schema_version") != "model-ingress-window-capacity/v2":
    raise SystemExit("model ingress window report schema is invalid")
if not scenarios.get("qualification_run") or not scenarios.get("real_coraza"):
    raise SystemExit("capacity evidence must be a real Coraza qualification run")
repeats = int(scenarios.get("repeats", 0))
measurement = float(scenarios.get("measurement_seconds", 0))
if repeats < 3 or measurement < 60:
    raise SystemExit("capacity evidence requires three repeats of at least 60 seconds")
target = int(budgets.get("edge_throughput_rps", 0))
p99_budget = int(budgets.get("model_bypass_p99_micros", 0))
cpu_budget = float(budgets.get("model_bypass_cpu_percent", 0))
rss_budget = int(budgets.get("edge_memory_bytes", 0))
if target != 2000 or p99_budget != 1000 or cpu_budget != 5 or rss_budget != 512 * 1024 * 1024:
    raise SystemExit("frozen model ingress budgets are not 2000 rps, 1 ms, 5 CPU points and 512 MiB")
loads = ["modelside_idle", "modelside_stable", "modelside_full", "modelside_unreachable"]
bodies = ["small", "near_inspection_limit"]
windows = ["default", "local_hard_limit"]
expected = {
    (repeat, body, window, load)
    for repeat in range(1, repeats + 1)
    for body in bodies
    for window in windows
    for load in loads
}
actual = {
    (int(result.get("repeat", 0)), result.get("body"), result.get("window"), result.get("load"))
    for result in results
    if result.get("load") != "bypass_disabled"
}
baselines = [result for result in results if result.get("load") == "bypass_disabled"]
if actual != expected or len(baselines) != repeats * len(bodies):
    raise SystemExit("model ingress window load, body, window or repeat matrix is incomplete")
for result in results:
    if int(result.get("completed", 0)) < target:
        raise SystemExit(result["name"] + " did not complete the target request count")
    if int(result.get("load_generator_dropped", 0)) != 0:
        raise SystemExit(result["name"] + " dropped scheduled requests in the load generator")
    if float(result.get("throughput_requests_per_second", 0)) < target:
        raise SystemExit(result["name"] + " missed the throughput budget")
    if int(result.get("p99_increase_micros", 0)) > p99_budget:
        raise SystemExit(result["name"] + " exceeded the model bypass p99 budget")
    if float(result.get("cpu_percent_increase", 0)) > cpu_budget:
        raise SystemExit(result["name"] + " exceeded the model bypass CPU budget")
    if int(result.get("resident_bytes", 0)) > rss_budget:
        raise SystemExit(result["name"] + " exceeded the Edge resident memory budget")
for result in results:
    if result.get("load") == "modelside_full" and int(result.get("modelside_rejected", 0)) <= 0:
        raise SystemExit(result["name"] + " did not exercise ModelSide rejection")
    if result.get("load") == "modelside_unreachable" and int(result.get("transport_failed", 0)) <= 0:
        raise SystemExit(result["name"] + " did not exercise transport failure")

report = {
    "schema_version": "model-ingress-window-capacity-report/v2",
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
        "loads": loads,
        "bodies": bodies,
        "windows": windows,
        "measurement_seconds": measurement,
        "repeats": repeats,
    },
    "budgets": budgets,
    "model_bypass": scenarios,
    "throughput_budget_met": all(float(result["throughput_requests_per_second"]) >= target for result in results),
    "p99_budget_met": all(int(result["p99_increase_micros"]) <= p99_budget for result in results),
    "cpu_budget_met": all(float(result["cpu_percent_increase"]) <= cpu_budget for result in results),
    "resident_memory_budget_met": all(int(result["resident_bytes"]) <= rss_budget for result in results),
}
rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
print(rendered, end="")
if output_path:
    pathlib.Path(output_path).write_text(rendered, encoding="utf-8")
PY
