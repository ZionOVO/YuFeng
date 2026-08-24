package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
)

func TestToolGatewayDemoRepairAuthorization(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw := NewToolGatewayServer(st.Pool(), priv)
	gw.demoTriage = true
	_, assetID, _ := seedUnitAsset(t, ctx, st, "f3")
	otherAsset := "asset-other-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, otherAsset); err != nil {
		t.Fatal(err)
	}

	sessionTok := signToolToken(t, priv, "jarvis-f3", []string{"session.reply"}, []string{"ses_dummy"})
	triageTok := signToolToken(t, priv, "jarvis-f3", demoTriageInstructionTools, []string{assetBinding(assetID)})

	rules, err := edgecore.MarshalRules([]edgecore.Rule{
		{ID: "sql-union", Pattern: `(?i)union\s+select`},
		{ID: "xss-script", Pattern: `(?i)<script`},
		{ID: "path-traversal", Pattern: `\.\./`},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposeArgs := map[string]any{
		"kind": "KIND_RULE", "payload_schema": edgecore.RulePayloadSchema,
		"payload": string(rules),
		"scope":   map[string]any{"asset_ids": []string{assetID}},
		"ttl":     "86400s", "created_by": "forged-user",
	}
	raw, _ := json.Marshal(proposeArgs)

	var before int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(ctx, gw, sessionTok, "govern.propose", string(raw)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("会话令牌调 propose 应为 permission_denied，实际 %v", err)
	}
	var afterSession int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&afterSession); err != nil {
		t.Fatal(err)
	}
	if afterSession != before {
		t.Fatal("会话令牌 propose 不得写 releases")
	}

	cross := map[string]any{
		"kind": "KIND_RULE", "payload_schema": edgecore.RulePayloadSchema,
		"payload": string(rules),
		"scope":   map[string]any{"asset_ids": []string{otherAsset}},
		"ttl":     "86400s",
	}
	crossRaw, _ := json.Marshal(cross)
	if _, err := invoke(ctx, gw, triageTok, "govern.propose", string(crossRaw)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("越资产 propose 应为 permission_denied，实际 %v", err)
	}

	if _, err := invoke(ctx, gw, triageTok, "govern.promote_enforce", `{"release_id":"x"}`); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("triage 调 promote 应为 permission_denied，实际 %v", err)
	}

	resp, err := invoke(ctx, gw, triageTok, "govern.propose", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(resp.Msg.ResultJson), &body); err != nil {
		t.Fatal(err)
	}
	relID, _ := body["releaseId"].(string)
	if relID == "" {
		t.Fatalf("提案无 releaseId: %s", resp.Msg.ResultJson)
	}
	var createdBy string
	if err := st.Pool().QueryRow(ctx, `SELECT created_by FROM releases WHERE release_id=$1`, relID).Scan(&createdBy); err != nil {
		t.Fatal(err)
	}
	if createdBy != "jarvis-f3" {
		t.Fatalf("created_by 应为 agent_id，实际 %s", createdBy)
	}
}

func signToolToken(t *testing.T, key ed25519.PrivateKey, sub string, tools, bindings []string) string {
	t.Helper()
	now := time.Now()
	tok, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: sub, Role: "orchestrator", Audience: "tools", TokenID: "jti-" + newTestSuffix(),
		ExpiresAt: now.Add(time.Hour).Unix(), IssuedAt: now.Unix(), NotBefore: now.Add(-time.Second).Unix(),
		Tools: tools, Bindings: bindings, MaxCalls: 20,
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func invoke(ctx context.Context, gw *ToolGatewayServer, token, tool, args string) (*connect.Response[toolgatewayv1.InvokeToolResponse], error) {
	req := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: tool, ArgsJson: args})
	req.Header().Set("Authorization", "Bearer "+token)
	return gw.InvokeTool(ctx, req)
}

func TestCaseToolsRequireExactCaseBindingWithinSameAsset(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	assetID := "asset-case-scope-" + newTestSuffix()
	caseOne := "case-scope-one-" + newTestSuffix()
	caseTwo := "case-scope-two-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$3,'open',80,'one'),($2,'traffic-interception',$3,'open',80,'two')`,
		caseOne, caseTwo, assetID); err != nil {
		t.Fatal(err)
	}
	server := &ToolGatewayServer{pool: st.Pool()}
	claims := kernel.Claims{Bindings: []string{assetBinding(assetID), "case:" + caseOne}}
	if err := server.authorizeToolArgs(ctx, claims, "case.get", `{"case_id":"`+caseOne+`"}`); err != nil {
		t.Fatalf("bound case must be readable: %v", err)
	}
	if err := server.authorizeToolArgs(ctx, claims, "case.get", `{"case_id":"`+caseTwo+`"}`); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("same-asset cross-case access want permission_denied got %v", err)
	}
}

func TestToolGateUsesArtifactSigner(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, tokenKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, artifactKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := kernel.NewMemorySigner(artifactKey)
	if err != nil {
		t.Fatal(err)
	}
	gw := NewToolGatewayServer(st.Pool(), tokenKey)
	gw.demoTriage = true
	gw.artifactSigner = signer
	_, assetID, _ := seedUnitAsset(t, ctx, st, "sign")
	if _, err := st.Pool().Exec(ctx, `DELETE FROM asset_generations WHERE asset_id=$1`, assetID); err != nil {
		t.Fatal(err)
	}
	tok := signToolToken(t, tokenKey, "jarvis-sign", demoTriageInstructionTools, []string{assetBinding(assetID)})
	genID, err := publishBaselineGeneration(ctx, st.Pool(), tokenKey, signer, assetID, "jarvis-sign")
	if err != nil {
		t.Fatal(err)
	}
	if genID == "" {
		t.Fatal("baseline generation id empty")
	}
	keys := proposalDetectionKeys(t, ctx, "GET", "/api/items", "id=1+UNION+SELECT+password")
	clusterID := seedProposalCluster(t, ctx, st.Pool(), assetID, "/api/items", "GET",
		commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED, keys)
	propose, err := json.Marshal(map[string]any{
		"intent": map[string]any{
			"kind":      "PROPOSAL_KIND_POLICY",
			"clusterId": clusterID,
			"detectionKeys": []map[string]any{
				{"detectorId": "crs", "ruleId": keys[0].GetRuleId(), "targetLocation": keys[0].GetTargetLocation().String()},
			},
		},
		"scope": map[string]any{"assetIds": []string{assetID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prop, err := invoke(ctx, gw, tok, "govern.propose", string(propose))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(prop.Msg.ResultJson), &body); err != nil {
		t.Fatal(err)
	}
	relID, _ := body["releaseId"].(string)
	if relID == "" {
		t.Fatalf("propose missing releaseId: %s", prop.Msg.ResultJson)
	}
	gated, err := invoke(ctx, gw, tok, "govern.gate", `{"release_id":"`+relID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(gated.Msg.ResultJson), &body); err != nil {
		t.Fatal(err)
	}
	if body["state"] != "SIGNED" {
		t.Fatalf("gate want SIGNED: %s", gated.Msg.ResultJson)
	}
	rel, err := loadRelease(ctx, st.Pool(), relID)
	if err != nil {
		t.Fatal(err)
	}
	art := rel.Artifact()
	if err := kernel.VerifyArtifact(art, artifactKey.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("gated artifact must verify with artifact signer: %v", err)
	}
	if err := kernel.VerifyArtifact(art, tokenKey.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("gated artifact must not verify with the capability-token key")
	}

	var raw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT envelope FROM asset_generations WHERE generation_id=$1`, genID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var gen artifactv1.AssetGeneration
	if err := protojson.Unmarshal(raw, &gen); err != nil {
		t.Fatal(err)
	}
	if len(gen.Members) == 0 || gen.Members[0].Artifact == nil {
		t.Fatal("baseline missing member")
	}
	base := gen.Members[0].Artifact
	if err := kernel.VerifyArtifact(base, artifactKey.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("baseline must verify with artifact signer: %v", err)
	}
	if err := kernel.VerifyArtifact(base, tokenKey.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("baseline must not verify with the capability-token key")
	}
}
