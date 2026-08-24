#!/usr/bin/env bash
# 对发布稳定分支的预期合并 Git 树运行一次完整本机静态预检。
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [[ -f "$root/.env" && -z "${YUFENG_SKIP_DOTENV:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$root/.env"
  set +a
fi

output_dir=""
while (($# > 0)); do
  case "$1" in
    --output-dir)
      output_dir=${2:-}
      shift 2
      ;;
    *)
      echo "usage: $0 [--output-dir <directory>]" >&2
      exit 64
      ;;
  esac
done

for command in git docker python3 go node npm buf tar system_profiler; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "required command is unavailable: $command" >&2
    exit 2
  }
done
if ! docker info >/dev/null 2>&1; then
  echo "Docker 未运行" >&2
  exit 2
fi

version=$(tr -d '[:space:]' < VERSION)
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "VERSION is not a semantic version" >&2
  exit 1
fi
branch=$(git branch --show-current)
if [[ "$branch" != "release/${version}" ]]; then
  echo "release preflight must run from release/${version}" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "release preflight requires a clean worktree" >&2
  exit 1
fi

git fetch --quiet origin develop
base_commit=$(git rev-parse origin/develop)
source_commit=$(git rev-parse HEAD)
if [[ "$(git merge-base "$base_commit" "$source_commit")" != "$base_commit" ]]; then
  echo "origin/develop moved after the release branch was frozen" >&2
  exit 1
fi
preflight_tree=$(git merge-tree --write-tree "$base_commit" "$source_commit")
if [[ ! "$preflight_tree" =~ ^[0-9a-f]{40,64}$ ]]; then
  echo "expected merge tree is invalid or conflicted" >&2
  exit 1
fi
synthetic_commit=$(printf 'release preflight for %s\n' "$version" | git commit-tree "$preflight_tree" -p "$base_commit" -p "$source_commit")

if [[ -z "$output_dir" ]]; then
  output_dir="$root/.tmp/release-preflight/${version}-${preflight_tree:0:12}"
elif [[ "$output_dir" != /* ]]; then
  output_dir="$root/$output_dir"
fi
if [[ -e "$output_dir" ]]; then
  echo "preflight output already exists: $output_dir" >&2
  exit 1
fi

weights_dir=${YUFENG_MODELSIDE_WEIGHTS_DIR:-}
if [[ -z "$weights_dir" ]]; then
  echo "YUFENG_MODELSIDE_WEIGHTS_DIR is required" >&2
  exit 2
fi
if [[ "$weights_dir" != /* ]]; then
  weights_dir="$root/$weights_dir"
fi
if [[ -z "${YUFENG_EDGE_UNIT:-}" || -z "${YUFENG_MODELSIDE_ID:-}" ]]; then
  echo "YUFENG_EDGE_UNIT and YUFENG_MODELSIDE_ID are required" >&2
  exit 2
fi

export YUFENG_SKIP_DOTENV=1
export YUFENG_MODELSIDE_WEIGHTS_DIR="$weights_dir"
export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-yufeng}
export YUFENG_DB_PASSWORD_FILE=${YUFENG_DB_PASSWORD_FILE:-"$root/deploy/secrets/db_password"}
export YUFENG_TRAFFIC_DB_PASSWORD_FILE=${YUFENG_TRAFFIC_DB_PASSWORD_FILE:-"$root/deploy/secrets/traffic_db_password"}
export YUFENG_ADMIN_PASSWORD_FILE=${YUFENG_ADMIN_PASSWORD_FILE:-"$root/deploy/secrets/admin_password"}
export YUFENG_AGENT_BOOTSTRAP_TOKEN_FILE=${YUFENG_AGENT_BOOTSTRAP_TOKEN_FILE:-"$root/deploy/secrets/agent_bootstrap_token"}
export YUFENG_UNIT_BOOTSTRAP_TOKEN_FILE=${YUFENG_UNIT_BOOTSTRAP_TOKEN_FILE:-"$root/deploy/secrets/unit_bootstrap_token"}
export YUFENG_MODELSIDE_RESULT_TOKEN_FILE=${YUFENG_MODELSIDE_RESULT_TOKEN_FILE:-"$root/deploy/secrets/modelside_result_token"}
export YUFENG_CENTRAL_WORKER_BOOTSTRAP_TOKEN_FILE=${YUFENG_CENTRAL_WORKER_BOOTSTRAP_TOKEN_FILE:-"$root/deploy/secrets/central_worker_bootstrap_token"}

mkdir -p "$root/.tmp"
stage=$(mktemp -d "$root/.tmp/release-preflight-stage.XXXXXX")
candidate="$stage/candidate"
promotion_probe="$stage/promotion-probe"
evidence_root="$stage/yufeng-evidence"
mkdir -p "$evidence_root/logs" "$evidence_root/environment"
worktree_added=0
promotion_probe_added=0
test_database_started=0
completed=0
test_database="yufeng-release-preflight-${source_commit:0:12}-$$"
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [[ "$test_database_started" -eq 1 ]]; then
    docker stop "$test_database" >/dev/null 2>&1 || true
  fi
  if [[ "$worktree_added" -eq 1 ]]; then
    git worktree remove --force "$candidate" >/dev/null 2>&1 || true
  fi
  if [[ "$promotion_probe_added" -eq 1 ]]; then
    git worktree remove --force "$promotion_probe" >/dev/null 2>&1 || true
  fi
  if [[ "$completed" -eq 1 ]]; then
    rm -rf "$stage"
  else
    echo "未通过的静态预检保留在：$stage" >&2
  fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

git worktree add --detach "$candidate" "$synthetic_commit" >/dev/null
worktree_added=1
if [[ "$(git -C "$candidate" rev-parse HEAD^{tree})" != "$preflight_tree" ]] || \
    [[ -n "$(git -C "$candidate" status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "temporary preflight worktree does not match the expected merge tree" >&2
  exit 1
fi

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

results="$stage/command-results.jsonl"
: > "$results"
run_logged() {
  local name=$1
  local display=$2
  shift 2
  local log="$evidence_root/logs/${name}.log"
  local command_started command_finished exit_code result log_sha
  command_started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  printf 'preflight command start: %s\n' "$name"
  set +e
  (cd "$candidate" && "$@") 2>&1 | tee "$log"
  exit_code=${PIPESTATUS[0]}
  set -e
  command_finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  result=failed
  if [[ "$exit_code" -eq 0 ]]; then
    result=passed
  fi
  log_sha=$(sha256_file "$log")
  python3 - "$results" "$name" "$display" "$command_started" "$command_finished" "$exit_code" "$result" "logs/${name}.log" "$log_sha" <<'PY'
import json
import sys

output, name, command, started, finished, exit_code, result, log, log_sha = sys.argv[1:]
record = {
    "name": name,
    "command": command,
    "started-at": started,
    "finished-at": finished,
    "exit-code": int(exit_code),
    "result": result,
    "log": log,
    "log-sha256": log_sha,
}
with open(output, "a", encoding="utf-8") as handle:
    handle.write(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n")
PY
  printf 'preflight command end: %s result=%s\n' "$name" "$result"
  return "$exit_code"
}

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
environment_fingerprint=$(python3 "$candidate/scripts/release-environment.py" capture \
  --root "$candidate" --weights-dir "$weights_dir" \
  --output "$evidence_root/environment/summary.json")

# 证据提升运行于最终 develop 工作树；随机检出目录不得进入环境身份。
git worktree add --detach "$promotion_probe" "$synthetic_commit" >/dev/null
promotion_probe_added=1
promotion_probe_fingerprint=$(python3 "$candidate/scripts/release-environment.py" capture \
  --root "$promotion_probe" --weights-dir "$weights_dir" \
  --output "$stage/promotion-probe-environment.json")
if [[ "$promotion_probe_fingerprint" != "$environment_fingerprint" ]]; then
  echo "release environment fingerprint depends on the checkout path" >&2
  exit 1
fi
git worktree remove "$promotion_probe" >/dev/null
promotion_probe_added=0

docker run -d --rm --name "$test_database" \
  -e POSTGRES_DB=yufeng_test \
  -e POSTGRES_HOST_AUTH_METHOD=trust \
  -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
test_database_started=1
for _ in $(seq 1 60); do
  if docker exec "$test_database" pg_isready -U postgres -d yufeng_test >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! docker exec "$test_database" pg_isready -U postgres -d yufeng_test >/dev/null 2>&1; then
  echo "release preflight PostgreSQL did not become ready" >&2
  exit 1
fi
docker exec "$test_database" psql -U postgres -d yufeng_test -v ON_ERROR_STOP=1 \
  -c "CREATE ROLE yufeng_traffic LOGIN PASSWORD 'integration-only'" >/dev/null
test_port=$(docker port "$test_database" 5432/tcp | tail -n 1)
test_port=${test_port##*:}
export YUFENG_TEST_DSN="postgres://postgres@127.0.0.1:${test_port}/yufeng_test?sslmode=disable"
export YUFENG_TRAFFIC_TEST_DSN="postgres://yufeng_traffic:integration-only@127.0.0.1:${test_port}/yufeng_test?sslmode=disable"

run_logged release-static \
  './scripts/delivery-evidence.sh static' \
  ./scripts/delivery-evidence.sh static
test -f "$candidate/lib/edgecore/hot_path_prototype_benchmark_test.go"
run_logged hot-path-benchmarks \
  "go test ./lib/edgecore -run '^$' -bench '^Benchmark' -benchmem -benchtime=250ms -count=5" \
  go test ./lib/edgecore -run '^$' -bench '^Benchmark' -benchmem -benchtime=250ms -count=5

docker stop "$test_database" >/dev/null
test_database_started=0
unset YUFENG_TEST_DSN YUFENG_TRAFFIC_TEST_DSN

verification_environment="$stage/environment-verification.json"
verification_fingerprint=$(python3 "$candidate/scripts/release-environment.py" capture \
  --root "$candidate" --weights-dir "$weights_dir" --output "$verification_environment")
if [[ "$verification_fingerprint" != "$environment_fingerprint" ]]; then
  echo "release environment changed during static preflight" >&2
  exit 1
fi
if [[ -n "$(git -C "$candidate" status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "candidate worktree changed during static preflight" >&2
  exit 1
fi

finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
python3 - "$evidence_root" "$results" "$version" "$base_commit" "$source_commit" "$preflight_tree" \
  "$synthetic_commit" "$environment_fingerprint" "$started_at" "$finished_at" <<'PY'
import json
import sys
from pathlib import Path

(evidence_raw, results_raw, version, base_commit, source_commit, tree,
 synthetic_commit, environment_fingerprint, started_at, finished_at) = sys.argv[1:]
evidence = Path(evidence_raw)
commands = [json.loads(line) for line in Path(results_raw).read_text(encoding="utf-8").splitlines()]
report = {
    "schema": "yufeng.release-preflight-report/v1",
    "release-version": version,
    "base-commit": base_commit,
    "source-commit": source_commit,
    "preflight-tree": tree,
    "preflight-result": "passed",
    "environment-fingerprint": environment_fingerprint,
    "timestamps": {"started-at": started_at, "finished-at": finished_at},
    "git": {
        "branch": f"release/{version}",
        "base-commit": base_commit,
        "source-commit": source_commit,
        "synthetic-commit": synthetic_commit,
        "tree": tree,
        "worktree": "clean",
    },
    "environment-summary": "environment/summary.json",
    "commands": commands,
    "secret-scan": {
        "result": "passed",
        "scanner": "scripts/release-evidence.py scan",
        "record": "environment/secret-scan.txt",
    },
}
(evidence / "report.json").write_text(
    json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
    encoding="utf-8",
)
PY

secret_scan_result=$(python3 "$candidate/scripts/release-evidence.py" scan --root "$evidence_root")
printf '%s\n' "$secret_scan_result" > "$evidence_root/environment/secret-scan.txt"
python3 "$candidate/scripts/release-evidence.py" scan --root "$evidence_root" >/dev/null

report_sha256=$(sha256_file "$evidence_root/report.json")
mkdir -p "$output_dir"
archive_name="yufeng-${version}-preflight-evidence.tar.gz"
checksum_name="yufeng-${version}-preflight-evidence.tar.gz.sha256"
manifest_name="yufeng-${version}-preflight-evidence.json"
archive="$output_dir/$archive_name"
checksum="$output_dir/$checksum_name"
manifest="$output_dir/$manifest_name"
COPYFILE_DISABLE=1 tar -czf "$archive" -C "$stage" yufeng-evidence
preflight_sha256=$(sha256_file "$archive")
printf '%s  %s\n' "$preflight_sha256" "$archive_name" > "$checksum"
python3 - "$manifest" "$version" "$base_commit" "$source_commit" "$preflight_tree" \
  "$preflight_sha256" "$archive_name" "$checksum_name" "$report_sha256" \
  "$environment_fingerprint" "$finished_at" <<'PY'
import json
import sys
from datetime import datetime, timedelta, timezone

(output, version, base_commit, source_commit, tree, archive_sha, archive_name,
 checksum_name, report_sha, environment_fingerprint, generated_at) = sys.argv[1:]
generated = datetime.fromisoformat(generated_at.replace("Z", "+00:00")).astimezone(timezone.utc)
manifest = {
    "schema": "yufeng.release-preflight/v1",
    "release-version": version,
    "base-commit": base_commit,
    "source-commit": source_commit,
    "preflight-tree": tree,
    "preflight-sha256": archive_sha,
    "preflight-result": "passed",
    "archive-asset": archive_name,
    "checksum-asset": checksum_name,
    "report-path": "yufeng-evidence/report.json",
    "report-sha256": report_sha,
    "environment-fingerprint": environment_fingerprint,
    "generated-at": generated.strftime("%Y-%m-%dT%H:%M:%SZ"),
    "expires-at": (generated + timedelta(hours=72)).strftime("%Y-%m-%dT%H:%M:%SZ"),
}
with open(output, "w", encoding="utf-8") as handle:
    json.dump(manifest, handle, ensure_ascii=False, indent=2, sort_keys=True)
    handle.write("\n")
PY

python3 "$candidate/scripts/release-evidence.py" verify-preflight \
  --manifest "$manifest" --archive "$archive" --checksum "$checksum" \
  --expected-version "$version" --expected-base-commit "$base_commit" \
  --expected-source-commit "$source_commit" --expected-tree "$preflight_tree" \
  --expected-environment-fingerprint "$environment_fingerprint" >/dev/null
python3 "$candidate/scripts/release-evidence.py" scan --root "$output_dir" >/dev/null
if [[ "$source_commit" != "$(git rev-parse HEAD)" ]] || \
    [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "release branch changed while collecting static preflight" >&2
  exit 1
fi

completed=1
printf 'release static preflight passed\nmanifest: %s\nGit tree: %s\n' "$manifest" "$preflight_tree"
