package brain

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestLedgerRetentionRequiresSignedAuditCheckpoint(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	oldEventTime := now.AddDate(0, 0, -40)
	if err := ensureLedgerPartition(ctx, st.Pool(), "events", "events_default", "occurred_at", oldEventTime.Truncate(24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	eventID := "retention-event-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO event_receipts(event_id,payload_digest,occurred_at) VALUES($1,'digest',$2)`, eventID, oldEventTime); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO events(event_id,occurred_at,kind,verdict,payload,payload_digest)
		VALUES($1,$2,'traffic','allow','{}','digest')`, eventID, oldEventTime); err != nil {
		t.Fatal(err)
	}

	oldAuditMonth := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := ensureLedgerPartition(ctx, st.Pool(), "audit_entries", "audit_entries_default", "occurred_at", oldAuditMonth, true); err != nil {
		t.Fatal(err)
	}
	objectID := "retention-audit-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO audit_entries(
		occurred_at,actor_type,actor_id,action,object_type,object_id,details,previous_hash,entry_hash)
		VALUES($1,'user','u1','test.retention','asset',$2,'{}','','retention-head')`, oldAuditMonth.Add(time.Hour), objectID); err != nil {
		t.Fatal(err)
	}

	withoutSigner, err := MaintainLedgerData(ctx, st.Pool(), now, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if withoutSigner.Events != 1 || withoutSigner.AuditEntries != 0 {
		t.Fatalf("unsigned retention=%+v", withoutSigner)
	}
	var auditCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE object_id=$1`, objectID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit removed without checkpoint count=%d err=%v", auditCount, err)
	}

	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := kernel.NewMemorySigner(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(t.TempDir(), "audit-checkpoints.jsonl")
	withSigner, err := MaintainLedgerData(context.Background(), st.Pool(), now, checkpointPath, signer)
	if err != nil {
		t.Fatal(err)
	}
	if withSigner.AuditEntries != 1 || withSigner.AuditPartitions != 1 {
		t.Fatalf("signed retention=%+v", withSigner)
	}
	raw, err := os.ReadFile(checkpointPath)
	if err != nil || len(raw) == 0 {
		t.Fatalf("checkpoint file is empty: %v", err)
	}
	var signature string
	if err := st.Pool().QueryRow(ctx, `SELECT checkpoint_signature FROM audit_partition_anchors
		WHERE partition_name='audit_entries_202601'`).Scan(&signature); err != nil || signature == "" {
		t.Fatalf("audit anchor missing signature=%q err=%v", signature, err)
	}
}
