package scripts

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestBackupRestoreLivePreservesSourceAndComparesRows(t *testing.T) {
	body := readScript(t, "backup-restore-live.sh")
	for _, want := range []string{
		`pg_dump`, `pg_restore`, `to_jsonb(t)::text`, `pg_sequences`, `cmp -s`,
		`schemaname IN ('public','traffic')`, `BackupRestoreElapsedWithinDeadline`,
		`committed_row_loss`, `source_database_preserved`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("backup restore script missing %q", want)
		}
	}
	for _, forbidden := range []string{"down -v", "docker volume rm", `docker rm -f "$source_container"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("backup restore script must preserve the source database, found %q", forbidden)
		}
	}
}

func TestBackupRestoreElapsedWithinDeadline(t *testing.T) {
	raw, ok := os.LookupEnv("YUFENG_BACKUP_RESTORE_ELAPSED_SECONDS")
	if !ok {
		t.Skip("live restore supplies elapsed seconds")
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 {
		t.Fatalf("elapsed seconds=%q", raw)
	}
	if elapsed := time.Duration(seconds) * time.Second; elapsed > kernel.BackupRestoreDeadline {
		t.Fatalf("restore elapsed=%s exceeds deadline=%s", elapsed, kernel.BackupRestoreDeadline)
	}
}
