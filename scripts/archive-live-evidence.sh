#!/usr/bin/env bash
# 对最终 develop 合并提交只补一次活栈证据，并与已通过的静态预检装配发布归档。
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [[ -f "$root/.env" && -z "${YUFENG_SKIP_DOTENV:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$root/.env"
  set +a
fi

ci_url=""
preflight_manifest=""
preflight_archive=""
preflight_checksum=""
output_dir=""
while (($# > 0)); do
  case "$1" in
    --ci-url)
      ci_url=${2:-}
      shift 2
      ;;
    --preflight-manifest)
      preflight_manifest=${2:-}
      shift 2
      ;;
    --preflight-archive)
      preflight_archive=${2:-}
      shift 2
      ;;
    --preflight-checksum)
      preflight_checksum=${2:-}
      shift 2
      ;;
    --output-dir)
      output_dir=${2:-}
      shift 2
      ;;
    *)
      echo "usage: $0 --ci-url <develop CI URL> --preflight-manifest <file> --preflight-archive <file> --preflight-checksum <file> [--output-dir <directory>]" >&2
      exit 64
      ;;
  esac
done
if [[ -z "$ci_url" || -z "$preflight_manifest" || -z "$preflight_archive" || -z "$preflight_checksum" ]]; then
  echo "continuous integration URL and all three preflight files are required" >&2
  exit 64
fi
for path_variable in preflight_manifest preflight_archive preflight_checksum; do
  path=${!path_variable}
  if [[ "$path" != /* ]]; then
    printf -v "$path_variable" '%s/%s' "$root" "$path"
  fi
  if [[ ! -f "${!path_variable}" ]]; then
    echo "preflight file is unavailable: ${!path_variable}" >&2
    exit 2
  fi
done
for command in git gh docker python3 go node npm buf tar system_profiler; do
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
if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "release evidence requires a clean worktree" >&2
  exit 1
fi
git fetch --quiet origin develop
commit=$(git rev-parse HEAD)
develop_commit=$(git rev-parse origin/develop)
if [[ "$commit" != "$develop_commit" ]]; then
  echo "release evidence commit must equal origin/develop" >&2
  exit 1
fi
tree=$(git rev-parse HEAD^{tree})
read -r parsed_commit base_parent source_parent extra_parent <<< "$(git rev-list --parents -n 1 "$commit")"
if [[ "$parsed_commit" != "$commit" || -z "$base_parent" || -z "$source_parent" || -n "${extra_parent:-}" ]]; then
  echo "final develop evidence commit must have exactly two parents" >&2
  exit 1
fi
if [[ "$(git rev-parse "${source_parent}^{tree}")" != "$tree" ]]; then
  echo "final develop tree differs from the merged release branch" >&2
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

short_commit=${commit:0:12}
if [[ -z "$output_dir" ]]; then
  output_dir="$root/.tmp/release-evidence/${version}-${short_commit}"
elif [[ "$output_dir" != /* ]]; then
  output_dir="$root/$output_dir"
fi
if [[ -e "$output_dir" ]]; then
  echo "evidence output already exists: $output_dir" >&2
  exit 1
fi

mkdir -p "$root/.tmp"
stage=$(mktemp -d "$root/.tmp/release-evidence-stage.XXXXXX")
completed=0
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [[ "$completed" -eq 1 ]]; then
    rm -rf "$stage"
  else
    echo "未通过的本机活栈证据保留在：$stage" >&2
  fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

current_environment="$stage/current-environment.json"
environment_fingerprint=$(python3 scripts/release-environment.py capture \
  --root "$root" --weights-dir "$weights_dir" --output "$current_environment")
python3 scripts/release-evidence.py verify-preflight \
  --manifest "$preflight_manifest" --archive "$preflight_archive" --checksum "$preflight_checksum" \
  --expected-version "$version" --expected-base-commit "$base_parent" \
  --expected-source-commit "$source_parent" --expected-tree "$tree" \
  --expected-environment-fingerprint "$environment_fingerprint" >/dev/null

repository=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
run_id=${ci_url%/}
run_id=${run_id##*/}
if [[ ! "$run_id" =~ ^[0-9]+$ ]]; then
  echo "continuous integration URL does not end in a run id" >&2
  exit 1
fi
gh api "repos/${repository}/actions/runs/${run_id}" > "$stage/ci-run.json"
python3 - "$stage/ci-run.json" "$ci_url" "$commit" <<'PY'
import json
import sys

path, expected_url, expected_commit = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    run = json.load(handle)
if run.get("html_url") != expected_url:
    raise SystemExit("continuous integration URL does not match the GitHub run")
if run.get("head_sha") != expected_commit or run.get("head_branch") != "develop" or run.get("event") != "push":
    raise SystemExit("continuous integration run is not the exact develop push")
if run.get("conclusion") != "success" or run.get("status") != "completed":
    raise SystemExit("continuous integration run has not completed successfully")
if not str(run.get("path", "")).startswith(".github/workflows/ci.yml"):
    raise SystemExit("continuous integration run did not use ci.yml")
PY

source_container=${YUFENG_POSTGRES_CONTAINER:-yufeng-postgres-1}
if [[ "$(docker inspect -f '{{.State.Running}}' "$source_container" 2>/dev/null || true)" != "true" ]]; then
  echo "源 PostgreSQL 未运行" >&2
  exit 2
fi
backup_run=$(date -u +%Y%m%dT%H%M%SZ)
backup_dir=${YUFENG_EVIDENCE_BACKUP_DIR:-"$root/.tmp/pilot-backups/${version}-${commit}-${backup_run}"}
if [[ -e "$backup_dir" ]]; then
  echo "pilot backup already exists: $backup_dir" >&2
  exit 1
fi
mkdir -p "$backup_dir"
backup_file="$backup_dir/yufeng.dump"
backup_started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker exec "$source_container" pg_dump -U yufeng -d yufeng -Fc > "$backup_file"
if [[ ! -s "$backup_file" ]]; then
  echo "pilot database backup is empty" >&2
  exit 1
fi

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

backup_sha256=$(sha256_file "$backup_file")
backup_bytes=$(wc -c < "$backup_file" | tr -d ' ')
backup_finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
printf 'source backup ok: sha256=%s bytes=%s\n' "$backup_sha256" "$backup_bytes"

tar -xzf "$preflight_archive" -C "$stage"
evidence_root="$stage/yufeng-evidence"
mkdir -p "$evidence_root/provenance" "$evidence_root/results"
mv "$evidence_root/report.json" "$evidence_root/provenance/preflight-report.json"
cp "$preflight_manifest" "$evidence_root/provenance/preflight-manifest.json"
cp "$current_environment" "$evidence_root/environment/promotion-summary.json"

results="$stage/live-command-results.jsonl"
: > "$results"
run_logged() {
  local name=$1
  local display=$2
  shift 2
  local log="$evidence_root/logs/${name}.log"
  local command_started command_finished exit_code result log_sha
  command_started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  printf 'evidence command start: %s\n' "$name"
  set +e
  "$@" 2>&1 | tee "$log"
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
  printf 'evidence command end: %s result=%s\n' "$name" "$result"
  return "$exit_code"
}

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
run_logged live-evidence \
  './scripts/delivery-evidence.sh live' \
  env \
    YUFENG_PERFORMANCE_REPORT="$evidence_root/results/performance.json" \
    YUFENG_BACKUP_RESTORE_REPORT="$evidence_root/results/backup-restore.json" \
    YUFENG_TRAFFIC_REVIEW_REPORT="$evidence_root/results/traffic-review.json" \
    ./scripts/delivery-evidence.sh live
for required_result in performance.json backup-restore.json traffic-review.json; do
  if [[ ! -s "$evidence_root/results/$required_result" ]]; then
    echo "live evidence did not write $required_result" >&2
    exit 1
  fi
done

verification_environment="$stage/environment-verification.json"
verification_fingerprint=$(python3 scripts/release-environment.py capture \
  --root "$root" --weights-dir "$weights_dir" --output "$verification_environment")
if [[ "$verification_fingerprint" != "$environment_fingerprint" ]]; then
  echo "release environment changed during live evidence" >&2
  exit 1
fi

preflight_manifest_sha256=$(sha256_file "$preflight_manifest")
preflight_archive_sha256=$(sha256_file "$preflight_archive")
finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
python3 - "$stage" "$evidence_root" "$results" "$version" "$commit" "$tree" "$base_parent" "$source_parent" \
  "$ci_url" "$started_at" "$finished_at" "$backup_started_at" "$backup_finished_at" \
  "$backup_sha256" "$backup_bytes" "$environment_fingerprint" \
  "$preflight_manifest_sha256" "$preflight_archive_sha256" <<'PY'
import json
import sys
from pathlib import Path

(stage_raw, evidence_raw, results_raw, version, commit, tree, base_parent, source_parent,
 ci_url, started, finished, backup_started, backup_finished, backup_sha, backup_bytes,
 environment_fingerprint, preflight_manifest_sha, preflight_archive_sha) = sys.argv[1:]
stage = Path(stage_raw)
evidence = Path(evidence_raw)
ci = json.loads((stage / "ci-run.json").read_text(encoding="utf-8"))
preflight = json.loads((evidence / "provenance" / "preflight-manifest.json").read_text(encoding="utf-8"))
commands = [json.loads(line) for line in Path(results_raw).read_text(encoding="utf-8").splitlines()]
report = {
    "schema": "yufeng.release-evidence-report/v2",
    "release-version": version,
    "evidence-commit": commit,
    "evidence-tree": tree,
    "evidence-result": "passed",
    "ci-url": ci_url,
    "merge-parents": [base_parent, source_parent],
    "timestamps": {"started-at": started, "finished-at": finished},
    "git": {"branch": "develop", "commit": commit, "tree": tree, "worktree": "clean"},
    "continuous-integration": {
        "url": ci.get("html_url", ""),
        "head-sha": ci.get("head_sha", ""),
        "head-branch": ci.get("head_branch", ""),
        "event": ci.get("event", ""),
        "status": ci.get("status", ""),
        "conclusion": ci.get("conclusion", ""),
        "workflow-path": ci.get("path", ""),
    },
    "source-backup": {
        "included-in-archive": False,
        "sha256": backup_sha,
        "bytes": int(backup_bytes),
        "started-at": backup_started,
        "finished-at": backup_finished,
    },
    "static-preflight": {
        "manifest": "provenance/preflight-manifest.json",
        "manifest-sha256": preflight_manifest_sha,
        "report": "provenance/preflight-report.json",
        "report-sha256": preflight.get("report-sha256", ""),
        "archive-sha256": preflight_archive_sha,
        "environment-fingerprint": environment_fingerprint,
    },
    "promotion-environment-summary": "environment/promotion-summary.json",
    "live-results": {
        "performance": "results/performance.json",
        "backup-restore": "results/backup-restore.json",
        "traffic-review": "results/traffic-review.json",
    },
    "commands": commands,
    "secret-scan": {
        "result": "passed",
        "scanner": "scripts/release-evidence.py scan",
        "record": "environment/final-secret-scan.txt",
    },
}
(evidence / "report.json").write_text(
    json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
    encoding="utf-8",
)
PY

secret_scan_result=$(python3 scripts/release-evidence.py scan --root "$evidence_root")
printf '%s\n' "$secret_scan_result" > "$evidence_root/environment/final-secret-scan.txt"
python3 scripts/release-evidence.py scan --root "$evidence_root" >/dev/null

report_sha256=$(sha256_file "$evidence_root/report.json")
mkdir -p "$output_dir/assets"
archive_name="yufeng-${version}-live-evidence.tar.gz"
checksum_name="yufeng-${version}-live-evidence.tar.gz.sha256"
manifest_name="yufeng-${version}-live-evidence.json"
archive="$output_dir/assets/$archive_name"
checksum="$output_dir/assets/$checksum_name"
manifest="$output_dir/assets/$manifest_name"
COPYFILE_DISABLE=1 tar -czf "$archive" -C "$stage" yufeng-evidence
evidence_sha256=$(sha256_file "$archive")
printf '%s  %s\n' "$evidence_sha256" "$archive_name" > "$checksum"
python3 - "$manifest" "$preflight_manifest" "$version" "$commit" "$tree" "$base_parent" "$source_parent" \
  "$evidence_sha256" "$archive_name" "$checksum_name" "$report_sha256" "$ci_url" "$finished_at" \
  "$preflight_manifest_sha256" "$preflight_archive_sha256" <<'PY'
import json
import sys

(output, preflight_path, version, commit, tree, base_parent, source_parent, evidence_sha,
 archive_name, checksum_name, report_sha, ci_url, generated_at,
 preflight_manifest_sha, preflight_archive_sha) = sys.argv[1:]
with open(preflight_path, encoding="utf-8") as handle:
    source = json.load(handle)
manifest = {
    "schema": "yufeng.release-evidence/v2",
    "release-version": version,
    "evidence-commit": commit,
    "evidence-tree": tree,
    "evidence-sha256": evidence_sha,
    "evidence-result": "passed",
    "archive-asset": archive_name,
    "checksum-asset": checksum_name,
    "report-path": "yufeng-evidence/report.json",
    "report-sha256": report_sha,
    "ci-url": ci_url,
    "generated-at": generated_at,
    "merge-parents": [base_parent, source_parent],
    "preflight": {
        "base-commit": source.get("base-commit", ""),
        "source-commit": source.get("source-commit", ""),
        "tree": source.get("preflight-tree", ""),
        "archive-sha256": preflight_archive_sha,
        "manifest-sha256": preflight_manifest_sha,
        "report-sha256": source.get("report-sha256", ""),
        "environment-fingerprint": source.get("environment-fingerprint", ""),
        "generated-at": source.get("generated-at", ""),
        "expires-at": source.get("expires-at", ""),
    },
}
with open(output, "w", encoding="utf-8") as handle:
    json.dump(manifest, handle, ensure_ascii=False, indent=2, sort_keys=True)
    handle.write("\n")
PY

python3 - "$output_dir" "$version" "$commit" "$tree" "$evidence_sha256" "$ci_url" <<'PY'
import sys
from pathlib import Path

directory, version, commit, tree, evidence_sha, ci_url = sys.argv[1:]
root = Path(directory)
metadata = "\n".join([
    f"release-version={version}",
    f"evidence-commit={commit}",
    f"evidence-tree={tree}",
    f"evidence-sha256={evidence_sha}",
    "evidence-result=passed",
])
pr_body = (
    f"## {version} L1 单站点生产试点\n\n"
    "发布稳定分支的相同 Git 树已通过本机静态预检、远端合并确认与一次本机活栈、恢复、性能验收。\n\n"
    f"{metadata}\n\n持续集成：{ci_url}\n"
)
tag_message = f"{version} L1 单站点生产试点\n\n{metadata}\n"
(root / "release-pr-body.md").write_text(pr_body, encoding="utf-8")
(root / "release-tag-message.txt").write_text(tag_message, encoding="utf-8")
PY

python3 scripts/release-metadata.py verify --file "$output_dir/release-pr-body.md" \
  --expected-version "$version" --expected-commit "$commit" --expected-tree "$tree" \
  --expected-sha256 "$evidence_sha256" >/dev/null
python3 scripts/release-metadata.py verify --file "$output_dir/release-tag-message.txt" \
  --expected-version "$version" --expected-commit "$commit" --expected-tree "$tree" \
  --expected-sha256 "$evidence_sha256" >/dev/null
python3 scripts/release-evidence.py verify --manifest "$manifest" --archive "$archive" --checksum "$checksum" \
  --expected-version "$version" --expected-commit "$commit" --expected-tree "$tree" \
  --expected-sha256 "$evidence_sha256" --expected-base-commit "$base_parent" \
  --expected-source-commit "$source_parent" >/dev/null
python3 scripts/release-evidence.py scan --root "$output_dir" >/dev/null
if [[ "$commit" != "$(git rev-parse HEAD)" ]] || \
    [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "worktree changed while collecting live release evidence" >&2
  exit 1
fi

completed=1
printf 'release evidence passed\nassets: %s\nmetadata: %s\nlocal backup sha256: %s\n' \
  "$output_dir/assets" "$output_dir/release-pr-body.md" "$backup_sha256"
