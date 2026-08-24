package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"

	"yufeng/agents/modelgateway"
	"yufeng/agents/runtime"

	agentv1 "yufeng/proto/gen/agentv1"
	commonv1 "yufeng/proto/gen/commonv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
)

func TestJarvisHandleEventTriageCreatesShadow(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agents := NewAgentServer(st.Pool(), "boot", priv)
	gw := NewToolGatewayServer(st.Pool(), priv)
	gw.demoTriage = true
	agentID := "jarvis-f4-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES($1,'x','orchestrator','test-pub')`, agentID); err != nil {
		t.Fatal(err)
	}
	_, assetID, _ := seedUnitAsset(t, ctx, st, "f4")
	keys := proposalDetectionKeys(t, ctx, demoAttackMethod, demoAttackPath, demoAttackQuery)
	clusterID := seedProposalCluster(t, ctx, st.Pool(), assetID, demoAttackPath, demoAttackMethod,
		commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED, keys)
	if err := agents.EnqueueInstruction(ctx, agentID, instructionTriage, clusterID, demoTriageInstructionTools, []string{assetBinding(assetID)}); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := st.Pool().QueryRow(ctx, `SELECT capability_token FROM agent_instructions WHERE payload_ref=$1`, clusterID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Handle(ctx, scriptedDemoProvider{}, localTools{gw: gw}, &agentv1.AgentInstruction{
		Kind: instructionTriage, PayloadRef: clusterID, CapabilityToken: token,
	}, ""); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE created_by=$1 ORDER BY proposed_at DESC LIMIT 1`, agentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "shadow" && state != "canary" && state != "enforce" {
		t.Fatalf("提案后状态至少应为 shadow，实际 %s", state)
	}

	bad := runtime.Handle(ctx, badJSON{}, localTools{gw: gw}, &agentv1.AgentInstruction{
		Kind: instructionSession, PayloadRef: "ses-x", CapabilityToken: token,
	}, "")
	if bad == nil {
		t.Fatal("会话指令在无会话绑定时必须失败")
	}
	var extra int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases WHERE created_by=$1`, agentID).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if extra != 1 {
		t.Fatalf("会话坏输出不得再写发布，实际 %d", extra)
	}
}

type localTools struct {
	gw *ToolGatewayServer
}

func (l localTools) Invoke(ctx context.Context, _, capabilityToken, name, argsJSON string) (string, error) {
	req := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: name, ArgsJson: argsJSON})
	req.Header().Set("Authorization", "Bearer "+capabilityToken)
	resp, err := l.gw.InvokeTool(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Msg.ResultJson, nil
}

type badJSON struct{}

func (badJSON) Complete(context.Context, modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	return modelgateway.ChatResponse{Content: "nope"}, nil
}
