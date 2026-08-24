package brain

import (
	"crypto/ed25519"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"

	agentv1 "yufeng/proto/gen/agentv1"
)

func TestPollBindingsFromClusterNotLatestEvent(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	priv := mustKey(t)
	clusterAsset := "asset-clu-" + newTestSuffix()
	recentAsset := "asset-recent-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1'),($2,$2,'L1')`, clusterAsset, recentAsset); err != nil {
		t.Fatal(err)
	}
	evtCluster := "evt-old-" + newTestSuffix()
	evtRecent := "evt-new-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO events(event_id, occurred_at, asset_id, kind, verdict, payload)
		VALUES($1, now() - interval '1 hour', $2, 'KIND_TRAFFIC', 'allow', '{}')`, evtCluster, clusterAsset); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO events(event_id, occurred_at, asset_id, kind, verdict, payload)
		VALUES($1, now(), $2, 'KIND_TRAFFIC', 'allow', '{}')`, evtRecent, recentAsset); err != nil {
		t.Fatal(err)
	}
	clusterID := "clu_" + newTestSuffix()
	if clusterID == evtCluster || clusterID == evtRecent {
		t.Fatal("cluster_id must differ from event ids")
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO triage_clusters(cluster_id, asset_id, route_template, method, identity_key, reason, event_ids, representative)
		VALUES($1,$2,'/api/items','GET','key:942100','TRIAGE_REASON_DETECTED_UNMITIGATED',$3::jsonb,$4)`,
		clusterID, clusterAsset, `["`+evtCluster+`"]`, evtCluster); err != nil {
		t.Fatal(err)
	}

	boot := "boot-g0c-" + newTestSuffix()
	agentID := "agent-g0c-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := ensureTriageTurn(ctx, st.Pool(), agentID, clusterID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueInstruction(ctx, agentID, instructionTriage, turnID, triageInstructionTools,
		[]string{assetBinding(clusterAsset), turnBinding(turnID)}); err != nil {
		t.Fatal(err)
	}
	poll := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: agentID, LongPollSeconds: 1})
	poll.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	ins, err := s.PollInstructions(ctx, poll)
	if err != nil || len(ins.Msg.Instructions) == 0 {
		t.Fatalf("poll %v", err)
	}
	got := ins.Msg.Instructions[0]
	if got.PayloadRef != turnID || got.TurnId != turnID {
		t.Fatalf("instruction refs payload=%q turn=%q want %q", got.PayloadRef, got.TurnId, turnID)
	}
	var sourceRef, pinnedAsset string
	if err := st.Pool().QueryRow(ctx, `SELECT th.source_ref, t.input_snapshot->>'assetId'
		FROM agent_turns t JOIN agent_threads th ON th.thread_id=t.thread_id WHERE t.turn_id=$1`, turnID).
		Scan(&sourceRef, &pinnedAsset); err != nil {
		t.Fatal(err)
	}
	if sourceRef != clusterID || pinnedAsset != clusterAsset {
		t.Fatalf("turn source=%q asset=%q want cluster=%q asset=%q", sourceRef, pinnedAsset, clusterID, clusterAsset)
	}
	claims, err := kernel.VerifyCapabilityToken(got.CapabilityToken, priv.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantAsset := assetBinding(clusterAsset)
	wantTurn := turnBinding(turnID)
	if len(claims.Bindings) != 2 || !containsAll(claims.Bindings, wantAsset, wantTurn) {
		t.Fatalf("bindings=%v want [%s %s] (must not fall back to latest event %s)",
			claims.Bindings, wantAsset, wantTurn, assetBinding(recentAsset))
	}
}
