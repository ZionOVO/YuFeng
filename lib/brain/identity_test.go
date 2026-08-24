package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAgentBootstrapHijack(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok := "bound-" + newTestSuffix()
	agentA := "agent-a-" + newTestSuffix()
	agentB := "agent-b-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), "shared-"+newTestSuffix(), priv)
	if err := SeedAgentBootstrap(ctx, st.Pool(), agentA, tok); err != nil {
		t.Fatal(err)
	}
	hijack := connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentB, BootstrapToken: tok, AgentPublicKey: "k"})
	if _, err := s.RegisterAgent(ctx, hijack); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong agent_id want permission_denied, got %v", err)
	}
	var used *time.Time
	if err := st.Pool().QueryRow(ctx, `SELECT used_at FROM agent_bootstrap WHERE agent_id=$1`, agentA).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != nil {
		t.Fatal("hijack must not consume bootstrap")
	}
	ok := connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentA, BootstrapToken: tok, AgentPublicKey: "k"})
	if _, err := s.RegisterAgent(ctx, ok); err != nil {
		t.Fatal(err)
	}
	reuse := connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentA, BootstrapToken: tok, AgentPublicKey: "k"})
	if _, err := s.RegisterAgent(ctx, reuse); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("reuse want unauthenticated, got %v", err)
	}
}

func TestDualTokenGatewayTable(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw := NewToolGatewayServer(st.Pool(), priv)
	publishTestToolDescriptors(t, ctx, st.Pool(), priv, "event.get")
	boot := "boot-" + newTestSuffix()
	agentID := "gw-agent-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	capOK := signLiveTestCapability(t, ctx, st.Pool(), priv, agentID, []string{"event.get"})
	capOther, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: agentID, AuthorizedParty: "other", Audience: "tools",
		TokenID: "jti-other", Tools: []string{"event.get"}, MaxCalls: 2,
		ExpiresAt: now.Add(time.Hour).Unix(), IssuedAt: now.Unix(),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: agentID, AuthorizedParty: agentID, Audience: "tools",
		TokenID: "jti-exp", Tools: []string{"event.get"}, MaxCalls: 2,
		ExpiresAt: now.Add(-time.Minute).Unix(), IssuedAt: now.Add(-time.Hour).Unix(),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, access, cap string
		want              connect.Code
	}{
		{name: "missing cap", access: reg.Msg.AccessToken, want: connect.CodeUnauthenticated},
		{name: "missing access", cap: capOK, want: connect.CodeUnauthenticated},
		{name: "expired cap", access: reg.Msg.AccessToken, cap: expired, want: connect.CodeUnauthenticated},
		{name: "sub != azp", access: reg.Msg.AccessToken, cap: capOther, want: connect.CodePermissionDenied},
		{name: "ok", access: reg.Msg.AccessToken, cap: capOK, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: "event.get", ArgsJson: `{"event_id":"missing"}`})
			if tc.access != "" {
				req.Header().Set("Authorization", "Bearer "+tc.access)
			}
			if tc.cap != "" {
				req.Header().Set(CapabilityHeader, "Bearer "+tc.cap)
			}
			_, err := gw.InvokeTool(ctx, req)
			if tc.want == 0 {
				if connect.CodeOf(err) == connect.CodeUnauthenticated {
					t.Fatalf("ok path auth failed: %v", err)
				}
				return
			}
			if connect.CodeOf(err) != tc.want {
				t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
			}
		})
	}
}

func TestIdempotencySameAndConflict(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	d1 := requestDigest("govern.propose", `{"a":1}`, "k1")
	d2 := requestDigest("govern.propose", `{"a":2}`, "k1")
	if err := storeIdempotency(ctx, st.Pool(), "tool", "k1", d1, "ok", `{"id":"1"}`); err != nil {
		t.Fatal(err)
	}
	hit, _, body, err := loadIdempotency(ctx, st.Pool(), "tool", "k1", d1)
	if err != nil || !hit || body == "" {
		t.Fatalf("same digest should hit: hit=%v err=%v", hit, err)
	}
	_, _, _, err = loadIdempotency(ctx, st.Pool(), "tool", "k1", d2)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("different digest want failed_precondition, got %v", err)
	}
}

func TestInvokeToolIdempotencyNoDoubleBudget(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw := NewToolGatewayServer(st.Pool(), priv)
	publishTestToolDescriptors(t, ctx, st.Pool(), priv, "event.get")
	boot := "boot-idem-" + newTestSuffix()
	agentID := "agent-idem-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	_, assetID, _ := seedUnitAsset(t, ctx, st, "idem")
	eid := "evt-idem-" + newTestSuffix()
	payload := `{"id":"` + eid + `","assetId":"` + assetID + `"}`
	if _, err := st.Pool().Exec(ctx, `INSERT INTO events(event_id, unit_id, asset_id, request_id, occurred_at, source, kind, verdict, payload)
		VALUES($1,'u',$2,'r',now(),'t','traffic','allow',$3::jsonb)`, eid, assetID, payload); err != nil {
		t.Fatal(err)
	}
	capTok := signLiveTestCapabilityWithBindings(t, ctx, st.Pool(), priv, agentID,
		[]string{"event.get"}, []string{"asset:" + assetID}, 3)
	invoke := func(key, args string) (*toolgatewayv1.InvokeToolResponse, error) {
		req := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: "event.get", ArgsJson: args, IdempotencyKey: key})
		req.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
		req.Header().Set(CapabilityHeader, "Bearer "+capTok)
		resp, err := gw.InvokeTool(ctx, req)
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	args := `{"event_id":"` + eid + `"}`
	first, err := invoke("idem-1", args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := invoke("idem-1", args)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResultJson != second.ResultJson {
		t.Fatalf("retry must return first result")
	}
	if first.CallsRemaining == second.CallsRemaining+1 {
		t.Fatal("retry must not consume budget again")
	}
	_, err = invoke("idem-1", `{"event_id":"other"}`)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("conflict want failed_precondition got %v", err)
	}
}

func TestUploadRejectsUnboundAsset(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, _, token := seedUnitAsset(t, ctx, st, "c9")
	other := "asset-unbound-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, other); err != nil {
		t.Fatal(err)
	}
	tel := NewTelemetryServer(st.Pool(), nil, nil, "")
	ev := &eventv1.Event{Id: "evt-c9-" + newTestSuffix(), AssetId: other, UnitId: unitID, OccurredAt: timestamppb.Now(), Source: "t", Kind: eventv1.Kind_KIND_TRAFFIC}
	req := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{ev}})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := tel.UploadEvents(ctx, req)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("unbound asset want permission_denied, got %v", err)
	}
}

func TestGrantMissing(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := requireUserGrant(ctx, st.Pool(), "user-none", "govern.propose", "asset", "a1"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("want grant_missing, got %v", err)
	}
}
