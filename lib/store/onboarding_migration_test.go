package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMigrationSQLDefinesOnboardingSlot 锁住引导迁移文本：单行引导表 + 非明文凭据槽。
func TestMigrationSQLDefinesOnboardingSlot(t *testing.T) {
	sql := readMigration(t, "00013_onboarding.sql")
	for _, want := range []string{
		"deployment_onboarding",
		"CHECK (id = 1)",
		"ONBOARDING_STATE_PENDING",
		"credential_slots",
		"secret_hash",
		"secret_ciphertext",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("00013_onboarding.sql missing %q", want)
		}
	}
	if strings.Contains(sql, "secret text") || strings.Contains(sql, "api_key text") {
		t.Error("model secret must not be a plaintext text column")
	}
}

func TestEventTraceIndexMigrationDropsFullJson(t *testing.T) {
	sql := readMigration(t, "00016_event_trace_index.sql")
	if !strings.Contains(sql, "DROP INDEX IF EXISTS events_release_time_idx") {
		t.Fatal("must drop full-json release_traces btree")
	}
	if strings.Contains(sql, "(release_traces::text)") && !strings.Contains(sql, "Down") {
		t.Fatal("up migration must not recreate the full-json btree")
	}
}

func TestCheckTicketMigrationMakesProjectionImmutable(t *testing.T) {
	sql := readMigration(t, "00027_check_tickets.sql")
	for _, want := range []string{
		"event_id          text PRIMARY KEY REFERENCES events(event_id)",
		"ticket_digest",
		"quarantine_reason",
		"check_tickets_immutable",
		"BEFORE UPDATE OR DELETE",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("00027_check_tickets.sql missing %q", want)
		}
	}
}

func TestAnalysisWorkerMigrationSeparatesIdentityAndLeaseAttempts(t *testing.T) {
	sql := readMigration(t, "00028_analysis_workers.sql")
	for _, want := range []string{
		"CREATE TABLE worker_bootstrap",
		"CREATE TABLE worker_identities",
		"CREATE TABLE worker_access_tokens",
		"worker_kind IN ('RUN_SUPERVISOR', 'ANALYSIS_SCORER')",
		"CREATE TABLE analysis_work_items",
		"event_id                text NOT NULL UNIQUE REFERENCES check_tickets(event_id)",
		"lease_epoch             bigint NOT NULL DEFAULT 0",
		"CREATE TABLE analysis_attempts",
		"UNIQUE (analysis_work_id, lease_epoch)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("00028_analysis_workers.sql missing %q", want)
		}
	}
}

func TestTrafficDatabaseRoleMigrationKeepsExactPrivileges(t *testing.T) {
	sql := readMigration(t, "00044_restrict_traffic_database_role.sql")
	for _, want := range []string{
		"NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		"REVOKE CREATE ON SCHEMA traffic",
		"REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA traffic",
		"REVOKE ALL PRIVILEGES ON users, releases, grants, audit_entries",
		"GRANT SELECT, INSERT ON traffic.traffic_windows, traffic.traffic_window_receipts",
		"traffic.review_candidates, traffic.review_case_outbox",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("traffic role migration missing %q", want)
		}
	}
	for _, forbidden := range []string{"GRANT UPDATE", "GRANT DELETE", "GRANT ALL"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("traffic role migration must not contain %q", forbidden)
		}
	}
}

// TestMigrateSeedsSingleOnboardingRow 迁移后必须恰好一行 id=1、初态 PENDING。
func TestMigrateSeedsSingleOnboardingRow(t *testing.T) {
	dsn := os.Getenv("YUFENG_TEST_DSN")
	if dsn == "" {
		t.Skip("YUFENG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	ctx := context.Background()
	st, err := Open(ctx, Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM deployment_onboarding`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deployment_onboarding rows=%d want 1", n)
	}
	var id int
	if err := st.Pool().QueryRow(ctx, `SELECT id FROM deployment_onboarding WHERE id=1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("id=%d", id)
	}
	var def string
	if err := st.Pool().QueryRow(ctx, `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema='public' AND table_name='deployment_onboarding' AND column_name='state'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "ONBOARDING_STATE_PENDING") {
		t.Fatalf("state default=%q", def)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO deployment_onboarding(id, state) VALUES (2, 'ONBOARDING_STATE_PENDING')`); err == nil {
		t.Fatal("id=2 must be rejected")
	}

	var secretCols int
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name IN ('deployment_onboarding','credential_slots')
		  AND column_name IN ('secret','api_key','password')`).Scan(&secretCols); err != nil {
		t.Fatal(err)
	}
	if secretCols != 0 {
		t.Fatal("plaintext secret column present")
	}

	var slotCols int
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='credential_slots'
		  AND column_name IN ('secret_hash','secret_ciphertext')`).Scan(&slotCols); err != nil {
		t.Fatal(err)
	}
	if slotCols != 2 {
		t.Fatalf("credential slot columns=%d want secret_hash + secret_ciphertext", slotCols)
	}
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "migrations", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
