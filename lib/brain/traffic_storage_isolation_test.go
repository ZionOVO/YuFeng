package brain

import (
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"yufeng/lib/store"
)

const postgresInsufficientPrivilege = "42501"

func TestTrafficPoolUsesRestrictedRole(t *testing.T) {
	if os.Getenv("YUFENG_TEST_DSN") != "" && os.Getenv("YUFENG_TRAFFIC_TEST_DSN") == "" {
		t.Fatal("YUFENG_TRAFFIC_TEST_DSN must be set when PostgreSQL integration tests are enabled")
	}
	st, ctx := openTestStore(t)
	defer st.Close()

	if got := st.TrafficPool().Config().MaxConns; got != 4 {
		t.Fatalf("traffic pool max connections = %d, want 4", got)
	}
	if err := store.ValidateRestrictedTrafficRole(ctx, st.Pool(), st.TrafficPool()); err != nil {
		t.Fatalf("restricted traffic role validation failed: %v", err)
	}
	if err := store.ValidateRestrictedTrafficRole(ctx, st.Pool(), st.Pool()); err == nil {
		t.Fatal("governance role unexpectedly accepted as restricted traffic role")
	}

	for _, table := range []string{
		"traffic.traffic_windows",
		"traffic.traffic_window_receipts",
		"traffic.review_candidates",
		"traffic.review_case_outbox",
	} {
		var canSelect, canInsert, canUpdate, canDelete bool
		if err := st.TrafficPool().QueryRow(ctx, `SELECT
			has_table_privilege(current_user, $1, 'SELECT'),
			has_table_privilege(current_user, $1, 'INSERT'),
			has_table_privilege(current_user, $1, 'UPDATE'),
			has_table_privilege(current_user, $1, 'DELETE')`, table).Scan(&canSelect, &canInsert, &canUpdate, &canDelete); err != nil {
			t.Fatalf("query traffic privileges for %s: %v", table, err)
		}
		if !canSelect || !canInsert || canUpdate || canDelete {
			t.Errorf("traffic role privileges for %s = select:%t insert:%t update:%t delete:%t", table, canSelect, canInsert, canUpdate, canDelete)
		}
	}

	tx, err := st.TrafficPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			t.Errorf("rollback traffic role transaction: %v", rollbackErr)
		}
	}()
	receiptID := "traffic-isolation-" + newTestSuffix()
	if _, err := tx.Exec(ctx, `INSERT INTO traffic.traffic_window_receipts(window_id, window_start, payload_digest)
		VALUES($1, now(), 'traffic-isolation')`, receiptID); err != nil {
		t.Fatalf("traffic role cannot insert traffic receipt: %v", err)
	}
	var payloadDigest string
	if err := tx.QueryRow(ctx, `SELECT payload_digest FROM traffic.traffic_window_receipts WHERE window_id=$1`, receiptID).Scan(&payloadDigest); err != nil {
		t.Fatalf("traffic role cannot read inserted traffic receipt: %v", err)
	}
	if payloadDigest != "traffic-isolation" {
		t.Fatalf("traffic receipt payload digest = %q", payloadDigest)
	}

	var schema string
	if err := st.Pool().QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	qualified := func(table string) string {
		return pgx.Identifier{schema, table}.Sanitize()
	}
	denied := []struct {
		name string
		sql  string
	}{
		{name: "read users", sql: `SELECT 1 FROM ` + qualified("users") + ` LIMIT 1`},
		{name: "insert releases", sql: `INSERT INTO ` + qualified("releases") + ` (release_id, state, artifact, ttl_seconds) VALUES ('traffic-denied', 'shadow', '{}', 60)`},
		{name: "update releases", sql: `UPDATE ` + qualified("releases") + ` SET state=state WHERE false`},
		{name: "insert grants", sql: `INSERT INTO ` + qualified("grants") + ` (grant_id, subject_kind, subject_id) VALUES ('traffic-denied', 'agent', 'traffic-denied')`},
		{name: "update grants", sql: `UPDATE ` + qualified("grants") + ` SET subject_id=subject_id WHERE false`},
		{name: "insert audit entries", sql: `INSERT INTO ` + qualified("audit_entries") + ` (actor_type, actor_id, action, object_type, previous_hash, entry_hash) VALUES ('agent', 'traffic-denied', 'denied', 'traffic', '', '')`},
		{name: "update audit entries", sql: `UPDATE ` + qualified("audit_entries") + ` SET actor_id=actor_id WHERE false`},
	}
	for _, tc := range denied {
		if _, err := st.TrafficPool().Exec(ctx, tc.sql); err == nil {
			t.Errorf("traffic role unexpectedly allowed %s", tc.name)
		} else {
			var databaseError *pgconn.PgError
			if !errors.As(err, &databaseError) || databaseError.Code != postgresInsufficientPrivilege {
				t.Errorf("traffic role %s error = %v, want insufficient_privilege", tc.name, err)
			}
		}
	}
}
