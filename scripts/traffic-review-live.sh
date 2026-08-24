#!/bin/sh
# 单站点试点流量审查活栈门禁；复用已完成引导的部署并在验收后关闭临时能力。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [ -f "$root/.env" ] && [ -z "${YUFENG_SKIP_DOTENV:-}" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$root/.env"
  set +a
fi

compose_file=${COMPOSE_FILE:-deploy/compose.yaml}
data_compose_file=${YUFENG_EDGE_COMPOSE_FILE:-deploy/compose.edge-modelside.yaml}
test_compose_file=${YUFENG_TEST_COMPOSE_FILE:-deploy/compose.test.yaml}
base=${YUFENG_BRAIN_URL:-https://127.0.0.1:9050}
admin_user=${YUFENG_ADMIN_USER:-admin}
admin_pass=${YUFENG_ADMIN_PASS:-}
edge_name=${YUFENG_EDGE_CONTAINER:-yufeng-edge-1}
edge_admin_port=${YUFENG_EDGE_ADMIN_PORT:-19092}
mode=${1:-live}

compose() {
  docker compose -f "$compose_file" -f "$data_compose_file" -f "$test_compose_file" "$@"
}

if [ "$mode" = "static" ]; then
  go test ./lib/brain -count=1 -run 'TestTrafficReviewPolicyPublishesSignedGenerationOneLevelAtATime|TestReviewCandidatesUseDurableOutboxAndBoundedRepresentatives|TestManagedAgentProfileDrivesDurableCaseDelegation|TestSubmitEvidenceBundleDurablyContinuesCaseReviewOnce|TestShadowCandidateCoordinatorCreatesRealShadowRelease'
  go test ./cmd/yufeng-edge -count=1 -run 'TestReviewUploadLoopRetriesWindowsBeforeUploadingCandidates|TestDrainTrafficReviewRetriesSnapshotAfterSpoolBecomesAvailable|TestBuildEvidenceBundle'
  go test ./agents/runtime -count=1 -run 'TestHandleCaseReview'
  echo "traffic review static ok"
  exit 0
fi
if [ "$mode" != "live" ]; then
  echo "usage: $0 [static|live]" >&2
  exit 64
fi
if [ -z "$admin_pass" ]; then
  echo "YUFENG_ADMIN_PASS is required" >&2
  exit 2
fi
if [ -z "${YUFENG_MODEL_API_KEY:-}" ] || [ -z "${YUFENG_MODEL_BASE_URL:-}" ] || [ -z "${YUFENG_CHAT_MODEL:-}" ]; then
  echo "本机 .env 必须提供真实模型地址、模型名和密钥" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "Docker 不可用" >&2
  exit 2
fi
for service in brain jarvis agentd; do
  if ! compose ps --status running "$service" | grep -q "$service"; then
    echo "服务未运行：$service" >&2
    exit 2
  fi
done
if [ "$(docker inspect -f '{{.State.Running}}' "$edge_name" 2>/dev/null || true)" != "true" ]; then
  echo "边缘容器未运行" >&2
  exit 2
fi

export YUFENG_BRAIN_URL="$base"
export YUFENG_ADMIN_USER="$admin_user"
export YUFENG_ADMIN_PASS="$admin_pass"
export YUFENG_EDGE_ADMIN_PORT="$edge_admin_port"

python3 <<'PY'
import datetime
import json
import os
import secrets
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

base = os.environ["YUFENG_BRAIN_URL"].rstrip("/")
admin_user = os.environ["YUFENG_ADMIN_USER"]
admin_pass = os.environ["YUFENG_ADMIN_PASS"]
model_base_url = os.environ["YUFENG_MODEL_BASE_URL"]
model_name = os.environ["YUFENG_CHAT_MODEL"]
model_key = os.environ["YUFENG_MODEL_API_KEY"]
edge_admin_port = os.environ["YUFENG_EDGE_ADMIN_PORT"]
report_path = os.environ.get("YUFENG_TRAFFIC_REVIEW_REPORT", "")
tls = ssl._create_unverified_context()


class RPCFailure(RuntimeError):
    pass


def rpc(path, body, token="", idempotent=False, timeout=90):
    headers = {
        "Content-Type": "application/json",
        "Connect-Protocol-Version": "1",
    }
    if token:
        headers["Authorization"] = "Bearer " + token
    if idempotent:
        headers["Idempotency-Key"] = str(uuid.uuid4())
    request = urllib.request.Request(
        base + path,
        data=json.dumps(body, separators=(",", ":")).encode(),
        method="POST",
        headers=headers,
    )
    try:
        with urllib.request.urlopen(request, context=tls, timeout=timeout) as response:
            raw = response.read().decode()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as error:
        raw = error.read().decode()
        try:
            detail = json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            detail = {"message": "non-json response"}
        code = detail.get("code", "unknown")
        message = detail.get("message", "request failed")
        raise RPCFailure(f"{path} failed: HTTP {error.code} {code} {message}") from error


def wait_for(label, timeout, probe, interval=2):
    deadline = time.time() + timeout
    next_progress = time.time() + 30
    last = None
    while time.time() < deadline:
        last = probe()
        if last:
            print(label + " ok", flush=True)
            return last
        if time.time() >= next_progress:
            print(label + " still waiting", flush=True)
            next_progress = time.time() + 30
        time.sleep(interval)
    raise RuntimeError(label + " timed out; last=" + json.dumps(last, sort_keys=True, ensure_ascii=False))


def parse_time(value):
    if not value:
        return None
    if value.endswith("Z"):
        value = value[:-1] + "+00:00"
    return datetime.datetime.fromisoformat(value)


def local_edge_ready():
    request = urllib.request.Request("http://127.0.0.1:" + edge_admin_port + "/ready")
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.loads(response.read().decode())


def current_edge_generation_loaded(token, onboarding):
    asset_id = onboarding.get("localAssetId", "")
    unit_id = onboarding.get("localUnitId", "")
    if not asset_id or not unit_id:
        return False
    detail = rpc(
        "/yufeng.asset.v1.AssetService/GetAsset",
        {"assetId": asset_id},
        token,
    )
    units = (detail.get("asset") or {}).get("units") or []
    unit = next(
        (
            value
            for value in units
            if value.get("unitId") == unit_id
            and str(value.get("kind", "")).lower() in ("edge", "unit_kind_edge")
        ),
        None,
    )
    if unit is None:
        return False
    heartbeat = parse_time(unit.get("lastHeartbeatAt", ""))
    if heartbeat is None:
        return False
    age = (datetime.datetime.now(datetime.timezone.utc) - heartbeat).total_seconds()
    if age < -5 or age > 90:
        return False
    local = local_edge_ready()
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


def mode_of(status):
    return (status.get("policy") or {}).get("mode", "TRAFFIC_REVIEW_MODE_OFF")


def get_policy(token, asset_id):
    return rpc(
        "/yufeng.asset.v1.AssetService/GetTrafficReviewPolicy",
        {"assetId": asset_id},
        token,
    ).get("status") or {}


def update_policy(token, asset_id, mode):
    current = get_policy(token, asset_id)
    if mode_of(current) == mode:
        return current
    response = rpc(
        "/yufeng.asset.v1.AssetService/UpdateTrafficReviewPolicy",
        {
            "assetId": asset_id,
            "mode": mode,
            "expectedGenerationId": current.get("generationId", ""),
        },
        token,
        idempotent=True,
    )
    status = response.get("status") or {}
    if mode_of(status) != mode or not status.get("generationId"):
        raise RuntimeError("traffic review policy update returned an incomplete generation")
    return status


def wait_edge_generation(token, asset_id, status):
    target_sequence = int(status.get("generationSeq") or 0)
    target_id = status.get("generationId", "")

    def loaded():
        body = rpc(
            "/yufeng.asset.v1.AssetService/GetAsset",
            {"assetId": asset_id},
            token,
        )
        units = (body.get("asset") or {}).get("units") or []
        edges = [unit for unit in units if str(unit.get("kind", "")).lower() == "edge"]
        if not edges:
            return None
        if all(int(unit.get("currentGenerationSeq") or 0) >= target_sequence for unit in edges):
            return {"generationId": target_id, "generationSeq": target_sequence, "edgeCount": len(edges)}
        return None

    return wait_for("edge loaded generation " + str(target_sequence), 90, loaded)


admin_token = ""
asset_id = ""
profile_id = ""
profile_name = ""
case_id = ""
shadow_release_id = ""
retire_user_id = ""
retire_grant_id = ""
retire_token = ""
success = False
primary_error = None
assigned_run_id = ""
finding_disposition = ""


def ensure_retire_operator():
    global retire_user_id, retire_grant_id, retire_token
    username = "trafficreview" + uuid.uuid4().hex[:16]
    password = "TrafficReview!A1" + secrets.token_hex(12)
    created = rpc(
        "/yufeng.user.v1.UserService/CreateUser",
        {
            "username": username,
            "password": password,
            "displayName": "流量审查活栈清理操作员",
            "role": "USER_ROLE_OPERATOR",
        },
        admin_token,
        idempotent=True,
    )
    retire_user_id = (created.get("user") or {}).get("userId", "")
    if not retire_user_id:
        raise RuntimeError("temporary retirement operator has no user id")
    grant = rpc(
        "/yufeng.grant.v1.GrantService/PutGrant",
        {
            "subjectUserId": retire_user_id,
            "tools": ["govern.retire"],
            "bindings": [{"kind": "asset", "id": asset_id}],
        },
        admin_token,
        idempotent=True,
    )
    retire_grant_id = (grant.get("grant") or {}).get("grantId", "")
    login = rpc(
        "/yufeng.auth.v1.AuthService/Login",
        {"username": username, "password": password},
    )
    retire_token = login.get("token", "")
    if not retire_token:
        raise RuntimeError("temporary retirement operator login returned no token")


try:
    login = rpc(
        "/yufeng.auth.v1.AuthService/Login",
        {"username": admin_user, "password": admin_pass},
    )
    admin_token = login.get("token", "")
    if not admin_token:
        raise RuntimeError("administrator login returned no token")

    onboarding = rpc(
        "/yufeng.onboarding.v1.OnboardingService/GetOnboarding",
        {},
        admin_token,
    )
    if onboarding.get("state") != "ONBOARDING_STATE_COMPLETED":
        raise RuntimeError("onboarding must be completed before traffic review evidence")
    asset_id = onboarding.get("localAssetId", "")
    if not asset_id or onboarding.get("jarvisOnline") is not True or not current_edge_generation_loaded(admin_token, onboarding):
        raise RuntimeError("completed onboarding is missing the live local asset, manually deployed Edge, or Jarvis")

    rpc(
        "/yufeng.model.v1.ModelGatewayService/UpdateModelGateway",
        {"baseUrl": model_base_url, "secret": model_key, "model": model_name},
        admin_token,
        idempotent=True,
    )
    probe = rpc(
        "/yufeng.model.v1.ModelGatewayService/ProbeModelGateway",
        {},
        admin_token,
        timeout=120,
    )
    if probe.get("ok") is not True:
        raise RuntimeError("real model gateway probe did not succeed")
    print("traffic review real model gateway ok", flush=True)

    profile_name = "L1 流量审查活栈 " + uuid.uuid4().hex[:12]
    created_profile = rpc(
        "/yufeng.agent.v1.AgentProfileService/CreateAgentProfile",
        {
            "displayName": profile_name,
            "tools": ["case.get", "case.request_evidence", "run.create"],
            "bindings": [{"kind": "asset", "id": asset_id}],
        },
        admin_token,
        idempotent=True,
    )
    profile = created_profile.get("profile") or {}
    profile_id = profile.get("agentId", "")
    if not profile_id or profile.get("state") != "AGENT_PROFILE_STATE_ENABLED":
        raise RuntimeError("traffic review agent profile was not enabled")
    if profile.get("tools") != ["case.get", "case.request_evidence", "run.create"]:
        raise RuntimeError("traffic review agent profile toolset changed")
    print("traffic review bounded agent profile ok", flush=True)

    current = get_policy(admin_token, asset_id)
    if current.get("edgeSupported") is not True:
        raise RuntimeError("bound edge does not advertise traffic review protocol support")
    if mode_of(current) != "TRAFFIC_REVIEW_MODE_OFF":
        off = update_policy(admin_token, asset_id, "TRAFFIC_REVIEW_MODE_OFF")
        wait_edge_generation(admin_token, asset_id, off)

    for target_mode in (
        "TRAFFIC_REVIEW_MODE_STATISTICS_ONLY",
        "TRAFFIC_REVIEW_MODE_REDACTED_CASES",
        "TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL",
        "TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES",
    ):
        status = update_policy(admin_token, asset_id, target_mode)
        wait_edge_generation(admin_token, asset_id, status)
        if mode_of(status) != target_mode:
            raise RuntimeError("traffic review mode did not advance exactly one level")
    print("traffic review signed policy progression ok", flush=True)

    probe_started = datetime.datetime.now(datetime.timezone.utc)
    route = "/traffic-review-live/probe-" + uuid.uuid4().hex[:20]
    query = urllib.parse.urlencode({"id": "1 UNION SELECT password FROM users"})
    request = urllib.request.Request("http://127.0.0.1:18080" + route + "?" + query)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            traffic_status = response.status
            response.read()
    except urllib.error.HTTPError as error:
        traffic_status = error.code
        error.read()
    if traffic_status not in (200, 403):
        raise RuntimeError("real traffic review probe returned HTTP " + str(traffic_status))
    print("traffic review real request accepted for observation", flush=True)

    window_end = (int(time.time()) // 300 + 1) * 300
    case_timeout = max(150, window_end - time.time() + 150)

    def new_case():
        listed = rpc(
            "/yufeng.case.v1.CaseService/ListCases",
            {"assetId": asset_id, "moduleId": "traffic-interception", "pageSize": 200},
            admin_token,
        )
        for item in listed.get("cases") or []:
            created = parse_time(item.get("createdAt", ""))
            routes = {representative.get("routeTemplate", "") for representative in item.get("representatives") or []}
            if created and created >= probe_started - datetime.timedelta(seconds=5) and route in routes and item.get("assignedAgentId") == profile_id:
                return item
        return None

    traffic_case = wait_for("five-minute traffic window and investigation case", case_timeout, new_case)
    case_id = traffic_case.get("caseId", "")
    if not case_id or traffic_case.get("assignedAgentId") != profile_id:
        raise RuntimeError("traffic case was not assigned to the bounded agent profile")

    def pending_approval():
        activities = rpc(
            "/yufeng.case.v1.CaseService/PollCaseActivities",
            {"caseId": case_id, "afterSequence": "0", "longPollSeconds": 0},
            admin_token,
        ).get("activities") or []
        for activity in activities:
            if activity.get("kind") == "CASE_ACTIVITY_KIND_EVIDENCE_REQUESTED" and activity.get("refId"):
                return {"approvalId": activity["refId"], "activities": activities}
        return None

    approval = wait_for("evidence approval request", 120, pending_approval)
    approval_id = approval["approvalId"]
    approval_view = rpc(
        "/yufeng.agent.v1.AgentInteractionService/GetApproval",
        {"approvalId": approval_id},
        admin_token,
    ).get("approval") or {}
    if approval_view.get("kind") != "APPROVAL_KIND_EVIDENCE" or approval_view.get("caseId") != case_id or approval_view.get("assetId") != asset_id:
        raise RuntimeError("evidence approval scope does not match the traffic case")
    decided = rpc(
        "/yufeng.agent.v1.AgentInteractionService/DecideApproval",
        {"approvalId": approval_id, "approved": True, "reason": "L1 单站点活栈证据批准"},
        admin_token,
        idempotent=True,
    )
    if decided.get("state") != "approved":
        raise RuntimeError("evidence approval did not enter approved state")
    print("traffic review one-time evidence approval ok", flush=True)

    def shadow_case():
        item = rpc(
            "/yufeng.case.v1.CaseService/GetCase",
            {"caseId": case_id},
            admin_token,
        ).get("case") or {}
        state = item.get("state", "")
        if state in (
            "INVESTIGATION_CASE_STATE_FAILED",
            "INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED",
            "INVESTIGATION_CASE_STATE_RESOLVED",
        ):
            raise RuntimeError("traffic investigation entered terminal state " + state)
        if state == "INVESTIGATION_CASE_STATE_SHADOW_OBSERVING" and item.get("shadowReleaseId"):
            return item
        return None

    completed_case = wait_for("agentd real-model investigation and Shadow coordination", 240, shadow_case)
    shadow_release_id = completed_case.get("shadowReleaseId", "")
    finding = completed_case.get("finding") or {}
    assigned_run_id = completed_case.get("assignedRunId", "")
    finding_disposition = finding.get("disposition", "")
    if not assigned_run_id or not finding_disposition:
        raise RuntimeError("traffic case does not prove a short-lived run and typed model finding")

    release = rpc(
        "/yufeng.govern.v1.GovernService/GetRelease",
        {"releaseId": shadow_release_id},
        admin_token,
    ).get("release") or {}
    if release.get("state") != "RELEASE_STATE_SHADOW" or release.get("canaryStartedAt") or release.get("enforcedAt"):
        raise RuntimeError("traffic review release advanced beyond Shadow")
    timeline = rpc(
        "/yufeng.govern.v1.GovernService/GetReleaseTimeline",
        {"releaseId": shadow_release_id},
        admin_token,
    ).get("entries") or []
    forbidden_states = {"RELEASE_STATE_CANARY", "RELEASE_STATE_ENFORCE"}
    if any(entry.get("toState") in forbidden_states for entry in timeline):
        raise RuntimeError("traffic review release timeline contains Canary or Enforce")

    activities = rpc(
        "/yufeng.case.v1.CaseService/PollCaseActivities",
        {"caseId": case_id, "afterSequence": "0", "longPollSeconds": 0},
        admin_token,
    ).get("activities") or []
    activity_kinds = {activity.get("kind") for activity in activities}
    required_kinds = {
        "CASE_ACTIVITY_KIND_EVIDENCE_REQUESTED",
        "CASE_ACTIVITY_KIND_APPROVAL_DECIDED",
        "CASE_ACTIVITY_KIND_RUN_PROGRESS",
        "CASE_ACTIVITY_KIND_FINDING",
        "CASE_ACTIVITY_KIND_SHADOW_CANDIDATE",
    }
    if not required_kinds.issubset(activity_kinds):
        raise RuntimeError("traffic case activity ledger is incomplete")

    profiles = rpc(
        "/yufeng.agent.v1.AgentProfileService/ListAgentProfiles",
        {"pageSize": 200},
        admin_token,
    ).get("profiles") or []
    completed_profile = next((item for item in profiles if item.get("agentId") == profile_id), None)
    if not completed_profile or completed_profile.get("lastWorkerId") != "agentd-central" or not completed_profile.get("lastRunAt"):
        raise RuntimeError("central agentd did not record the ephemeral traffic investigation run")
    print("traffic review typed finding and Shadow-only release ok", flush=True)
    success = True
except Exception as error:
    primary_error = error
finally:
    cleanup_errors = []

    def cleanup(label, action):
        try:
            action()
            print(label + " ok", flush=True)
        except Exception as error:
            cleanup_errors.append(label + ": " + str(error))

    if admin_token and asset_id and shadow_release_id:
        def retire_shadow():
            ensure_retire_operator()
            retired = rpc(
                "/yufeng.govern.v1.GovernService/RetireRelease",
                {"releaseId": shadow_release_id, "reason": "L1 traffic review live cleanup"},
                retire_token,
                idempotent=True,
            ).get("release") or {}
            if retired.get("state") != "RELEASE_STATE_RETIRED":
                raise RuntimeError("Shadow release did not retire")
        cleanup("traffic review Shadow retirement", retire_shadow)

    if admin_token and asset_id:
        def disable_policy():
            off = update_policy(admin_token, asset_id, "TRAFFIC_REVIEW_MODE_OFF")
            if off.get("generationId"):
                wait_edge_generation(admin_token, asset_id, off)
        cleanup("traffic review policy shutdown", disable_policy)

    if admin_token and profile_id:
        def disable_profile():
            updated = rpc(
                "/yufeng.agent.v1.AgentProfileService/UpdateAgentProfile",
                {
                    "agentId": profile_id,
                    "displayName": profile_name,
                    "state": "AGENT_PROFILE_STATE_DISABLED",
                    "tools": ["case.get", "case.request_evidence", "run.create"],
                    "bindings": [{"kind": "asset", "id": asset_id}],
                },
                admin_token,
                idempotent=True,
            ).get("profile") or {}
            if updated.get("state") != "AGENT_PROFILE_STATE_DISABLED":
                raise RuntimeError("traffic review profile did not disable")
        cleanup("traffic review agent profile disable", disable_profile)

    if admin_token and case_id:
        def close_case():
            current = rpc(
                "/yufeng.case.v1.CaseService/GetCase",
                {"caseId": case_id},
                admin_token,
            ).get("case") or {}
            if current.get("state") == "INVESTIGATION_CASE_STATE_RESOLVED":
                if success and current.get("resolution") != "CASE_RESOLUTION_SHADOW_PUBLISHED":
                    raise RuntimeError("traffic review case has an unexpected existing resolution")
                return
            resolution = "CASE_RESOLUTION_SHADOW_PUBLISHED" if success else "CASE_RESOLUTION_FAILED"
            closed = rpc(
                "/yufeng.case.v1.CaseService/ResolveCase",
                {"caseId": case_id, "resolution": resolution, "note": "L1 流量审查活栈验收收口"},
                admin_token,
                idempotent=True,
            ).get("case") or {}
            if closed.get("state") != "INVESTIGATION_CASE_STATE_RESOLVED" or closed.get("resolution") != resolution:
                raise RuntimeError("traffic review case did not reach the requested audited resolution")
        cleanup("traffic review case audit closure", close_case)

    if admin_token and retire_grant_id:
        cleanup(
            "traffic review temporary grant revoke",
            lambda: rpc(
                "/yufeng.grant.v1.GrantService/RevokeGrant",
                {"grantId": retire_grant_id},
                admin_token,
                idempotent=True,
            ),
        )
    if admin_token and retire_user_id:
        cleanup(
            "traffic review temporary operator disable",
            lambda: rpc(
                "/yufeng.user.v1.UserService/DeleteUser",
                {"userId": retire_user_id},
                admin_token,
                idempotent=True,
            ),
        )

    if primary_error is not None:
        if cleanup_errors:
            raise RuntimeError(str(primary_error) + "; cleanup failures: " + "; ".join(cleanup_errors)) from primary_error
        raise primary_error
    if cleanup_errors:
        raise RuntimeError("traffic review cleanup failures: " + "; ".join(cleanup_errors))

if report_path:
    with open(report_path, "w", encoding="utf-8") as handle:
        json.dump({
            "result": "passed",
            "asset_id": asset_id,
            "agent_profile_id": profile_id,
            "case_id": case_id,
            "assigned_run_id": assigned_run_id,
            "worker_id": "agentd-central",
            "finding_disposition": finding_disposition,
            "shadow_release_id": shadow_release_id,
            "verified_release_state": "RELEASE_STATE_SHADOW",
            "cleanup": {
                "release": "RELEASE_STATE_RETIRED",
                "policy": "TRAFFIC_REVIEW_MODE_OFF",
                "profile": "AGENT_PROFILE_STATE_DISABLED",
                "case": "INVESTIGATION_CASE_STATE_RESOLVED",
            },
        }, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")
print("traffic review live ok")
PY
