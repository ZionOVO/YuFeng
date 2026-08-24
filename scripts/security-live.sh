#!/bin/sh
# 企业试点安全负向演练；要求已完成真实目标部署，不创建或输出生产秘密。
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
modelside_name=${YUFENG_MODELSIDE_CONTAINER:-yufeng-modelside-1}
mode=${1:-live}

compose() {
  docker compose -f "$compose_file" -f "$data_compose_file" -f "$test_compose_file" "$@"
}

if [ "$mode" = "static" ]; then
  go test ./lib/brain -count=1 -run 'TestGovernWriteGrantAndIdempotency|TestRegisterCannotHijackExistingUnit|TestDualTokenGatewayTable|TestInvokeToolIdempotencyNoDoubleBudget|TestProductionAgentTokensRequireAndPinClientCertificate|TestProductionTriageCompilesOnePinnedShadowPolicy|TestTrafficPoolUsesRestrictedRole|TestWriteRPCRoleAndBindingsTable|TestRedact'
  go test ./lib/kernel -count=1 -run 'TestValidateProductionTLS|TestValidateProductionMTLS|TestValidateProductionSigner|TestMTLSClientRequiredOnRealServer'
  go test ./lib/dataplane -count=1 -run 'TestSupervisorOnlyProjectsManuallyStartedEdgeReadiness|TestSupervisorExposesNoLifecycleMutationEndpoint'
  go test ./lib/edgecore -count=1 -run 'TestTrafficEventPseudonymizesClientAddress|TestTrafficEventExcludesRequestSecrets'
  go test ./lib/brain -count=1 -run 'TestValidateResultAgainstSignedProfile|TestModelResultIngestionIsAtomicIdempotentAndBounded'
  go test ./deploy -count=1 -run 'TestControlPlaneComposeHasNoEdgeLifecycleAuthority|TestControlPlaneComposeTLSPrivateKeysHaveSinglePurposeVisibility|TestEdgeModelSideComposeIsAnExplicitManualDataPlane'
  echo "security static ok"
  exit 0
fi
if [ "$mode" != "live" ]; then
  echo "usage: $0 [static|live]" >&2
  exit 2
fi
if [ -z "$admin_pass" ]; then
  echo "YUFENG_ADMIN_PASS is required" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "Docker 不可用" >&2
  exit 2
fi
if ! compose ps --status running brain | grep -q brain; then
  echo "请先完成真实目标部署" >&2
  exit 2
fi
if [ "$(docker inspect -f '{{.State.Running}}' "$edge_name" 2>/dev/null || true)" != "true" ]; then
  echo "边缘容器未运行" >&2
  exit 2
fi

echo "security: traffic database role has an exact read and insert boundary"
python3 - "$compose_file" "$test_compose_file" <<'PY'
import subprocess, sys, time

compose_file, test_file = sys.argv[1:3]
command = [
    "docker", "compose", "-f", compose_file, "-f", test_file,
    "exec", "-T", "postgres", "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1",
    "-U", "yufeng", "-d", "yufeng",
]

def sql(statement, expected_success=True):
    result = subprocess.run(command, input=statement, text=True, capture_output=True)
    output = result.stdout + result.stderr
    if expected_success and result.returncode != 0:
        raise SystemExit("traffic role allowed operation failed: " + output)
    if not expected_success:
        if result.returncode == 0:
            raise SystemExit("traffic role unexpectedly accepted denied operation")
        if "42501" not in output:
            raise SystemExit("traffic role denial was not insufficient_privilege: " + output)
    return result.stdout.strip()

role_ok = sql("""\
SELECT rolcanlogin AND NOT rolinherit AND NOT rolsuper AND NOT rolcreaterole
       AND NOT rolcreatedb AND NOT rolreplication AND NOT rolbypassrls
FROM pg_roles WHERE rolname='yufeng_traffic';
""")
if role_ok.splitlines()[-1:] != ["t"]:
    raise SystemExit("traffic role attributes are not restricted: " + role_ok)

for table in (
    "traffic.traffic_windows",
    "traffic.traffic_window_receipts",
    "traffic.review_candidates",
    "traffic.review_case_outbox",
):
    allowed = sql(f"""\
SET ROLE yufeng_traffic;
SELECT has_table_privilege(current_user, '{table}', 'SELECT')
   AND has_table_privilege(current_user, '{table}', 'INSERT')
   AND NOT has_table_privilege(current_user, '{table}', 'UPDATE')
   AND NOT has_table_privilege(current_user, '{table}', 'DELETE')
   AND NOT has_table_privilege(current_user, '{table}', 'TRUNCATE')
   AND NOT has_table_privilege(current_user, '{table}', 'REFERENCES')
   AND NOT has_table_privilege(current_user, '{table}', 'TRIGGER');
""")
    if allowed.splitlines()[-1:] != ["t"]:
        raise SystemExit("traffic table privilege mismatch for " + table + ": " + allowed)

receipt_id = "security-traffic-role-" + str(int(time.time()))
sql(f"""\
BEGIN;
SET ROLE yufeng_traffic;
INSERT INTO traffic.traffic_window_receipts(window_id, window_start, payload_digest)
VALUES ('{receipt_id}', now(), 'security-traffic-role');
SELECT payload_digest FROM traffic.traffic_window_receipts WHERE window_id='{receipt_id}';
ROLLBACK;
""")

denied = (
    "SELECT 1 FROM users LIMIT 1;",
    "INSERT INTO releases(release_id, state, artifact, ttl_seconds) VALUES ('traffic-denied', 'shadow', '{}', 60);",
    "UPDATE releases SET state=state WHERE false;",
    "INSERT INTO grants(grant_id, subject_kind, subject_id) VALUES ('traffic-denied', 'agent', 'traffic-denied');",
    "UPDATE grants SET subject_id=subject_id WHERE false;",
    "INSERT INTO audit_entries(actor_type, actor_id, action, object_type, previous_hash, entry_hash) VALUES ('agent', 'traffic-denied', 'denied', 'traffic', '', '');",
    "UPDATE audit_entries SET actor_id=actor_id WHERE false;",
)
for statement in denied:
    sql("\\set VERBOSITY verbose\nSET ROLE yufeng_traffic;\n" + statement + "\n", expected_success=False)
print("security traffic database role boundary ok")
PY

echo "security: shipped production brain rejects missing certificates"
mkdir -p "$root/.tmp"
security_probe_dir=$(mktemp -d "$root/.tmp/security-missing-tls.XXXXXX")
security_probe_secret="$security_probe_dir/secret"
cleanup_security_probe() {
  rm -f "$security_probe_secret"
  rmdir "$security_probe_dir" 2>/dev/null || true
}
trap cleanup_security_probe EXIT HUP INT TERM
umask 077
printf 'security-probe-%s\n' "$$" > "$security_probe_secret"
set +e
missing_tls=$(docker run --rm \
  --entrypoint /usr/local/bin/yufeng-brain \
  --mount "type=bind,src=$security_probe_secret,dst=/run/yufeng-security-probe-secret,readonly" \
  yufeng:local \
  -dsn= \
  -bootstrap-admin-pass-file /run/yufeng-security-probe-secret \
  -agent-bootstrap-token-file /run/yufeng-security-probe-secret \
  -unit-bootstrap-token-file /run/yufeng-security-probe-secret \
  -modelside-token-file /run/yufeng-security-probe-secret 2>&1)
missing_tls_rc=$?
set -e
cleanup_security_probe
trap - EXIT HUP INT TERM
if [ "$missing_tls_rc" -eq 0 ] || ! printf '%s' "$missing_tls" | grep -q 'tls:'; then
  echo "生产 brain 缺证书未失败关闭"
  exit 1
fi

echo "security: container process and mount boundaries are closed"
python3 - "$edge_name" "$modelside_name" <<'PY'
import json, subprocess, sys
edge_name, modelside_name = sys.argv[1:3]
names = ["yufeng-brain-1", "yufeng-jarvis-1", "yufeng-agentd-1", "yufeng-signer-1", "yufeng-keys-1", edge_name, modelside_name]
items = json.loads(subprocess.check_output(["docker", "inspect", *names]))
by_name = {item["Name"].lstrip("/"): item for item in items}

def mounts(name):
    return {m.get("Destination", "") for m in by_name[name].get("Mounts") or []}

def command_text(name):
    cfg = by_name[name].get("Config") or {}
    return "\n".join((cfg.get("Env") or []) + (cfg.get("Cmd") or []) + (cfg.get("Entrypoint") or []))

socket_holders = [name for name in by_name if "/var/run/docker.sock" in mounts(name)]
assert socket_holders == [], socket_holders
key_holders = sorted(name for name in by_name if "/keys" in mounts(name))
assert key_holders == ["yufeng-keys-1", "yufeng-signer-1"], key_holders
assert "/sign" in mounts("yufeng-brain-1") and "/keys" not in mounts("yufeng-brain-1")
for name in ("yufeng-jarvis-1", "yufeng-agentd-1", edge_name, modelside_name):
    assert not ({"/keys", "/sign", "/var/run/docker.sock"} & mounts(name)), (name, mounts(name))
for name in by_name:
    text = command_text(name)
    assert "YUFENG_MODEL_API_KEY" not in text
    assert "-signing-key" not in text
assert "/pubkey" in mounts(edge_name) and "/source" in mounts(edge_name)
assert "/source" not in mounts(modelside_name)
assert "/run/secrets/unit_bootstrap_token" in mounts(edge_name)
assert "/run/secrets/unit_bootstrap_token" not in mounts(modelside_name)
assert "/run/secrets/modelside_result_token" in mounts(modelside_name)
assert "/run/secrets/modelside_result_token" not in mounts(edge_name)
assert "-modelside" in command_text(edge_name)
assert "--brain-token-file" in command_text(modelside_name) and "--weights" in command_text(modelside_name)
print("security process boundary ok")
PY

unit_bootstrap=$(docker exec "$edge_name" cat /run/secrets/unit_bootstrap_token)
echo "security: viewer and unit registration fail closed on the live control plane"
YUFENG_SECURITY_BASE="$base" YUFENG_SECURITY_ADMIN_USER="$admin_user" YUFENG_SECURITY_ADMIN_PASS="$admin_pass" YUFENG_SECURITY_UNIT_BOOTSTRAP="$unit_bootstrap" python3 <<'PY'
import json, os, ssl, time, urllib.error, urllib.request, uuid
base = os.environ["YUFENG_SECURITY_BASE"].rstrip("/")
ctx = ssl._create_unverified_context()

def call(path, body, token="", idem=False):
    headers = {"Content-Type":"application/json", "Connect-Protocol-Version":"1"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if idem:
        headers["Idempotency-Key"] = str(uuid.uuid4())
    req = urllib.request.Request(base + path, data=json.dumps(body).encode(), method="POST", headers=headers)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=20) as resp:
            return resp.status, json.loads(resp.read().decode())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read().decode())

status, body = call("/yufeng.auth.v1.AuthService/Login", {
    "username":os.environ["YUFENG_SECURITY_ADMIN_USER"],
    "password":os.environ["YUFENG_SECURITY_ADMIN_PASS"],
})
assert status == 200 and body.get("token"), (status, body)
admin = body["token"]
viewer_name = "security-viewer-" + str(int(time.time()))
status, body = call("/yufeng.user.v1.UserService/CreateUser", {
    "username":viewer_name, "password":"ViewerSecurity123", "displayName":viewer_name,
    "role":"USER_ROLE_VIEWER",
}, admin, True)
assert status == 200, (status, body)
status, body = call("/yufeng.auth.v1.AuthService/Login", {"username":viewer_name,"password":"ViewerSecurity123"})
assert status == 200 and body.get("token"), (status, body)
viewer = body["token"]
status, body = call("/yufeng.govern.v1.GovernService/PromoteEnforce", {"releaseId":"security-denied"}, viewer, True)
assert status == 403 and body.get("code") == "permission_denied", (status, body)

registration = {
    "unitId":"local-1", "kind":"UNIT_KIND_EDGE", "version":"security-hijack",
    "contractVersion":"v1", "pubkeyHint":"security-hijack",
}
status, body = call("/yufeng.registry.v1.RegistryService/Register", registration)
assert status == 401 and body.get("code") == "unauthenticated", (status, body)
status, body = call("/yufeng.registry.v1.RegistryService/Register", registration, os.environ["YUFENG_SECURITY_UNIT_BOOTSTRAP"])
assert status == 403 and body.get("code") == "permission_denied", (status, body)
print("security live identity negative cases ok")
PY
unset unit_bootstrap

suffix=$(date +%s)
query_secret="query-secret-$suffix"
auth_secret="authorization-secret-$suffix"
cookie_secret="cookie-secret-$suffix"
body_secret="business-private-key-$suffix"
source_octet=$((suffix % 200 + 20))
raw_source="198.51.100.$source_octet"

echo "security: request secrets stay out of central ledgers, logs, and model projections"
set +e
traffic_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-For: $raw_source" \
  -H "Authorization: Bearer $auth_secret" \
  -H "Cookie: session=$cookie_secret" \
  -H 'Content-Type: text/plain' \
  --data-binary "$body_secret" \
  "http://127.0.0.1:18080/api/items?id=1%20UNION%20SELECT%20security&probe=$query_secret")
traffic_rc=$?
set -e
if [ "$traffic_rc" -ne 0 ] || { [ "$traffic_code" != "200" ] && [ "$traffic_code" != "403" ]; }; then
  echo "安全探测请求失败：HTTP $traffic_code"
  exit 1
fi
sleep 5
YUFENG_SECURITY_QUERY="$query_secret" YUFENG_SECURITY_AUTH="$auth_secret" \
YUFENG_SECURITY_COOKIE="$cookie_secret" YUFENG_SECURITY_BODY="$body_secret" \
YUFENG_SECURITY_SOURCE="$raw_source" python3 - "$compose_file" "$data_compose_file" "$test_compose_file" "$edge_name" "$modelside_name" <<'PY'
import os, subprocess, sys
compose_file, data_file, test_file, edge_name, modelside_name = sys.argv[1:6]
markers = {
    "query": os.environ["YUFENG_SECURITY_QUERY"],
    "authorization": os.environ["YUFENG_SECURITY_AUTH"],
    "cookie": os.environ["YUFENG_SECURITY_COOKIE"],
    "request_body": os.environ["YUFENG_SECURITY_BODY"],
    "raw_source": os.environ["YUFENG_SECURITY_SOURCE"],
}
dump = subprocess.check_output([
    "docker", "compose", "-f", compose_file, "-f", test_file,
    "exec", "-T", "postgres", "pg_dump", "-U", "yufeng", "-d", "yufeng", "--data-only",
], stderr=subprocess.STDOUT)
logs = b""
for name in ("yufeng-brain-1", "yufeng-jarvis-1", "yufeng-agentd-1", "yufeng-signer-1", edge_name, modelside_name):
    logs += subprocess.check_output(["docker", "logs", name], stderr=subprocess.STDOUT)
for label, marker in markers.items():
    raw = marker.encode()
    if raw in dump:
        raise SystemExit("request secret leaked into central ledger: " + label)
    if raw in logs:
        raise SystemExit("request secret leaked into platform log: " + label)
print("security live data boundary ok")
PY

echo "security live ok"
