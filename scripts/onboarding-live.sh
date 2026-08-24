#!/bin/sh
# 人工部署 Edge 与 ModelSide 的交付门禁；控制面只签发规格和制品，不管理数据面进程。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [ -f "$root/.env" ] && [ -z "${YUFENG_SKIP_DOTENV:-}" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$root/.env"
  set +a
fi

control_compose=${COMPOSE_FILE:-"$root/deploy/compose.yaml"}
data_compose=${YUFENG_EDGE_COMPOSE_FILE:-"$root/deploy/compose.edge-modelside.yaml"}
test_compose=${YUFENG_TEST_COMPOSE_FILE:-"$root/deploy/compose.test.yaml"}
base=${YUFENG_BRAIN_URL:-https://127.0.0.1:9050}
admin=${YUFENG_ADMIN_URL:-http://127.0.0.1:19090}
admin_user=${YUFENG_ADMIN_USER:-admin}
admin_pass=${YUFENG_ADMIN_PASS:-}
model=${YUFENG_CHAT_MODEL:-grok-4-1-fast-non-reasoning}
model_url=${YUFENG_MODEL_BASE_URL:-https://api.x.ai/v1}
unit_id=${YUFENG_EDGE_UNIT:-local-1}
asset_id=${YUFENG_EDGE_ASSET:-asset-local-1}
modelside_id=${YUFENG_MODELSIDE_ID:-${unit_id}-modelside}
weights_dir=${YUFENG_MODELSIDE_WEIGHTS_DIR:-}
upstream=${YUFENG_TEST_UPSTREAM:-http://testapp-a:8080}
edge_admin_port=${YUFENG_EDGE_ADMIN_PORT:-19092}
mode=${1:-live}

die() {
  echo "onboarding-live: $*" >&2
  exit 1
}

scan_compose() {
  python3 - "$control_compose" "$data_compose" <<'PY'
import sys
from pathlib import Path

control_path, data_path = map(Path, sys.argv[1:3])
control = control_path.read_text(encoding="utf-8")
data = data_path.read_text(encoding="utf-8")


def services(raw: str) -> list[str]:
    names: list[str] = []
    inside = False
    for line in raw.splitlines():
        stripped = line.strip()
        if not inside:
            if stripped == "services:":
                inside = True
            continue
        if line and line[0] not in " \t":
            break
        if line.startswith("  ") and not line.startswith("   ") and stripped.endswith(":"):
            names.append(stripped[:-1])
    return names


control_services = services(control)
data_services = services(data)
expected_control = ["postgres", "traffic-role", "keys", "signer", "brain", "jarvis", "agentd"]
expected_data = ["modelside", "edge"]
if control_services != expected_control:
    raise SystemExit(f"control services={control_services} want {expected_control}")
if data_services != expected_data:
    raise SystemExit(f"manual data services={data_services} want {expected_data}")
if "/var/run/docker.sock" in control + data:
    raise SystemExit("shipped compose files must not expose the Docker socket")
for required in (
    "-modelside-token-file",
    "deploy/edge.Dockerfile",
    "components/modelside/Dockerfile",
    "unix:///run/yufeng/modelside.sock",
    "YUFENG_BRAIN_URL",
    "YUFENG_EDGE_UNIT",
    "YUFENG_MODELSIDE_ID",
    "http://127.0.0.1:19092/ready",
):
    if required not in control + data:
        raise SystemExit("manual deployment compose is missing " + required)
print("compose static ok")
PY
}

keycheck() {
  if [ -z "${YUFENG_MODEL_API_KEY:-}" ]; then
    echo "未设置模型密钥，活栈模型连通性标为人工门禁"
    exit 2
  fi
  echo "model key present (not printed)"
}

rpc() {
  path=$1
  if [ "$#" -ge 2 ] && [ -n "$2" ]; then
    body=$2
  else
    body='{}'
  fi
  token=${3:-}
  idempotent=${4:-0}
  python3 - "$base" "$path" "$body" "$token" "$idempotent" <<'PY'
import json
import ssl
import sys
import urllib.error
import urllib.request
import uuid

base, path, raw, token, idempotent = sys.argv[1:6]
headers = {"Content-Type": "application/json", "Connect-Protocol-Version": "1"}
if token:
    headers["Authorization"] = "Bearer " + token
if idempotent == "1":
    headers["Idempotency-Key"] = str(uuid.uuid4())
request = urllib.request.Request(base.rstrip("/") + path, data=raw.encode(), method="POST", headers=headers)
context = ssl._create_unverified_context()
try:
    with urllib.request.urlopen(request, context=context, timeout=90) as response:
        payload = response.read().decode()
        print(json.dumps({"http": response.status, "body": json.loads(payload) if payload else {}}))
except urllib.error.HTTPError as error:
    payload = error.read().decode()
    try:
        decoded = json.loads(payload) if payload else {}
    except json.JSONDecodeError:
        decoded = {"raw": payload}
    print(json.dumps({"http": error.code, "body": decoded}))
PY
}

require_ok() {
  label=$1
  python3 -c 'import json,sys; value=json.load(sys.stdin); assert value["http"] == 200, value; print(sys.argv[1] + " ok")' "$label"
}

login() {
  body=$(ADMIN_USER="$admin_user" ADMIN_PASS="$admin_pass" python3 -c 'import json,os; print(json.dumps({"username":os.environ["ADMIN_USER"],"password":os.environ["ADMIN_PASS"]}))')
  rpc "/yufeng.auth.v1.AuthService/Login" "$body" | python3 -c 'import json,sys; value=json.load(sys.stdin); assert value["http"] == 200, value; print(value["body"]["token"])'
}

deployment_body() {
  python3 - "$unit_id" "$asset_id" "$upstream" <<'PY'
import json
import sys

unit_id, asset_id, upstream = sys.argv[1:4]
print(json.dumps({
    "unitId": unit_id,
    "assetId": asset_id,
    "posture": "INGRESS_POSTURE_REVERSE_PROXY",
    "trafficKey": "enterprise-site",
    "trustedProxyCidrs": ["0.0.0.0/0"],
    "reverseProxy": {"listenAddress": ":18080", "upstreamUrl": upstream},
    "modelProfile": {
        "profileId": "http-threat-model/default",
        "modelGroup": "http-threat",
        "modelType": "PVM",
        "modelVersion": "v1",
        "alertThreshold": 0.9,
        "reviewFloor": 0.5,
        "reviewWindowSeconds": 300,
        "maxReviewPerUnit": 4,
        "maxReviewPerRoute": 1,
        "dedupeRule": "MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE",
        "allowedHeaders": ["accept", "content-type", "user-agent"],
        "maxBodyBytes": 65536,
        "reviewNewRoutes": True,
        "reviewInsufficientCoverage": True,
    },
}, separators=(",", ":")))
PY
}

wait_for_edge() {
  token=$1
  python3 - "$base" "$token" "$unit_id" "$asset_id" "$edge_admin_port" <<'PY'
import datetime
import json
import ssl
import sys
import time
import urllib.request

base, token, unit_id, asset_id, edge_admin_port = sys.argv[1:6]
headers = {
    "Content-Type": "application/json",
    "Connect-Protocol-Version": "1",
    "Authorization": "Bearer " + token,
}
context = ssl._create_unverified_context()
deadline = time.time() + 120
last = {}


def rpc(path, body):
    request = urllib.request.Request(
        base.rstrip("/") + path,
        data=json.dumps(body, separators=(",", ":")).encode(), method="POST", headers=headers,
    )
    with urllib.request.urlopen(request, context=context, timeout=30) as response:
        return json.loads(response.read().decode())


def local_ready():
    request = urllib.request.Request("http://127.0.0.1:" + edge_admin_port + "/ready")
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.loads(response.read().decode())


def recent_heartbeat(raw):
    if not raw:
        return False
    value = raw[:-1] + "+00:00" if raw.endswith("Z") else raw
    heartbeat = datetime.datetime.fromisoformat(value)
    age = (datetime.datetime.now(datetime.timezone.utc) - heartbeat).total_seconds()
    return -5 <= age <= 90


def completed_current_generation(onboarding):
    detail = rpc("/yufeng.asset.v1.AssetService/GetAsset", {"assetId": asset_id})
    units = (detail.get("asset") or {}).get("units") or []
    unit = next((value for value in units if value.get("unitId") == unit_id and str(value.get("kind", "")).lower() == "edge"), None)
    if unit is None or not recent_heartbeat(unit.get("lastHeartbeatAt", "")):
        return False
    local = local_ready()
    current_id = unit.get("currentGenerationId", "")
    current_sequence = int(unit.get("currentGenerationSeq") or 0)
    expected_sequence = int(onboarding.get("expectedGenerationSeq") or 0)
    expected_listen_plan_version = int(onboarding.get("expectedListenPlanVersion") or 0)
    return (
        local.get("ready") is True
        and current_id != ""
        and expected_sequence > 0
        and current_sequence >= expected_sequence
        and local.get("generation_id", "") == current_id
        and int(local.get("generation_seq") or 0) == current_sequence
        and int(local.get("listen_plan_version") or 0) == expected_listen_plan_version
    )


while time.time() < deadline:
    try:
        last = rpc("/yufeng.onboarding.v1.OnboardingService/GetOnboarding", {})
        if last.get("localUnitId") != unit_id or last.get("localAssetId") != asset_id:
            time.sleep(2)
            continue
        if last.get("edgeReady") is True:
            print("manual Edge registration and signed artifact load ok")
            raise SystemExit(0)
        if last.get("state") == "ONBOARDING_STATE_COMPLETED" and completed_current_generation(last):
            print("completed Edge heartbeat and current signed artifact load ok")
            raise SystemExit(0)
    except Exception as error:
        last = {"error": str(error)}
    time.sleep(2)
raise SystemExit("manual Edge did not become ready: " + json.dumps(last, sort_keys=True))
PY
}

if [ "$mode" = "static" ]; then
  scan_compose
  if [ "${YUFENG_SKIP_DEPLOY_GO_TESTS:-}" != "1" ]; then
    go test ./deploy -run 'TestControlPlaneComposeHasNoEdgeLifecycleAuthority|TestEdgeModelSideComposeIsAnExplicitManualDataPlane|TestNativeEdgeAndModelSideServicesRemainOperatorManaged' -count=1
  fi
  echo "onboarding static ok"
  exit 0
fi
if [ "$mode" = "keycheck" ]; then
  keycheck
  exit 0
fi
if [ "$mode" != "live" ]; then
  echo "usage: $0 [static|keycheck|live]" >&2
  exit 64
fi

[ -n "$admin_pass" ] || { echo "YUFENG_ADMIN_PASS is required" >&2; exit 2; }
keycheck
[ -n "$weights_dir" ] && [ -d "$weights_dir" ] || { echo "YUFENG_MODELSIDE_WEIGHTS_DIR must name an installed immutable weight directory" >&2; exit 2; }
command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 || { echo "Docker 不可用" >&2; exit 2; }
scan_compose

if [ "${YUFENG_LIVE_RESET:-}" = "1" ]; then
  echo "explicit reset: remove the selected local delivery stack and volumes"
  YUFENG_EDGE_UNIT="$unit_id" YUFENG_MODELSIDE_ID="$modelside_id" YUFENG_MODELSIDE_WEIGHTS_DIR="$weights_dir" \
    docker compose -f "$control_compose" -f "$data_compose" -f "$test_compose" down -v --remove-orphans
fi

echo "start control plane"
make compose-up
docker compose -f "$control_compose" -f "$test_compose" up -d --build testapp-a testapp-b
curl -fsS "$admin/readyz" >/dev/null || die "Brain is not ready"
token=$(login)
onboarding=$(rpc "/yufeng.onboarding.v1.OnboardingService/GetOnboarding" "{}" "$token")
state=$(printf '%s' "$onboarding" | python3 -c 'import json,sys; print(json.load(sys.stdin)["body"].get("state", ""))')
if [ "$state" = "ONBOARDING_STATE_COMPLETED" ]; then
  wait_for_edge "$token"
  curl -fsS "http://127.0.0.1:18080/echo" >/dev/null
  echo "completed onboarding and manually managed data plane verified"
  exit 0
fi

model_body=$(MODEL_URL="$model_url" MODEL_SECRET="$YUFENG_MODEL_API_KEY" MODEL_NAME="$model" python3 -c 'import json,os; print(json.dumps({"baseUrl":os.environ["MODEL_URL"],"secret":os.environ["MODEL_SECRET"],"model":os.environ["MODEL_NAME"]}))')
rpc "/yufeng.onboarding.v1.OnboardingService/PutModelConfig" "$model_body" "$token" 1 | require_ok "model configuration"
rpc "/yufeng.onboarding.v1.OnboardingService/TestModelConnectivity" "{}" "$token" 1 | require_ok "model connectivity"
specification=$(deployment_body)
specification_result=$(rpc "/yufeng.onboarding.v1.OnboardingService/PutDeploymentSpecification" "$specification" "$token" 1)
printf '%s' "$specification_result" | require_ok "deterministic deployment specification"
printf '%s' "$specification_result" | python3 -c 'import json,sys; value=json.load(sys.stdin)["body"]; assert value.get("listenPlanVersion") and value.get("generationId") and value.get("generationSeq"), value'

echo "operator action: start the separately delivered Edge and ModelSide services"
export YUFENG_EDGE_UNIT="$unit_id"
export YUFENG_MODELSIDE_ID="$modelside_id"
export YUFENG_MODELSIDE_WEIGHTS_DIR="$weights_dir"
docker compose -f "$control_compose" -f "$data_compose" -f "$test_compose" up -d --build modelside edge
attempt=0
until curl -fsS "http://127.0.0.1:${edge_admin_port}/ready" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 120 ]; then
    docker compose -f "$control_compose" -f "$data_compose" logs --tail=100 edge modelside
    die "manually started Edge did not become locally ready"
  fi
  sleep 1
done
wait_for_edge "$token"
upstream_name=$(curl -fsS http://127.0.0.1:18080/echo | python3 -c 'import json,sys; print(json.load(sys.stdin).get("name", ""))')
[ "$upstream_name" = "app-a" ] || die "reverse proxy returned unexpected upstream $upstream_name"

operator="liveop$(date +%s)"
operator_password=Operator12345
create_body=$(OPERATOR="$operator" OPERATOR_PASSWORD="$operator_password" python3 -c 'import json,os; print(json.dumps({"username":os.environ["OPERATOR"],"password":os.environ["OPERATOR_PASSWORD"],"displayName":os.environ["OPERATOR"],"role":"USER_ROLE_OPERATOR"}))')
operator_id=$(rpc "/yufeng.user.v1.UserService/CreateUser" "$create_body" "$token" 1 | python3 -c 'import json,sys; value=json.load(sys.stdin); assert value["http"] == 200, value; user=value["body"].get("user") or value["body"]; print(user.get("userId") or user.get("user_id") or "")')
[ -n "$operator_id" ] || die "operator creation returned no user identifier"
grant_body=$(OPERATOR_ID="$operator_id" ASSET_ID="$asset_id" python3 -c 'import json,os; print(json.dumps({"subjectUserId":os.environ["OPERATOR_ID"],"tools":["govern.promote_enforce","govern.retire"],"bindings":[{"kind":"asset","id":os.environ["ASSET_ID"]}]}))')
rpc "/yufeng.grant.v1.GrantService/PutGrant" "$grant_body" "$token" 1 | require_ok "operator grant"
rpc "/yufeng.onboarding.v1.OnboardingService/CompleteOnboarding" "{}" "$token" 1 | require_ok "onboarding completion"
final_state=$(rpc "/yufeng.onboarding.v1.OnboardingService/GetOnboarding" "{}" "$token" | python3 -c 'import json,sys; print(json.load(sys.stdin)["body"].get("state", ""))')
[ "$final_state" = "ONBOARDING_STATE_COMPLETED" ] || die "onboarding state is $final_state"
echo "onboarding live ok: Brain signed the plan; the operator-owned Edge and ModelSide loaded it"
