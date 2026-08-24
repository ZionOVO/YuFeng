package brain

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedEventReceiptForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO event_receipts(event_id, payload_digest, occurred_at)
		VALUES($1, 'sha256:test-fixture', now()) ON CONFLICT(event_id) DO NOTHING`, eventID); err != nil {
		t.Fatal(err)
	}
}
