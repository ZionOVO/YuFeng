package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	"yufeng/lib/store"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	eventv1 "yufeng/proto/gen/eventv1"
	sessionv1 "yufeng/proto/gen/sessionv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestTriageEnqueuePredicates(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agents := NewAgentServer(st.Pool(), "boot", priv)
	tel := NewTelemetryServer(st.Pool(), nil, agents, "jarvis-f2")
	tel.demoTriage = true
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES('jarvis-f2','x','orchestrator','test-pub') ON CONFLICT(agent_id) DO UPDATE SET public_key='test-pub'`); err != nil {
		t.Fatal(err)
	}

	unitID, assetID, token := seedUnitAsset(t, ctx, st, "f2")
	occurredAtByEventID := make(map[string]*timestamppb.Timestamp)
	upload := func(id, verdict, method, path string) *telemetryv1.UploadEventsResponse {
		t.Helper()
		occurredAt := occurredAtByEventID[id]
		if occurredAt == nil {
			occurredAt = timestamppb.Now()
			occurredAtByEventID[id] = occurredAt
		}
		ev := &eventv1.Event{
			Id: id, AssetId: assetID, UnitId: unitID, OccurredAt: occurredAt,
			Source: "test", Kind: eventv1.Kind_KIND_TRAFFIC,
			Verdict: eventVerdictEnum(verdict),
		}
		if method != "" {
			ev.Traffic = &eventv1.Event_Http{Http: &eventv1.Http{Method: method, Path: path, QueryRedacted: "id=1+UNION+SELECT+password"}}
		}
		req := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{ev}})
		req.Header().Set("Authorization", "Bearer "+token)
		resp, err := tel.UploadEvents(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	var baseline int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE kind=$1 AND agent_id='jarvis-f2'`, instructionTriage).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	countTriage := func() int {
		t.Helper()
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE kind=$1 AND agent_id='jarvis-f2'`, instructionTriage).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n - baseline
	}

	missID := "evt-f2-miss-" + newTestSuffix()
	miss := upload(missID, "allow", "GET", "/api/items")
	if miss.Accepted != 1 {
		t.Fatalf("漏拦应 accepted=1，实际 %+v", miss)
	}
	if countTriage() != 1 {
		t.Fatalf("漏拦应入队 1 条，实际 %d", countTriage())
	}
	claims := lastTriageClaims(t, ctx, st, priv.Public().(ed25519.PublicKey))
	if claims.Bindings[0] != assetBinding(assetID) {
		t.Fatalf("Bindings 应为 %s，实际 %v", assetBinding(assetID), claims.Bindings)
	}
	if !claimsAllows(claims.Tools, "govern.propose") || !claimsAllows(claims.Tools, "govern.gate") || !claimsAllows(claims.Tools, "govern.start_shadow") {
		t.Fatalf("Tools 缺治理提案工具: %v", claims.Tools)
	}
	if claimsAllows(claims.Tools, "govern.promote_enforce") {
		t.Fatal("Tools 不得含 promote")
	}

	dup := upload(missID, "allow", "GET", "/api/items")
	if dup.Deduped != 1 || countTriage() != 1 {
		t.Fatalf("重复 event_id 不得再入队: resp=%+v n=%d", dup, countTriage())
	}

	samePath := upload("evt-f2-samepath-"+newTestSuffix(), "allow", "GET", "/api/items")
	if samePath.Accepted != 1 || countTriage() != 1 {
		t.Fatalf("同路径 pending 不得再入队: resp=%+v n=%d", samePath, countTriage())
	}

	if upload("evt-f2-observe-"+newTestSuffix(), "observe", "GET", "/other").Accepted != 1 || countTriage() != 1 {
		t.Fatal("observe 不得入队")
	}
	if upload("evt-f2-block-"+newTestSuffix(), "block", "GET", "/other").Accepted != 1 || countTriage() != 1 {
		t.Fatal("block 不得入队")
	}

	seedOpenRule(t, ctx, st, assetID, "/locked")
	if upload("evt-f2-locked-"+newTestSuffix(), "allow", "GET", "/locked/x").Accepted != 1 || countTriage() != 1 {
		t.Fatal("已有未退休规则的路径不得入队")
	}
}

func eventVerdictEnum(s string) eventv1.Verdict {
	switch s {
	case "block":
		return eventv1.Verdict_VERDICT_BLOCK
	case "observe":
		return eventv1.Verdict_VERDICT_OBSERVE
	default:
		return eventv1.Verdict_VERDICT_ALLOW
	}
}

func lastTriageClaims(t *testing.T, ctx context.Context, st *store.Store, pub ed25519.PublicKey) kernel.Claims {
	t.Helper()
	var token string
	if err := st.Pool().QueryRow(ctx, `SELECT capability_token FROM agent_instructions WHERE kind=$1 AND agent_id='jarvis-f2' ORDER BY created_at DESC LIMIT 1`, instructionTriage).Scan(&token); err != nil {
		t.Fatal(err)
	}
	claims, err := kernel.VerifyCapabilityToken(token, pub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func seedUnitAsset(t *testing.T, ctx context.Context, st *store.Store, tag string) (unitID, assetID, rawToken string) {
	t.Helper()
	unitID = "unit-" + tag + "-" + newTestSuffix()
	assetID = "asset-" + tag + "-" + newTestSuffix()
	raw, hash, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash, producer_capabilities) VALUES($1,'edge',$2,$3::jsonb)`, unitID, hash, testEdgeCapabilitiesJSON(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,$2)`, unitID, assetID); err != nil {
		t.Fatal(err)
	}
	seedTaxonomyGeneration(t, ctx, st.Pool(), assetID)
	return unitID, assetID, raw
}

func seedTaxonomyGeneration(t *testing.T, ctx context.Context, pool *pgxpool.Pool, assetID string) {
	t.Helper()
	payload, err := protojson.Marshal(edgecore.DefaultTaxonomyMapper())
	if err != nil {
		t.Fatal(err)
	}
	art := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_TAXONOMY_MAPPER, Payload: payload, PayloadSchema: edgecore.TaxonomyMapperSchema}
	relID := "rel-tax-" + newTestSuffix()
	genID := "gen-tax-" + newTestSuffix()
	item := &artifactv1.ReleaseItem{ReleaseId: relID, Artifact: art, AssetId: assetID}
	gen := &artifactv1.AssetGeneration{GenerationId: genID, AssetId: assetID, GenerationSeq: 1, Members: []*artifactv1.ReleaseItem{item}}
	env, err := protojson.Marshal(gen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_generations(generation_id, asset_id, generation_seq, envelope, signed) VALUES($1,$2,1,$3::jsonb,true)`, genID, assetID, string(env)); err != nil {
		t.Fatal(err)
	}
}

func seedOpenRule(t *testing.T, ctx context.Context, st *store.Store, assetID, selector string) {
	t.Helper()
	art := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Scope: &artifactv1.Scope{AssetIds: []string{assetID}, RouteSelector: selector}}
	raw, err := protoJSONArtifact(art)
	if err != nil {
		t.Fatal(err)
	}
	rel := "rel-open-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds) VALUES($1,'shadow',$2::jsonb,86400)`, rel, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, rel, assetID); err != nil {
		t.Fatal(err)
	}
}

func protoJSONArtifact(a *artifactv1.Artifact) (string, error) {
	return protoJSON(a)
}

func newTestSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func TestSendMessageOnlySessionInstruction(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), "f2session", "Admin12345"); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agents := NewAgentServer(st.Pool(), "boot", priv)
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES('jarvis-f2s','x','orchestrator','test-pub') ON CONFLICT(agent_id) DO UPDATE SET public_key='test-pub'`); err != nil {
		t.Fatal(err)
	}
	sess := NewSessionServer(st.Pool(), agents, "jarvis-f2s")
	auth := NewAuthServer(st.Pool(), time.Hour, false, MinPasswordLength)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: "f2session", Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	bearer := "Bearer " + login.Msg.Token
	cr := connect.NewRequest(&sessionv1.CreateSessionRequest{Title: "f2-session"})
	cr.Header().Set("Authorization", bearer)
	created, err := sess.CreateSession(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	sm := connect.NewRequest(&sessionv1.SendMessageRequest{SessionId: created.Msg.SessionId, Content: "拦住这个 UNION SELECT"})
	sm.Header().Set("Authorization", bearer)
	if _, err := sess.SendMessage(ctx, sm); err != nil {
		t.Fatal(err)
	}
	var kind, token, turnID, sourceRef string
	if err := st.Pool().QueryRow(ctx, `SELECT i.kind, i.capability_token, i.turn_id, th.source_ref
		FROM agent_instructions i JOIN agent_turns t ON t.turn_id=i.turn_id
		JOIN agent_threads th ON th.thread_id=t.thread_id WHERE th.source_kind=$1 AND th.source_ref=$2`,
		threadSourceSession, created.Msg.SessionId).Scan(&kind, &token, &turnID, &sourceRef); err != nil {
		t.Fatal(err)
	}
	if kind != instructionSession {
		t.Fatalf("聊天只能入队 SESSION_MESSAGE，实际 %s", kind)
	}
	if turnID == "" || sourceRef != created.Msg.SessionId {
		t.Fatalf("session turn=%q source=%q", turnID, sourceRef)
	}
	claims, err := kernel.VerifyCapabilityToken(token, priv.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range claims.Tools {
		if strings.HasPrefix(tool, "govern.") {
			t.Fatalf("会话令牌不得含 govern.*: %v", claims.Tools)
		}
	}
	if !claimsAllows(claims.Tools, "model.generate") {
		t.Fatalf("会话令牌缺 model.generate: %v", claims.Tools)
	}
}
