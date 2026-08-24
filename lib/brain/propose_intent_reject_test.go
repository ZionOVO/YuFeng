package brain

import (
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/edgecore"

	agentv1 "yufeng/proto/gen/agentv1"
	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
)

func TestProductionRejectsRuleAndMissingIntent(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "g0k-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	assetID := "asset-intent-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	if err := insertUserGrant(ctx, st.Pool(), login.Msg.User.UserId, []string{"govern.propose"}, assetID); err != nil {
		t.Fatal(err)
	}
	gov := NewGovernServer(st.Pool(), mustKey(t), 0, 0, 0, 0)
	rules, err := edgecore.MarshalRules([]edgecore.Rule{{ID: "sql-union", Pattern: `(?i)union\s+select`}})
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ruleReq := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Kind: artifactv1.Kind_KIND_RULE, Payload: rules, PayloadSchema: edgecore.RulePayloadSchema,
		Scope: &artifactv1.Scope{AssetIds: []string{assetID}},
	})
	ruleReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := gov.ProposeArtifact(ctx, ruleReq); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ProposeArtifact KIND_RULE want failed_precondition got %v", err)
	}
	noIntent := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Scope: &artifactv1.Scope{AssetIds: []string{assetID}},
	})
	noIntent.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := gov.ProposeArtifact(ctx, noIntent); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ProposeArtifact missing intent want failed_precondition got %v", err)
	}
	withIntentRule := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Kind:          artifactv1.Kind_KIND_RULE,
		PayloadSchema: edgecore.RulePayloadSchema,
		Intent:        &governv1.ProposalIntent{Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY},
		Scope:         &artifactv1.Scope{AssetIds: []string{assetID}},
	})
	withIntentRule.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := gov.ProposeArtifact(ctx, withIntentRule); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("intent+KIND_RULE want failed_precondition got %v", err)
	}
	var afterHuman int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&afterHuman); err != nil {
		t.Fatal(err)
	}
	if afterHuman != before {
		t.Fatalf("human propose must not write draft: before=%d after=%d", before, afterHuman)
	}

	priv := mustKey(t)
	gw := NewToolGatewayServer(st.Pool(), priv)
	publishTestToolDescriptors(t, ctx, st.Pool(), priv, "govern.propose")
	var beforeTool int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&beforeTool); err != nil {
		t.Fatal(err)
	}
	agentID := "jarvis-intent-" + newTestSuffix()
	boot := "boot-g0k-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	capTok := signLiveTestCapabilityWithBindings(t, ctx, st.Pool(), priv, agentID,
		demoTriageInstructionTools, []string{assetBinding(assetID)}, 8)
	raw, err := json.Marshal(map[string]any{
		"kind": "KIND_RULE", "payload_schema": edgecore.RulePayloadSchema,
		"payload": string(rules),
		"scope":   map[string]any{"asset_ids": []string{assetID}},
		"ttl":     "86400s",
	})
	if err != nil {
		t.Fatal(err)
	}
	inv := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: "govern.propose", ArgsJson: string(raw)})
	inv.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	inv.Header().Set(CapabilityHeader, "Bearer "+capTok)
	if _, err := gw.InvokeTool(ctx, inv); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("toolgateway KIND_RULE want failed_precondition got %v", err)
	}
	var afterTool int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&afterTool); err != nil {
		t.Fatal(err)
	}
	if afterTool != beforeTool {
		t.Fatalf("toolgateway fixture must not write draft: before=%d after=%d", beforeTool, afterTool)
	}
}
