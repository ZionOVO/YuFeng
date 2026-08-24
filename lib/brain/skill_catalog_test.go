package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	agentskills "yufeng/agents/skills"
	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
	artifactv1 "yufeng/proto/gen/artifactv1"
	modelv1 "yufeng/proto/gen/modelv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	toolv1 "yufeng/proto/gen/toolv1"
)

func TestSkillCatalogActivationPinningAndCapabilityIntersection(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw := NewToolGatewayServer(st.Pool(), key)
	agentID, access := registerCatalogTestAgent(t, ctx, st.Pool(), key)
	tools := []string{"skill.list", "skill.load", "event.get", "event.list", "govern.propose"}
	capability := signLiveTestCapabilityWithBindings(t, ctx, st.Pool(), key, agentID, tools, []string{"skill:investigate"}, 8)
	turnID := bindCatalogTestTurn(t, ctx, st.Pool(), key.Public().(ed25519.PublicKey), capability, agentID)

	marker := filepath.Join(t.TempDir(), "must-not-exist")
	body := []byte("#!/bin/sh\ntouch " + marker)
	v1 := signedSkillArtifact(t, key, "investigate", "1.0.0", body, []string{"event.list"}, []string{"event.get"})
	releaseV1 := publishCatalogTestArtifact(t, ctx, st.Pool(), v1, "signed")

	list := connect.NewRequest(&toolgatewayv1.ListSkillsRequest{})
	setCatalogTestHeaders(list.Header(), access, capability)
	signedOnly, err := gw.ListSkills(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(signedOnly.Msg.GetSkills()) != 0 {
		t.Fatalf("signed but inactive skills=%v", signedOnly.Msg.GetSkills())
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE releases SET state='shadow', shadow_started_at=now(), updated_at=now() WHERE release_id=$1`, releaseV1); err != nil {
		t.Fatal(err)
	}
	visible, err := gw.ListSkills(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible.Msg.GetSkills()) != 1 || visible.Msg.GetSkills()[0].GetVersion() != "1.0.0" {
		t.Fatalf("visible skills=%v", visible.Msg.GetSkills())
	}

	load := connect.NewRequest(&toolgatewayv1.LoadSkillRequest{
		TurnId: turnID, SkillId: "investigate", Version: "1.0.0", ContentDigest: agentskills.ContentAddress(body),
	})
	setCatalogTestHeaders(load.Header(), access, capability)
	loaded, err := gw.LoadSkill(ctx, load)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Msg.GetManifest().GetContent()) != string(body) {
		t.Fatal("skill body was not loaded by content address")
	}
	if got := loaded.Msg.GetEffectiveTools(); len(got) != 2 || got[0] != "event.get" || got[1] != "event.list" {
		t.Fatalf("effective tools=%v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("skill body was executed: %v", err)
	}
	if _, err := invokeDual(ctx, gw, access, capability, "govern.propose", `{}`); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("tool outside loaded skill subset want permission_denied got %v", err)
	}
	ref := agentskills.Reference("investigate", "1.0.0", agentskills.ContentAddress(body))
	if err := gw.validateCatalogPins(ctx, st.Pool(), turnID, &modelv1.ContextManifest{SkillRefs: []string{ref}}); err != nil {
		t.Fatal(err)
	}

	v2 := signedSkillArtifact(t, key, "investigate", "2.0.0", []byte("new instructions"), nil, []string{"event.get"})
	publishCatalogTestArtifact(t, ctx, st.Pool(), v2, "shadow")
	visible, err = gw.ListSkills(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible.Msg.GetSkills()) != 1 || visible.Msg.GetSkills()[0].GetVersion() != "2.0.0" {
		t.Fatalf("latest visible skill=%v", visible.Msg.GetSkills())
	}
	loaded, err = gw.LoadSkill(ctx, load)
	if err != nil || loaded.Msg.GetManifest().GetVersion() != "1.0.0" {
		t.Fatalf("open turn changed version: response=%v err=%v", loaded, err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE releases SET state='retired', retire_reason='superseded', retired_at=now(), updated_at=now() WHERE release_id=$1`, releaseV1); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.LoadSkill(ctx, load); err != nil {
		t.Fatalf("superseded version must remain pinned to open turn: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE releases SET retire_reason='manual', updated_at=now() WHERE release_id=$1`, releaseV1); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.LoadSkill(ctx, load); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("revoked skill want failed_precondition got %v", err)
	}
	if err := gw.validateCatalogPins(ctx, st.Pool(), turnID, &modelv1.ContextManifest{SkillRefs: []string{ref}}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("checkpoint after revocation want failed_precondition got %v", err)
	}

	limitedCapability := signLiveTestCapabilityWithBindings(t, ctx, st.Pool(), key, agentID, []string{"skill.list", "skill.load"}, []string{"skill:investigate"}, 4)
	limitedTurn := bindCatalogTestTurn(t, ctx, st.Pool(), key.Public().(ed25519.PublicKey), limitedCapability, agentID)
	limited := connect.NewRequest(&toolgatewayv1.LoadSkillRequest{
		TurnId: limitedTurn, SkillId: "investigate", Version: "2.0.0", ContentDigest: v2SkillDigest(t, v2),
	})
	setCatalogTestHeaders(limited.Header(), access, limitedCapability)
	if _, err := gw.LoadSkill(ctx, limited); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("missing required tool want failed_precondition got %v", err)
	}
}

func registerCatalogTestAgent(t *testing.T, ctx context.Context, db *pgxpool.Pool, key ed25519.PrivateKey) (string, string) {
	t.Helper()
	agentID := "agent-catalog-" + newTestSuffix()
	bootstrap := "boot-catalog-" + newTestSuffix()
	server := NewAgentServer(db, bootstrap, key)
	registered, err := server.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: bootstrap, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	return agentID, registered.Msg.GetAccessToken()
}

func bindCatalogTestTurn(t *testing.T, ctx context.Context, db dbTX, publicKey ed25519.PublicKey, capability, agentID string) string {
	t.Helper()
	claims, err := kernel.VerifyCapabilityToken(capability, publicKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "thread-catalog-" + newTestSuffix()
	turnID := "turn-catalog-" + newTestSuffix()
	if _, err := db.Exec(ctx, `INSERT INTO agent_threads(thread_id, source_kind, source_ref, agent_id) VALUES($1,'session',$2,$3)`, threadID, "source-"+newTestSuffix(), agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_turns(turn_id, thread_id, source_version, input_snapshot, budget_id) VALUES($1,$2,1,'{}',$3)`, turnID, threadID, claims.BudgetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE agent_instructions SET turn_id=$1 WHERE budget_id=$2`, turnID, claims.BudgetID); err != nil {
		t.Fatal(err)
	}
	return turnID
}

func signedSkillArtifact(t *testing.T, key ed25519.PrivateKey, skillID, version string, body []byte, suggested, required []string) *artifactv1.Artifact {
	t.Helper()
	digest := agentskills.ContentAddress(body)
	manifest := &toolv1.SkillManifest{
		SkillId: skillID, Version: version, Name: "Investigation", Description: "Read frozen evidence",
		ContentRef: digest, ContentDigest: digest, Content: body, SuggestedTools: suggested, RequiredTools: required,
		MinRuntimeVersion: "1.27.0", ModelCapabilities: []string{"tool_calling"}, MaxContextBytes: 65536,
	}
	payload, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_SKILL, Payload: payload, PayloadSchema: "skill/v1", CreatedBy: "test"}
	if err := kernel.SignArtifact(artifact, key); err != nil {
		t.Fatal(err)
	}
	manifest.PublisherKeyId = artifact.GetSignature().GetKeyId()
	payload, err = protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Payload, artifact.Id, artifact.Signature = payload, "", nil
	if err := kernel.SignArtifact(artifact, key); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func publishCatalogTestArtifact(t *testing.T, ctx context.Context, db dbTX, artifact *artifactv1.Artifact, state string) string {
	t.Helper()
	raw, err := protojson.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "rel-catalog-" + newTestSuffix()
	if _, err := db.Exec(ctx, `INSERT INTO releases(release_id, state, artifact_id, artifact, ttl_seconds) VALUES($1,$2,$3,$4::jsonb,86400)`, releaseID, state, artifact.GetId(), string(raw)); err != nil {
		t.Fatal(err)
	}
	return releaseID
}

func v2SkillDigest(t *testing.T, artifact *artifactv1.Artifact) string {
	t.Helper()
	manifest, err := agentskills.Validate(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return manifest.GetContentDigest()
}

type catalogHeader interface {
	Set(string, string)
}

func setCatalogTestHeaders(header catalogHeader, access, capability string) {
	header.Set("Authorization", "Bearer "+access)
	header.Set(CapabilityHeader, "Bearer "+capability)
}
