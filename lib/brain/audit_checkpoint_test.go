package brain

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestAuditCheckpointDetectsRewrittenHead(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	objectID := "ckpt-" + newTestSuffix()
	if err := appendAudit(ctx, st.Pool(), "user", "u1", "test.checkpoint", "release", objectID, map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	var seq int64
	var head string
	if err := st.Pool().QueryRow(ctx, `SELECT sequence, entry_hash FROM audit_entries WHERE object_id=$1`, objectID).Scan(&seq, &head); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audit.ckpt")
	if err := AppendAuditCheckpointFile(ctx, st.Pool(), path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.VerifyAuditCheckpoint(bytes.NewReader(raw), seq, head); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE audit_entries SET entry_hash='ff' WHERE sequence=$1`, seq); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `UPDATE audit_entries SET entry_hash=$1 WHERE sequence=$2`, head, seq)
	})
	var nowHead string
	if err := st.Pool().QueryRow(ctx, `SELECT entry_hash FROM audit_entries WHERE sequence=$1`, seq).Scan(&nowHead); err != nil {
		t.Fatal(err)
	}
	if err := kernel.VerifyAuditCheckpoint(bytes.NewReader(raw), seq, nowHead); err == nil {
		t.Fatal("checkpoint must detect rewritten audit head")
	}
}

func TestAuditCheckpointLoopAppendsFile(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := appendAudit(ctx, st.Pool(), "user", "u1", "test.checkpoint.loop", "release", "loop-"+newTestSuffix(), nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audit.ckpt")
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	StartAuditCheckpointLoop(loopCtx, st.Pool(), path, 40*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && len(bytes.TrimSpace(raw)) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("checkpoint loop must write append-only file")
}
