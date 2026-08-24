package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "yufeng/proto/gen/agentv1"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

func TestProductionTriageCompilesOnePinnedShadowPolicy(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "jarvis-triage-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), "boot-triage", key)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: "boot-triage", AgentPublicKey: "triage-public-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, assetID, _ := seedUnitAsset(t, ctx, st, "triage-complete")
	keys := proposalDetectionKeys(t, ctx, demoAttackMethod, demoAttackPath, demoAttackQuery)
	clusterID := seedProposalCluster(t, ctx, st.Pool(), assetID, demoAttackPath, demoAttackMethod,
		commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED, keys)
	turnID, err := ensureTriageTurn(ctx, st.Pool(), agentID, clusterID)
	if err != nil {
		t.Fatal(err)
	}
	capability := signLiveTestCapabilityWithBindings(t, ctx, st.Pool(), key, agentID,
		triageInstructionTools, []string{assetBinding(assetID), turnBinding(turnID)}, 20)
	gw := NewToolGatewayServer(st.Pool(), key)
	publishTestToolDescriptors(t, ctx, st.Pool(), key, "triage.get", "triage.complete")

	projection, err := invokeDual(ctx, gw, registered.Msg.AccessToken, capability, "triage.get", `{"turn_id":"`+turnID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projection.Msg.ResultJson, clusterID) || !strings.Contains(projection.Msg.ResultJson, keys[0].GetRuleId()) {
		t.Fatalf("projection is not pinned to cluster and full key: %s", projection.Msg.ResultJson)
	}
	for _, forbidden := range []string{"queryRedacted", "headers", "body", "srcPseudonym"} {
		if strings.Contains(projection.Msg.ResultJson, forbidden) {
			t.Fatalf("triage projection leaked %s: %s", forbidden, projection.Msg.ResultJson)
		}
	}

	args := `{"turn_id":"` + turnID + `","decision":{"clusterId":"` + clusterID + `","disposition":"TRIAGE_DISPOSITION_PROPOSE_POLICY","rationale":"mapped core rule is not mitigated"}}`
	completed, err := invokeDual(ctx, gw, registered.Msg.AccessToken, capability, "triage.complete", args)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(completed.Msg.ResultJson), &result); err != nil {
		t.Fatal(err)
	}
	releaseID, _ := result["releaseId"].(string)
	if releaseID == "" || result["state"] != "SHADOW" {
		t.Fatalf("completion=%s", completed.Msg.ResultJson)
	}
	if _, err := invokeDual(ctx, gw, registered.Msg.AccessToken, capability, "govern.start_shadow", `{"release_id":"`+releaseID+`"}`); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("triage capability must not expose governance writes, got %v", err)
	}

	again, err := invokeDual(ctx, gw, registered.Msg.AccessToken, capability, "triage.complete", args)
	if err != nil {
		t.Fatal(err)
	}
	if again.Msg.ResultJson != completed.Msg.ResultJson {
		t.Fatalf("duplicate completion changed result: first=%s second=%s", completed.Msg.ResultJson, again.Msg.ResultJson)
	}
	var decisions, releases int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM triage_decisions WHERE turn_id=$1`, turnID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases WHERE created_by=$1`, agentID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || releases != 1 {
		t.Fatalf("duplicate completion appended decisions=%d releases=%d", decisions, releases)
	}
	var state, evidence, createdBy string
	var rawArtifact []byte
	if err := st.Pool().QueryRow(ctx, `SELECT state, evidence_class, created_by, artifact FROM releases WHERE release_id=$1`, releaseID).
		Scan(&state, &evidence, &createdBy, &rawArtifact); err != nil {
		t.Fatal(err)
	}
	if state != "shadow" || evidence != "crs_mapped" || createdBy != agentID {
		t.Fatalf("release state=%s evidence=%s created_by=%s", state, evidence, createdBy)
	}
	var artifact artifactv1.Artifact
	if err := protojson.Unmarshal(rawArtifact, &artifact); err != nil {
		t.Fatal(err)
	}
	var candidate artifactv1.PolicyCandidate
	if err := protojson.Unmarshal(artifact.GetPayload(), &candidate); err != nil {
		t.Fatal(err)
	}
	if len(candidate.GetPredicate().GetDetectionKeys()) == 0 {
		t.Fatal("compiled policy has no full detection key")
	}
	matched := false
	for _, trusted := range keys {
		for _, compiled := range candidate.GetPredicate().GetDetectionKeys() {
			if proto.Equal(trusted, compiled) {
				matched = true
			}
		}
	}
	if !matched {
		t.Fatalf("compiled keys are not from pinned event: %v", candidate.GetPredicate().GetDetectionKeys())
	}
}

func TestNonAutomaticTriageArtifactsRemainShadow(t *testing.T) {
	t.Run("unmapped policy", func(t *testing.T) {
		state, evidence, releaseID, pool, ctx := completeNonAutomaticTriage(t, commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMAPPED, false)
		if state != "shadow" || evidence != "crs_unmapped" {
			t.Fatalf("unmapped release state=%s evidence=%s", state, evidence)
		}
		blocked, err := skipAutoPromote(ctx, pool, releaseID, false)
		if err != nil || !blocked {
			t.Fatalf("unmapped policy must not auto-promote: blocked=%v err=%v", blocked, err)
		}
	})
	t.Run("shape", func(t *testing.T) {
		state, evidence, releaseID, pool, ctx := completeNonAutomaticTriage(t, commonv1.TriageReason_TRIAGE_REASON_SUSPECTED_MISS, true)
		if state != "shadow" || evidence != "intel" {
			t.Fatalf("shape release state=%s evidence=%s", state, evidence)
		}
		blocked, err := skipAutoPromote(ctx, pool, releaseID, false)
		if err != nil || !blocked {
			t.Fatalf("shape must not auto-promote: blocked=%v err=%v", blocked, err)
		}
	})
}

func completeNonAutomaticTriage(t *testing.T, reason commonv1.TriageReason, shape bool) (string, string, string, *pgxpool.Pool, context.Context) {
	t.Helper()
	st, ctx := openTestStore(t)
	t.Cleanup(st.Close)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "jarvis-nonautomatic-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), "boot-nonautomatic", key)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: "boot-nonautomatic", AgentPublicKey: "nonautomatic-public-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, assetID, _ := seedUnitAsset(t, ctx, st, "nonautomatic")
	var keys []*commonv1.DetectionKey
	if !shape {
		keys = proposalDetectionKeys(t, ctx, demoAttackMethod, demoAttackPath, demoAttackQuery)
	}
	clusterID := seedProposalCluster(t, ctx, st.Pool(), assetID, demoAttackPath, demoAttackMethod, reason, keys)
	if shape {
		var eventID string
		if err := st.Pool().QueryRow(ctx, `SELECT representative FROM triage_clusters WHERE cluster_id=$1`, clusterID).Scan(&eventID); err != nil {
			t.Fatal(err)
		}
		event := &eventv1.Event{
			Id: eventID, OccurredAt: timestamppb.Now(), AssetId: assetID, Source: "test", Kind: eventv1.Kind_KIND_TRAFFIC,
			Verdict: eventv1.Verdict_VERDICT_ALLOW, ClusterId: clusterID,
			Traffic: &eventv1.Event_Http{Http: &eventv1.Http{Method: "GET", Path: "/api/items", QueryRedacted: "page=2"}},
		}
		raw, err := protojson.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `UPDATE events SET payload=$1::jsonb WHERE event_id=$2`, raw, eventID); err != nil {
			t.Fatal(err)
		}
	}
	turnID, err := ensureTriageTurn(ctx, st.Pool(), agentID, clusterID)
	if err != nil {
		t.Fatal(err)
	}
	capability := signLiveTestCapabilityWithBindings(t, ctx, st.Pool(), key, agentID,
		triageInstructionTools, []string{assetBinding(assetID), turnBinding(turnID)}, 20)
	gw := NewToolGatewayServer(st.Pool(), key)
	publishTestToolDescriptors(t, ctx, st.Pool(), key, "triage.complete")
	disposition := "TRIAGE_DISPOSITION_PROPOSE_POLICY"
	extra := ""
	if shape {
		disposition = "TRIAGE_DISPOSITION_PROPOSE_SHAPE"
		extra = `,"optionalShapeDraft":{"methods":["GET"],"routeTemplate":"/api/items","constraints":[{"selector":"query.page","minLen":1,"maxLen":8,"charset":"digit"}]}`
	}
	args := `{"turn_id":"` + turnID + `","decision":{"clusterId":"` + clusterID + `","disposition":"` + disposition + `","rationale":"pinned evidence requires human promotion"` + extra + `}}`
	completed, err := invokeDual(ctx, gw, registered.Msg.AccessToken, capability, "triage.complete", args)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(completed.Msg.ResultJson), &result); err != nil {
		t.Fatal(err)
	}
	releaseID, _ := result["releaseId"].(string)
	var state, evidence string
	if err := st.Pool().QueryRow(ctx, `SELECT state, evidence_class FROM releases WHERE release_id=$1`, releaseID).Scan(&state, &evidence); err != nil {
		t.Fatal(err)
	}
	return state, evidence, releaseID, st.Pool(), ctx
}
