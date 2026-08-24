#!/bin/sh
# 企业试点逻辑备份恢复演练；原数据库只读，不删除原容器或数据卷。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

compose_file=${COMPOSE_FILE:-deploy/compose.yaml}
source_container=${YUFENG_POSTGRES_CONTAINER:-yufeng-postgres-1}
restore_name="yufeng-restore-check-$$"
mode=${1:-live}
restore_dir=""
brain_stopped=0

compose() {
  docker compose -f "$compose_file" "$@"
}

cleanup() {
  if [ "$brain_stopped" -eq 1 ]; then
    compose start brain >/dev/null 2>&1 || true
  fi
  docker rm -f "$restore_name" >/dev/null 2>&1 || true
  if [ -n "$restore_dir" ] && [ -d "$restore_dir" ]; then
    rm -rf "$restore_dir"
  fi
}
trap cleanup EXIT HUP INT TERM

if [ "$mode" = "static" ]; then
  go test ./scripts -count=1 -run 'TestBackupRestoreLivePreservesSourceAndComparesRows|TestBackupRestoreElapsedWithinDeadline'
  echo "backup restore static ok"
  exit 0
fi
if [ "$mode" != "live" ]; then
  echo "usage: $0 [static|live]" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "Docker 不可用" >&2
  exit 2
fi
if [ "$(docker inspect -f '{{.State.Running}}' "$source_container" 2>/dev/null || true)" != "true" ]; then
  echo "源 PostgreSQL 未运行" >&2
  exit 2
fi
if ! compose ps --status running brain | grep -q brain; then
  echo "brain 未运行" >&2
  exit 2
fi

restore_dir=$(mktemp -d "${TMPDIR:-/tmp}/yufeng-restore.XXXXXX")
dump_file="$restore_dir/yufeng.dump"
source_rows="$restore_dir/source.rows"
restore_rows="$restore_dir/restore.rows"
source_sequences="$restore_dir/source.sequences"
restore_sequences="$restore_dir/restore.sequences"
started=$(date +%s)

compose stop brain >/dev/null
brain_stopped=1

table_names() {
  container=$1
  database=$2
  docker exec "$container" psql -X -U yufeng -d "$database" -Atc \
    "SELECT schemaname || '.' || tablename FROM pg_tables WHERE schemaname IN ('public','traffic') ORDER BY schemaname,tablename"
}

write_rows() {
  container=$1
  database=$2
  output=$3
  : >"$output"
  for qualified in $(table_names "$container" "$database"); do
    schema=${qualified%%.*}
    table=${qualified#*.}
    case "$schema$table" in
      *[!A-Za-z0-9_]*)
        echo "不安全的表名：$qualified" >&2
        exit 1
        ;;
    esac
    printf '#table:%s\n' "$qualified" >>"$output"
    docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U yufeng -d "$database" -Atc \
      "SELECT to_jsonb(t)::text FROM \"$schema\".\"$table\" AS t ORDER BY to_jsonb(t)::text" >>"$output"
  done
}

write_sequences() {
  container=$1
  database=$2
  output=$3
  docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U yufeng -d "$database" -Atc \
    "SELECT to_jsonb(s)::text FROM (SELECT schemaname, sequencename, last_value, start_value, increment_by, max_value, min_value, cache_size, cycle FROM pg_sequences WHERE schemaname IN ('public','traffic') ORDER BY schemaname,sequencename) AS s" >"$output"
}

docker exec "$source_container" pg_dump -U yufeng -d yufeng -Fc >"$dump_file"
if [ ! -s "$dump_file" ]; then
  echo "备份文件为空" >&2
  exit 1
fi
write_rows "$source_container" yufeng "$source_rows"
write_sequences "$source_container" yufeng "$source_sequences"

docker run -d --name "$restore_name" \
  -e POSTGRES_DB=yufeng_restore -e POSTGRES_USER=yufeng -e POSTGRES_PASSWORD=yufeng \
  --tmpfs /var/lib/postgresql/data:rw,noexec,nosuid,size=1g postgres:16-alpine >/dev/null
ready=0
for _ in $(seq 1 60); do
  if docker exec "$restore_name" pg_isready -U yufeng -d yufeng_restore >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "临时 PostgreSQL 未就绪" >&2
  exit 1
fi
docker cp "$dump_file" "$restore_name:/tmp/yufeng.dump" >/dev/null
docker exec "$restore_name" pg_restore --exit-on-error --no-owner --no-privileges \
  -U yufeng -d yufeng_restore /tmp/yufeng.dump >/dev/null
write_rows "$restore_name" yufeng_restore "$restore_rows"
write_sequences "$restore_name" yufeng_restore "$restore_sequences"

if ! cmp -s "$source_rows" "$restore_rows"; then
  diff -u "$source_rows" "$restore_rows" >&2 || true
  echo "恢复后的治理与流量表行与源快照不一致" >&2
  exit 1
fi
if ! cmp -s "$source_sequences" "$restore_sequences"; then
  diff -u "$source_sequences" "$restore_sequences" >&2 || true
  echo "恢复后的序列状态与源快照不一致" >&2
  exit 1
fi

compose start brain >/dev/null
brain_stopped=0
brain_ready=0
for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:19090/readyz >/dev/null 2>&1; then
    brain_ready=1
    break
  fi
  sleep 1
done
if [ "$brain_ready" -ne 1 ]; then
  echo "源 brain 恢复超时" >&2
  exit 1
fi

finished=$(date +%s)
elapsed=$((finished - started))
YUFENG_BACKUP_RESTORE_ELAPSED_SECONDS=$elapsed go test ./scripts -count=1 -run '^TestBackupRestoreElapsedWithinDeadline$' >/dev/null
tables=$(table_names "$source_container" yufeng | wc -l | tr -d ' ')
rows=$(grep -vc '^#' "$source_rows" || true)
dump_bytes=$(wc -c <"$dump_file" | tr -d ' ')
if command -v shasum >/dev/null 2>&1; then
  manifest_sha=$(shasum -a 256 "$source_rows" "$source_sequences" | shasum -a 256 | awk '{print $1}')
else
  manifest_sha=$(sha256sum "$source_rows" "$source_sequences" | sha256sum | awk '{print $1}')
fi
python3 - "$elapsed" "$tables" "$rows" "$dump_bytes" "$manifest_sha" "${YUFENG_BACKUP_RESTORE_REPORT:-}" <<'PY'
import json, pathlib, sys
elapsed, tables, rows, dump_bytes, manifest, output_path = sys.argv[1:]
report = {
    "backup_restore_deadline_met": True,
    "committed_row_loss": 0,
    "dump_bytes": int(dump_bytes),
    "elapsed_seconds": int(elapsed),
    "manifest_sha256": manifest,
    "committed_rows": int(rows),
    "restored_tables": int(tables),
    "source_database_preserved": True,
}
rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
print(rendered, end="")
if output_path:
    pathlib.Path(output_path).write_text(rendered, encoding="utf-8")
PY
