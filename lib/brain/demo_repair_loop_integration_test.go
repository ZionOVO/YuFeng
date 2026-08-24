package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"connectrpc.com/connect"
	"yufeng/agents/runtime"
	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	"yufeng/lib/store"

	agentv1 "yufeng/proto/gen/agentv1"
	artifactv1 "yufeng/proto/gen/artifactv1"
	"yufeng/proto/gen/artifactv1/artifactv1connect"
	assetv1 "yufeng/proto/gen/assetv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	registryv1 "yufeng/proto/gen/registryv1"
	"yufeng/proto/gen/registryv1/registryv1connect"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	"yufeng/proto/gen/telemetryv1/telemetryv1connect"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	"yufeng/proto/gen/toolgatewayv1/toolgatewayv1connect"
)

// TestDemoRepairLoopAllowsThenBlocksAttack 验证攻击先被放行、智能代理提案、零门槛自动生效后再次请求被拒绝。
// 演示门槛全零仅本测试使用。
func TestDemoRepairLoopAllowsThenBlocksAttack(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	mux := NewMux(st, BuildInfo{Version: "test", ContractVersion: "v1"}, Options{
		SessionTTL: time.Hour, PasswordMinLength: MinPasswordLength,
		SigningKey: priv, AgentBootstrapToken: "boot", UnitBootstrapToken: "unit-boot",
		JarvisAgentID: "jarvis-f5", DemoTriage: true,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	schedCtx, stopSched := context.WithCancel(ctx)
	t.Cleanup(stopSched)
	// 仅演示：时长与请求门槛为 0。
	StartScheduler(schedCtx, st.Pool(), SchedulerConfig{Interval: 50 * time.Millisecond, DemoTriage: true, SigningKey: priv})

	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES('jarvis-f5','x','orchestrator','f5-pub') ON CONFLICT(agent_id) DO UPDATE SET public_key='f5-pub'`); err != nil {
		t.Fatal(err)
	}

	regClient := registryv1connect.NewRegistryServiceClient(srv.Client(), srv.URL)
	regReq := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: "unit-f5-" + newTestSuffix(), Kind: registryv1.UnitKind_UNIT_KIND_EDGE,
		Version: "test", ContractVersion: "v1", PubkeyHint: kernel.KeyID(pub),
		Asset:        &assetv1.Asset{Id: "asset-f5-" + newTestSuffix(), DisplayName: "f5", MaxAutoTier: commonv1.Tier_TIER_L1_TRAFFIC},
		Capabilities: testEdgeCapabilities(),
	})
	regReq.Header().Set("Authorization", "Bearer unit-boot")
	reg, err := regClient.Register(ctx, regReq)
	if err != nil {
		t.Fatal(err)
	}
	unitTok := reg.Msg.Token
	assetID := reg.Msg.AssetId
	if _, err := publishBaselineGeneration(ctx, st.Pool(), priv, nil, assetID, "jarvis-f5"); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)
	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	set := edgecore.NewReleaseSet()
	if err := edgecore.InstallSignedCRS(set, pub, priv); err != nil {
		t.Fatal(err)
	}
	proxy := edgecore.NewReleaseProxy(set, nil, upURL, assetID)
	var lastBlock *eventv1.Event
	tel := telemetryv1connect.NewTelemetryServiceClient(srv.Client(), srv.URL)
	proxy.SetObserver(func(req edgecore.Request, dec edgecore.Decision, requestID string) {
		ev := decisionToEvent(reg.Msg.UnitId, assetID, requestID, req, dec)
		if dec.Action == edgecore.ActionBlock {
			cp := ev
			lastBlock = cp
		}
		ureq := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{ev}})
		ureq.Header().Set("Authorization", "Bearer "+unitTok)
		if _, err := tel.UploadEvents(ctx, ureq); err != nil {
			t.Errorf("上传事件: %v", err)
		}
	})
	edge := httptest.NewServer(proxy)
	t.Cleanup(edge.Close)

	attack := edge.URL + demoAttackPath + "?" + demoAttackQuery
	res1, err := http.Get(attack)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res1.Body)
	if err := res1.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("第一次应为 200，实际 %d", res1.StatusCode)
	}

	var insID, lease, cap string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := st.Pool().QueryRow(ctx, `SELECT instruction_id, COALESCE(lease_id,''), capability_token
			FROM agent_instructions WHERE kind=$1 AND payload_ref IN (
			  SELECT cluster_id FROM triage_clusters WHERE asset_id=$2
			) ORDER BY created_at DESC LIMIT 1`, instructionTriage, assetID).Scan(&insID, &lease, &cap)
		if err == nil && cap != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cap == "" {
		t.Fatal("漏拦后未入队 EVENT_TRIAGE")
	}
	if err := runtime.Handle(ctx, scriptedDemoProvider{}, muxTools{url: srv.URL, client: srv.Client()}, &agentv1.AgentInstruction{
		Kind: instructionTriage, PayloadRef: instructionPayload(ctx, st, insID), CapabilityToken: cap,
	}, ""); err != nil {
		t.Fatal(err)
	}
	_ = lease

	var relID, state string
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := st.Pool().QueryRow(ctx, `SELECT release_id, state FROM releases WHERE created_by='jarvis-f5' ORDER BY proposed_at DESC LIMIT 1`).Scan(&relID, &state)
		if err == nil && state == "enforce" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state != "enforce" {
		t.Fatalf("门槛 0 应自动推进到 enforce，实际 state=%s id=%s", state, relID)
	}

	artClient := artifactv1connect.NewArtifactServiceClient(srv.Client(), srv.URL)
	list := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: reg.Msg.UnitId, AssetId: assetID})
	list.Header().Set("Authorization", "Bearer "+unitTok)
	generations, err := artClient.ListGenerations(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	for _, generation := range generations.Msg.GetGenerations() {
		if err := set.ApplyGeneration(generation, pub); err != nil {
			t.Fatal(err)
		}
	}
	current := set.CurrentGeneration()
	if current == nil {
		t.Fatal("修复后未装载资产世代")
	}
	promotedMember := false
	for _, member := range current.GetMembers() {
		if member.GetReleaseId() == relID && member.GetMode() == commonv1.ReleaseMode_RELEASE_MODE_ENFORCE {
			promotedMember = true
			break
		}
	}
	if !promotedMember {
		t.Fatalf("最新资产世代未包含已生效发布 release_id=%s generation=%d", relID, current.GetGenerationSeq())
	}

	res2, err := http.Get(attack)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res2.Body)
	if err := res2.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != http.StatusForbidden {
		t.Fatalf("第二次应为 403，实际 %d", res2.StatusCode)
	}
	if lastBlock == nil {
		t.Fatal("拦截事件未产生")
	}
	found := false
	for _, tr := range lastBlock.ReleaseTraces {
		if tr.ReleaseId == relID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("拦截事件未带 release_id=%s traces=%v", relID, lastBlock.ReleaseTraces)
	}
}

func instructionPayload(ctx context.Context, st *store.Store, id string) string {
	var ref string
	_ = st.Pool().QueryRow(ctx, `SELECT payload_ref FROM agent_instructions WHERE instruction_id=$1`, id).Scan(&ref)
	return ref
}

func decisionToEvent(unitID, assetID, requestID string, req edgecore.Request, dec edgecore.Decision) *eventv1.Event {
	return edgecore.TrafficEvent(unitID, assetID, requestID, req, dec, edgecore.SourcePseudonymizer{})
}

type muxTools struct {
	url    string
	client *http.Client
}

func (m muxTools) Invoke(ctx context.Context, _, capabilityToken, name, argsJSON string) (string, error) {
	c := toolgatewayv1connect.NewToolGatewayServiceClient(m.client, m.url)
	req := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: name, ArgsJson: argsJSON})
	req.Header().Set("Authorization", "Bearer "+capabilityToken)
	resp, err := c.InvokeTool(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Msg.ResultJson, nil
}
