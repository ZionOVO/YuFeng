package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestFreshDatabaseRestore(t *testing.T) {
	dsn := os.Getenv("YUFENG_TEST_DSN")
	if dsn == "" {
		t.Skip("YUFENG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	ctx := context.Background()
	src, err := Open(ctx, Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(src.Close)
	if err := src.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	eid := "evt-bak-restore-" + time.Now().UTC().Format("150405.000000000")
	rid := "rel-bak-restore-" + time.Now().UTC().Format("150405.000000000")
	_, _ = src.Pool().Exec(ctx, `DELETE FROM events WHERE event_id=$1`, eid)
	_, _ = src.Pool().Exec(ctx, `DELETE FROM releases WHERE release_id=$1`, rid)
	if _, err := src.Pool().Exec(ctx, `INSERT INTO events(event_id, occurred_at, asset_id, kind, verdict, payload)
		VALUES($1, now(), 'a', 'KIND_TRAFFIC', 'allow', '{"keep":true}')`, eid); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds)
		VALUES($1,'shadow','{"kind":"KIND_POLICY"}',86400)`, rid); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	snap, err := DumpLedger(ctx, src.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Pool().Exec(ctx, `DELETE FROM events WHERE event_id=$1`, eid); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Pool().Exec(ctx, `DELETE FROM releases WHERE release_id=$1`, rid); err != nil {
		t.Fatal(err)
	}
	if err := RestoreLedger(ctx, src.Pool(), snap); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > kernel.BackupRestoreDeadline {
		t.Fatalf("restore took %s, deadline %s", took, kernel.BackupRestoreDeadline)
	}
	var n int
	if err := src.Pool().QueryRow(ctx, `SELECT count(*) FROM events WHERE event_id=$1`, eid).Scan(&n); err != nil || n != 1+kernel.BackupCommittedRPO {
		t.Fatalf("RPO: restored events=%d err=%v", n, err)
	}
	if err := src.Pool().QueryRow(ctx, `SELECT count(*) FROM releases WHERE release_id=$1`, rid).Scan(&n); err != nil || n != 1+kernel.BackupCommittedRPO {
		t.Fatalf("RPO: restored releases=%d err=%v", n, err)
	}
}

func TestRestoreLedgerIsAtomicAndIdempotent(t *testing.T) {
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

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	firstEventID := "evt-restore-atomic-first-" + suffix
	secondEventID := "evt-restore-atomic-second-" + suffix
	releaseID := "rel-restore-atomic-" + suffix
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := st.Pool().Exec(cleanupCtx, `DELETE FROM events WHERE event_id=ANY($1::text[])`,
			[]string{firstEventID, secondEventID}); err != nil {
			t.Errorf("cleanup restored events: %v", err)
		}
		if _, err := st.Pool().Exec(cleanupCtx, `DELETE FROM event_receipts WHERE event_id=ANY($1::text[])`,
			[]string{firstEventID, secondEventID}); err != nil {
			t.Errorf("cleanup restored event receipts: %v", err)
		}
		if _, err := st.Pool().Exec(cleanupCtx, `DELETE FROM releases WHERE release_id=$1`, releaseID); err != nil {
			t.Errorf("cleanup restored release: %v", err)
		}
	})

	firstOccurredAt := time.Now().UTC().Truncate(time.Microsecond)
	secondOccurredAt := firstOccurredAt.Add(time.Second)
	snapshot := &LedgerSnapshot{
		Events: []map[string]any{
			{
				"event_id": firstEventID, "asset_id": "asset-restore", "kind": "KIND_TRAFFIC",
				"verdict": "allow", "payload": `{"valid":true}`, "occurred_at": firstOccurredAt,
				"payload_digest": "digest-first",
			},
			{
				"event_id": secondEventID, "asset_id": "asset-restore", "kind": "KIND_TRAFFIC",
				"verdict": "allow", "payload": `{`, "occurred_at": secondOccurredAt,
				"payload_digest": "digest-second",
			},
		},
		Releases: []map[string]any{{
			"release_id": releaseID, "state": "shadow", "artifact": `{"kind":"KIND_POLICY"}`,
			"ttl_seconds": int32(86400),
		}},
	}
	if err := RestoreLedger(ctx, st.Pool(), snapshot); err == nil {
		t.Fatal("restore with an invalid second event must fail")
	}
	var eventCount, receiptCount, releaseCount int
	if err := st.Pool().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM events WHERE event_id=ANY($1::text[])),
		(SELECT count(*) FROM event_receipts WHERE event_id=ANY($1::text[])),
		(SELECT count(*) FROM releases WHERE release_id=$2)`,
		[]string{firstEventID, secondEventID}, releaseID).Scan(&eventCount, &receiptCount, &releaseCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || receiptCount != 0 || releaseCount != 0 {
		t.Fatalf("failed restore left events=%d receipts=%d releases=%d", eventCount, receiptCount, releaseCount)
	}

	staleOccurredAt := firstOccurredAt.Add(-time.Hour)
	if _, err := st.Pool().Exec(ctx, `INSERT INTO event_receipts(event_id,payload_digest,occurred_at)
		VALUES($1,'stale-digest',$2)`, firstEventID, staleOccurredAt); err != nil {
		t.Fatal(err)
	}
	snapshot.Events[1]["payload"] = `{"valid":true}`
	for attempt := 0; attempt < 2; attempt++ {
		if err := RestoreLedger(ctx, st.Pool(), snapshot); err != nil {
			t.Fatalf("restore attempt %d: %v", attempt+1, err)
		}
	}
	var receiptOccurredAt, eventOccurredAt time.Time
	var receiptDigest string
	if err := st.Pool().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM events WHERE event_id=ANY($1::text[])),
		(SELECT count(*) FROM event_receipts WHERE event_id=ANY($1::text[])),
		(SELECT count(*) FROM releases WHERE release_id=$2),
		(SELECT occurred_at FROM event_receipts WHERE event_id=$3),
		(SELECT payload_digest FROM event_receipts WHERE event_id=$3),
		(SELECT occurred_at FROM events WHERE event_id=$3)`,
		[]string{firstEventID, secondEventID}, releaseID, firstEventID).
		Scan(&eventCount, &receiptCount, &releaseCount, &receiptOccurredAt, &receiptDigest, &eventOccurredAt); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 || receiptCount != 2 || releaseCount != 1 {
		t.Fatalf("idempotent restore rows events=%d receipts=%d releases=%d", eventCount, receiptCount, releaseCount)
	}
	if !receiptOccurredAt.Equal(firstOccurredAt) || !eventOccurredAt.Equal(firstOccurredAt) || receiptDigest != "digest-first" {
		t.Fatalf("restored receipt time=%s digest=%q event time=%s", receiptOccurredAt, receiptDigest, eventOccurredAt)
	}
}

func TestSnapshotTTLRejectsInvalidAndOverflowingValues(t *testing.T) {
	defaultTTL := int32(kernel.TTLDefault.Seconds())
	maxTTL := int64(kernel.TTLMax.Seconds())
	tests := []struct {
		name  string
		value any
		want  int32
	}{
		{name: "int", value: int(300), want: 300},
		{name: "int64", value: int64(301), want: 301},
		{name: "float64", value: float64(302), want: 302},
		{name: "maximum", value: maxTTL, want: int32(maxTTL)},
		{name: "zero", value: 0, want: defaultTTL},
		{name: "negative", value: int64(-1), want: defaultTTL},
		{name: "above_contract_maximum", value: maxTTL + 1, want: defaultTTL},
		{name: "above_int32", value: int64(math.MaxInt32) + 1, want: defaultTTL},
		{name: "not_a_number", value: math.NaN(), want: defaultTTL},
		{name: "positive_infinity", value: math.Inf(1), want: defaultTTL},
		{name: "negative_infinity", value: math.Inf(-1), want: defaultTTL},
		{name: "fractional", value: 300.5, want: defaultTTL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := snapshotTTL(test.value); got != test.want {
				t.Fatalf("snapshotTTL(%v) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
