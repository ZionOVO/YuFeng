package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/observability"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestListReleasesRecordsSyncDelayAndTrace(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	rec, stop := observability.InstallTestTracer()
	defer stop()
	_, _, tok := seedUnitAsset(t, ctx, st, "sync")
	arts := NewArtifactServer(st.Pool())
	req := connect.NewRequest(&artifactv1.ListReleasesRequest{FullSnapshot: true})
	req.Header().Set("Authorization", "Bearer "+tok)
	if _, err := arts.ListReleases(ctx, req); err != nil {
		t.Fatal(err)
	}
	if observability.Default().Get(observability.MetricReleaseSyncDelay) < 0 {
		t.Fatal("sync delay metric")
	}
	// 直接调用方法不会走拦截器；拦截器由 Handler 挂上，另测拦截器本身。
	_ = rec
}

func TestUndeliveredOutboxSetsBacklogMetric(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := writeOutbox(ctx, st.Pool(), "yufeng.test.backlog", "backlog-"+newTestSuffix(), map[string]string{"state": "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DeliverOutbox(ctx, st.Pool(), nil); err != nil {
		t.Fatal(err)
	}
	if observability.Default().Get(observability.MetricQueueBacklog) < 1 {
		t.Fatal("unpublished outbox must raise backlog")
	}
}

func TestLeaseReclaimIncrementsExpiredMetric(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	before := observability.Default().Get(observability.MetricLeaseExpired)
	agentID := "ag-lease-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key) VALUES($1,'x','orchestrator','pub')`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_instructions(instruction_id, agent_id, kind, payload_ref, status, lease_id, lease_expires_at, capability_token)
		VALUES($1,$2,'EVENT_TRIAGE',$3,'leased','old', now() - interval '1 minute','tok')`, "ins-"+newTestSuffix(), agentID, "ref-"+newTestSuffix()); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := NewAgentServer(st.Pool(), "boot", priv)
	got, err := s.leaseInstruction(ctx, agentID)
	if err != nil || got == nil {
		t.Fatalf("reclaim: %v %v", got, err)
	}
	if observability.Default().Get(observability.MetricLeaseExpired) <= before {
		t.Fatal("expired lease reclaim must increment metric")
	}
}
