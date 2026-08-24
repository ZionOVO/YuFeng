package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	agentskills "yufeng/agents/skills"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	toolv1 "yufeng/proto/gen/toolv1"
)

func TestCatalogGovernLifecyclePublishesAndRevokesTool(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	adminToken := catalogAdminToken(t, ctx, st.Pool(), "catalog.manage")
	govern := NewGovernServer(st.Pool(), key, 0, 0, 0, 0)

	unknownPayload, err := protojson.Marshal(&toolv1.ToolDescriptor{
		Name: "unknown.exec", Version: "1.0.0", InputSchema: []byte(`{}`),
		Binding: &toolv1.Binding{Target: &toolv1.Binding_Primitive{Primitive: "payload.supplied"}},
		Effect:  toolv1.ToolEffect_TOOL_EFFECT_EFFECTFUL, Replay: toolv1.ToolReplay_TOOL_REPLAY_NEVER,
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := connect.NewRequest(&governv1.ProposeCatalogArtifactRequest{
		Kind: artifactv1.Kind_KIND_TOOL_DESCRIPTOR, PayloadSchema: "tool/v1", Payload: unknownPayload, Ttl: durationpb.New(time.Hour),
	})
	setUserWriteHeaders(bad.Header(), adminToken, "catalog-unknown-"+newTestSuffix())
	if _, err := govern.ProposeCatalogArtifact(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown implementation want invalid_argument got %v", err)
	}

	implementation, _ := firstPartyToolRegistry().Lookup("event.get")
	payload, err := protojson.Marshal(&toolv1.ToolDescriptor{
		Name: "catalog.event.get", Description: "read event", Version: "1.0.0", InputSchema: []byte(`{"type":"object"}`),
		Binding: &toolv1.Binding{Target: &toolv1.Binding_Primitive{Primitive: "event.get"}},
		Effect:  implementation.Effect, Replay: implementation.Replay,
	})
	if err != nil {
		t.Fatal(err)
	}
	propose := connect.NewRequest(&governv1.ProposeCatalogArtifactRequest{
		Kind: artifactv1.Kind_KIND_TOOL_DESCRIPTOR, PayloadSchema: "tool/v1", Payload: payload, Ttl: durationpb.New(24 * time.Hour),
	})
	setUserWriteHeaders(propose.Header(), adminToken, "catalog-propose-"+newTestSuffix())
	proposed, err := govern.ProposeCatalogArtifact(ctx, propose)
	if err != nil {
		t.Fatal(err)
	}
	if proposed.Msg.GetState() != commonv1.ReleaseState_RELEASE_STATE_DRAFT {
		t.Fatalf("proposed state=%s", proposed.Msg.GetState())
	}

	sign := connect.NewRequest(&governv1.SignCatalogArtifactRequest{ReleaseId: proposed.Msg.GetReleaseId()})
	setUserWriteHeaders(sign.Header(), adminToken, "catalog-sign-"+newTestSuffix())
	signed, err := govern.SignCatalogArtifact(ctx, sign)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Msg.GetRelease().GetState() != commonv1.ReleaseState_RELEASE_STATE_SIGNED || signed.Msg.GetRelease().GetArtifact().GetSignature() == nil {
		t.Fatalf("signed release=%v", signed.Msg.GetRelease())
	}

	gw := NewToolGatewayServer(st.Pool(), key)
	agentID, access := registerCatalogTestAgent(t, ctx, st.Pool(), key)
	capability := signLiveTestCapability(t, ctx, st.Pool(), key, agentID, []string{"catalog.event.get"})
	list := connect.NewRequest(&toolgatewayv1.ListToolsRequest{})
	setCatalogTestHeaders(list.Header(), access, capability)
	before, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Msg.GetTools()) != 0 {
		t.Fatalf("signed catalog must be invisible: %v", before.Msg.GetTools())
	}

	activate := connect.NewRequest(&governv1.ActivateCatalogArtifactRequest{ReleaseId: proposed.Msg.GetReleaseId()})
	setUserWriteHeaders(activate.Header(), adminToken, "catalog-activate-"+newTestSuffix())
	activated, err := govern.ActivateCatalogArtifact(ctx, activate)
	if err != nil {
		t.Fatal(err)
	}
	if activated.Msg.GetRelease().GetState() != commonv1.ReleaseState_RELEASE_STATE_SHADOW {
		t.Fatalf("activated state=%s", activated.Msg.GetRelease().GetState())
	}
	visible, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible.Msg.GetTools()) != 1 || visible.Msg.GetTools()[0].GetName() != "catalog.event.get" {
		t.Fatalf("visible catalog=%v", visible.Msg.GetTools())
	}

	revoke := connect.NewRequest(&governv1.RevokeCatalogArtifactRequest{ReleaseId: proposed.Msg.GetReleaseId()})
	setUserWriteHeaders(revoke.Header(), adminToken, "catalog-revoke-"+newTestSuffix())
	revoked, err := govern.RevokeCatalogArtifact(ctx, revoke)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Msg.GetRelease().GetState() != commonv1.ReleaseState_RELEASE_STATE_RETIRED {
		t.Fatalf("revoked state=%s", revoked.Msg.GetRelease().GetState())
	}
	after, err := gw.ListTools(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Msg.GetTools()) != 0 {
		t.Fatalf("revoked catalog visible=%v", after.Msg.GetTools())
	}
}

func TestCatalogGovernPublishesLoadableSkillWithServerPublisher(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	adminToken := catalogAdminToken(t, ctx, st.Pool(), "catalog.manage")
	govern := NewGovernServer(st.Pool(), key, 0, 0, 0, 0)
	body := []byte("inspect the frozen event and cite evidence")
	digest := agentskills.ContentAddress(body)
	payload, err := protojson.Marshal(&toolv1.SkillManifest{
		SkillId: "catalog-investigate", Version: "1.0.0", Name: "Catalog Investigation", Description: "Read frozen evidence",
		ContentRef: digest, ContentDigest: digest, Content: body, RequiredTools: []string{"event.get"},
		MinRuntimeVersion: "1.27.0", MaxContextBytes: 4096, PublisherKeyId: "client-forged",
	})
	if err != nil {
		t.Fatal(err)
	}
	propose := connect.NewRequest(&governv1.ProposeCatalogArtifactRequest{
		Kind: artifactv1.Kind_KIND_SKILL, PayloadSchema: "skill/v1", Payload: payload, Ttl: durationpb.New(24 * time.Hour),
	})
	setUserWriteHeaders(propose.Header(), adminToken, "skill-propose-"+newTestSuffix())
	proposed, err := govern.ProposeCatalogArtifact(ctx, propose)
	if err != nil {
		t.Fatal(err)
	}
	var proposedManifest toolv1.SkillManifest
	if err := protojson.Unmarshal(proposed.Msg.GetArtifact().GetPayload(), &proposedManifest); err != nil {
		t.Fatal(err)
	}
	if proposedManifest.GetPublisherKeyId() != kernel.KeyID(key.Public().(ed25519.PublicKey)) {
		t.Fatalf("publisher key=%s", proposedManifest.GetPublisherKeyId())
	}
	sign := connect.NewRequest(&governv1.SignCatalogArtifactRequest{ReleaseId: proposed.Msg.GetReleaseId()})
	setUserWriteHeaders(sign.Header(), adminToken, "skill-sign-"+newTestSuffix())
	signed, err := govern.SignCatalogArtifact(ctx, sign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentskills.Validate(signed.Msg.GetRelease().GetArtifact()); err != nil {
		t.Fatalf("signed skill invalid: %v", err)
	}
	activate := connect.NewRequest(&governv1.ActivateCatalogArtifactRequest{ReleaseId: proposed.Msg.GetReleaseId()})
	setUserWriteHeaders(activate.Header(), adminToken, "skill-activate-"+newTestSuffix())
	if _, err := govern.ActivateCatalogArtifact(ctx, activate); err != nil {
		t.Fatal(err)
	}

	gw := NewToolGatewayServer(st.Pool(), key)
	agentID, access := registerCatalogTestAgent(t, ctx, st.Pool(), key)
	capability := signLiveTestCapabilityWithBindings(t, ctx, st.Pool(), key, agentID,
		[]string{"skill.list", "skill.load", "event.get"}, []string{"skill:catalog-investigate"}, 4)
	turnID := bindCatalogTestTurn(t, ctx, st.Pool(), key.Public().(ed25519.PublicKey), capability, agentID)
	list := connect.NewRequest(&toolgatewayv1.ListSkillsRequest{})
	setCatalogTestHeaders(list.Header(), access, capability)
	listed, err := gw.ListSkills(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetSkills()) != 1 || listed.Msg.GetSkills()[0].GetSkillId() != "catalog-investigate" {
		t.Fatalf("listed skills=%v", listed.Msg.GetSkills())
	}
	load := connect.NewRequest(&toolgatewayv1.LoadSkillRequest{
		TurnId: turnID, SkillId: "catalog-investigate", Version: "1.0.0", ContentDigest: digest,
	})
	setCatalogTestHeaders(load.Header(), access, capability)
	loaded, err := gw.LoadSkill(ctx, load)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Msg.GetManifest().GetContent()) != string(body) {
		t.Fatal("loaded skill content changed")
	}
}

func catalogAdminToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tool string) string {
	t.Helper()
	username := "catalog-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, pool, username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	assetID := "catalog-control-" + newTestSuffix()
	if _, err := pool.Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, pool, OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `SELECT user_id FROM users WHERE username=$1`, username).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'user',$2,$3::jsonb,'[]'::jsonb,$2)`, "grant-catalog-"+newTestSuffix(), userID, `["`+tool+`"]`); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(pool, time.Hour, false, MinPasswordLength)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: username, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	return login.Msg.GetToken()
}

func setUserWriteHeaders(header catalogHeader, token, idempotencyKey string) {
	header.Set("Authorization", "Bearer "+token)
	header.Set("Idempotency-Key", idempotencyKey)
}
