package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
	artifactv1 "yufeng/proto/gen/artifactv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	toolv1 "yufeng/proto/gen/toolv1"
)

func TestListToolsOnlyPublishedDescriptors(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw := NewToolGatewayServer(st.Pool(), priv)
	boot := "boot-tool-" + newTestSuffix()
	agentID := "agent-tool-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	list := connect.NewRequest(&toolgatewayv1.ListToolsRequest{})
	list.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	name := "probe.get-" + newTestSuffix()
	listCap := signLiveTestCapability(t, ctx, st.Pool(), priv, agentID, []string{name})
	if _, err := gw.ListTools(ctx, list); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("ListTools without capability token want unauthenticated got %v", err)
	}
	list.Header().Set(CapabilityHeader, "Bearer "+listCap)
	empty, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range empty.Msg.Tools {
		if it.Name == name {
			t.Fatal("unpublished tool must be invisible")
		}
	}

	payload, err := protojson.Marshal(&toolv1.ToolDescriptor{
		Name: name, Description: "signed probe",
		InputSchema: []byte(`{}`), Version: "1.0.0",
		Binding: &toolv1.Binding{Target: &toolv1.Binding_Primitive{Primitive: "event.get"}},
		Effect:  toolv1.ToolEffect_TOOL_EFFECT_SAFE, Replay: toolv1.ToolReplay_TOOL_REPLAY_SAFE,
	})
	if err != nil {
		t.Fatal(err)
	}
	art := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_TOOL_DESCRIPTOR, Payload: payload, PayloadSchema: "tool/v1",
		CreatedAt: timestamppb.Now(), CreatedBy: "t",
	}
	if err := kernel.SignArtifact(art, priv); err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(art)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "rel-tool-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact_id, artifact, ttl_seconds) VALUES($1,'signed',$2,$3::jsonb,86400)`,
		releaseID, art.GetId(), string(raw)); err != nil {
		t.Fatal(err)
	}
	signedOnly, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range signedOnly.Msg.GetTools() {
		if item.GetName() == name {
			t.Fatal("signed but inactive tool must be invisible")
		}
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE releases SET state='shadow', shadow_started_at=now(), updated_at=now() WHERE release_id=$1`, releaseID); err != nil {
		t.Fatal(err)
	}
	listed, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	var found *toolgatewayv1.ToolDescriptorItem
	for _, it := range listed.Msg.Tools {
		if it.Name == name {
			found = it
		}
	}
	if found == nil {
		t.Fatalf("published tool missing: %+v", listed.Msg.Tools)
	}
	for _, it := range listed.Msg.Tools {
		if it.Name == name && (it.Version == "" || it.SchemaDigest == "" || it.Effect == toolv1.ToolEffect_TOOL_EFFECT_UNSPECIFIED || it.Replay == toolv1.ToolReplay_TOOL_REPLAY_UNSPECIFIED) {
			t.Fatalf("short descriptor is incomplete: %+v", it)
		}
	}
	turnID := bindCatalogTestTurn(t, ctx, st.Pool(), priv.Public().(ed25519.PublicKey), listCap, agentID)
	describe := connect.NewRequest(&toolgatewayv1.DescribeToolRequest{
		TurnId: turnID, ToolName: name, ToolVersion: found.GetVersion(), SchemaDigest: found.GetSchemaDigest(),
	})
	setCatalogTestHeaders(describe.Header(), reg.Msg.GetAccessToken(), listCap)
	described, err := gw.DescribeTool(ctx, describe)
	if err != nil {
		t.Fatal(err)
	}
	if described.Msg.GetArtifactId() != art.GetId() || string(described.Msg.GetTool().GetInputSchema()) != `{}` {
		t.Fatalf("described tool=%v", described.Msg)
	}
	var pinnedDigest string
	if err := st.Pool().QueryRow(ctx, `SELECT schema_digest FROM agent_turn_tool_schemas WHERE turn_id=$1 AND tool_name=$2`, turnID, name).Scan(&pinnedDigest); err != nil {
		t.Fatal(err)
	}
	if pinnedDigest != found.GetSchemaDigest() {
		t.Fatalf("pinned digest=%s want %s", pinnedDigest, found.GetSchemaDigest())
	}
	deniedCap := signLiveTestCapability(t, ctx, st.Pool(), priv, agentID, []string{"different.tool"})
	list.Header().Set(CapabilityHeader, "Bearer "+deniedCap)
	filtered, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Msg.Tools) != 0 {
		t.Fatalf("ListTools leaked descriptors outside capability Tools: %+v", filtered.Msg.Tools)
	}

	ghost := "ghost.exec-" + newTestSuffix()
	badPayload, err := protojson.Marshal(&toolv1.ToolDescriptor{
		Name: ghost, Description: "no impl",
		InputSchema: []byte(`{}`), Version: "1.0.0",
		Binding: &toolv1.Binding{Target: &toolv1.Binding_Primitive{Primitive: "not-a-real-impl"}},
		Effect:  toolv1.ToolEffect_TOOL_EFFECT_EFFECTFUL, Replay: toolv1.ToolReplay_TOOL_REPLAY_NEVER,
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_TOOL_DESCRIPTOR, Payload: badPayload, PayloadSchema: "tool/v1",
		CreatedAt: timestamppb.Now(), CreatedBy: "t",
	}
	if err := kernel.SignArtifact(bad, priv); err != nil {
		t.Fatal(err)
	}
	braw, err := protojson.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact_id, artifact, ttl_seconds) VALUES($1,'shadow',$2,$3::jsonb,86400)`,
		"rel-ghost-"+newTestSuffix(), bad.GetId(), string(braw)); err != nil {
		t.Fatal(err)
	}
	capTok := signLiveTestCapability(t, ctx, st.Pool(), priv, agentID, []string{ghost})
	inv := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: ghost, ArgsJson: `{}`})
	inv.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	inv.Header().Set(CapabilityHeader, "Bearer "+capTok)
	if _, err := gw.InvokeTool(ctx, inv); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unknown impl want failed_precondition got %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE releases SET state='retired', retire_reason='manual', retired_at=now(), updated_at=now() WHERE release_id=$1`, releaseID); err != nil {
		t.Fatal(err)
	}
	list.Header().Set(CapabilityHeader, "Bearer "+listCap)
	retired, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range retired.Msg.GetTools() {
		if item.GetName() == name {
			t.Fatal("retired tool must be invisible")
		}
	}
	if _, err := gw.DescribeTool(ctx, describe); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("revoked pinned tool want failed_precondition got %v", err)
	}
}

func signLiveTestCapability(t *testing.T, ctx context.Context, db dbTX, key ed25519.PrivateKey, agentID string, tools []string) string {
	return signLiveTestCapabilityWithBindings(t, ctx, db, key, agentID, tools, nil, 2)
}

func signLiveTestCapabilityWithBindings(t *testing.T, ctx context.Context, db dbTX, key ed25519.PrivateKey, agentID string, tools, bindings []string, maxCalls int64) string {
	t.Helper()
	now := time.Now()
	tokenID := "jti-live-" + newTestSuffix()
	budgetID := "budget-live-" + newTestSuffix()
	leaseID := "lease-live-" + newTestSuffix()
	expires := now.Add(time.Hour)
	token, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: agentID, AuthorizedParty: agentID, Audience: "tools",
		TokenID: tokenID, BudgetID: budgetID, LeaseEpoch: 1, Tools: tools, Bindings: bindings, MaxCalls: maxCalls,
		ExpiresAt: expires.Unix(), IssuedAt: now.Unix(), NotBefore: now.Add(-time.Second).Unix(),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerCapabilityToken(ctx, db, tokenID, budgetID, leaseID, 1, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_instructions(
		instruction_id, agent_id, kind, status, lease_id, lease_expires_at, capability_token, budget_id, lease_epoch)
		VALUES($1,$2,'TEST','leased',$3,$4,$5,$6,1)`,
		"ins-live-"+newTestSuffix(), agentID, leaseID, expires, token, budgetID); err != nil {
		t.Fatal(err)
	}
	return token
}

func publishTestToolDescriptors(t *testing.T, ctx context.Context, db dbTX, key ed25519.PrivateKey, names ...string) {
	t.Helper()
	registry := firstPartyToolRegistry()
	for _, name := range names {
		implementation, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool implementation %s is not registered", name)
		}
		payload, err := protojson.Marshal(&toolv1.ToolDescriptor{
			Name: name, Description: "test descriptor for " + name, InputSchema: []byte(`{}`), Version: "1.0.0",
			Binding: &toolv1.Binding{Target: &toolv1.Binding_Primitive{Primitive: name}},
			Effect:  implementation.Effect, Replay: implementation.Replay,
		})
		if err != nil {
			t.Fatal(err)
		}
		artifact := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_TOOL_DESCRIPTOR, Payload: payload, PayloadSchema: "tool/v1", CreatedBy: "catalog-test"}
		if err := kernel.SignArtifact(artifact, key); err != nil {
			t.Fatal(err)
		}
		raw, err := protojson.Marshal(artifact)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO releases(release_id, state, artifact_id, artifact, ttl_seconds, created_by, shadow_started_at)
			VALUES($1,'shadow',$2,$3::jsonb,86400,'catalog-test',now())`, "rel-tool-test-"+newTestSuffix(), artifact.GetId(), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}
