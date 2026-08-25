package brain

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	authv1 "yufeng/proto/gen/authv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

func TestTrafficReviewPolicyPublishesSignedGenerationOneLevelAtATime(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	username := "review-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	login, err := NewAuthServer(st.Pool(), time.Hour, false, 8).Login(ctx,
		connect.NewRequest(&authv1.LoginRequest{Username: username, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	assetID := "review-asset-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id,display_name,max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.GetUser().GetUserId(), assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	server := NewAssetServer(st.Pool())
	server.signingKey = privateKey
	update := func(mode artifactv1.TrafficReviewMode, expected string) (*assetv1.TrafficReviewPolicyStatus, error) {
		req := connect.NewRequest(&assetv1.UpdateTrafficReviewPolicyRequest{AssetId: assetID, Mode: mode, ExpectedGenerationId: expected})
		req.Header().Set("Authorization", "Bearer "+login.Msg.Token)
		setTestIdempotency(req)
		resp, err := server.UpdateTrafficReviewPolicy(ctx, req)
		if err != nil {
			return nil, err
		}
		return resp.Msg.Status, nil
	}
	if _, err := update(artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES, ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("skipped mode must fail: %v", err)
	}
	statistics, err := update(artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY, "")
	if err != nil {
		t.Fatal(err)
	}
	if statistics.GetGenerationId() == "" || statistics.GetGenerationSeq() != 1 || statistics.GetPolicy().GetMode() != artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY {
		t.Fatalf("statistics status=%v", statistics)
	}
	if _, err := update(artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES, "stale-generation"); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "generation_mismatch") {
		t.Fatalf("stale generation must fail: %v", err)
	}
	redacted, err := update(artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES, statistics.GetGenerationId())
	if err != nil {
		t.Fatal(err)
	}
	if redacted.GetGenerationSeq() != 2 || redacted.GetPolicy().GetMode() != artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES {
		t.Fatalf("redacted status=%v", redacted)
	}
}

func TestTrafficReviewPolicyStatusRequiresEveryBoundEdgeCapability(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	server := NewAssetServer(st.Pool())
	getStatus := func() *assetv1.TrafficReviewPolicyStatus {
		t.Helper()
		response, err := server.GetTrafficReviewPolicy(ctx, bearerReq(h.adminTok, &assetv1.GetTrafficReviewPolicyRequest{
			AssetId: h.local,
		}))
		if err != nil {
			t.Fatal(err)
		}
		return response.Msg.GetStatus()
	}
	if getStatus().GetEdgeSupported() {
		t.Fatal("asset without a bound edge must not be traffic-review compatible")
	}

	supportedRaw, err := protojson.Marshal(&unitv1.ProducerCapabilities{
		ModuleCapabilities: []string{"traffic-review-candidate/v1", "traffic-window/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	unsupportedRaw, err := protojson.Marshal(&unitv1.ProducerCapabilities{
		ModuleCapabilities: []string{"traffic-window/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	supportedEdge := "traffic-review-edge-supported-" + newTestSuffix()
	unsupportedEdge := "traffic-review-edge-unsupported-" + newTestSuffix()
	boundHost := "traffic-review-host-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id,kind,producer_capabilities,last_heartbeat_at)
		VALUES($1,'edge',$3::jsonb,now()),($2,'host',$4::jsonb,NULL)`,
		supportedEdge, boundHost, supportedRaw, unsupportedRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id,asset_id) VALUES($1,$3),($2,$3)`,
		supportedEdge, boundHost, h.local); err != nil {
		t.Fatal(err)
	}
	if !getStatus().GetEdgeSupported() {
		t.Fatal("fresh capable edge must make the asset traffic-review compatible; bound hosts do not participate")
	}

	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id,kind,producer_capabilities,last_heartbeat_at)
		VALUES($1,'edge',$2::jsonb,now())`, unsupportedEdge, unsupportedRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id,asset_id) VALUES($1,$2)`,
		unsupportedEdge, h.local); err != nil {
		t.Fatal(err)
	}
	if getStatus().GetEdgeSupported() {
		t.Fatal("one capable edge must not hide another bound edge that lacks a required capability")
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET producer_capabilities=$2::jsonb WHERE unit_id=$1`,
		unsupportedEdge, supportedRaw); err != nil {
		t.Fatal(err)
	}
	if !getStatus().GetEdgeSupported() {
		t.Fatal("all fresh bound edges now advertise the required capabilities")
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET last_heartbeat_at=now()-interval '3 minutes' WHERE unit_id=$1`,
		unsupportedEdge); err != nil {
		t.Fatal(err)
	}
	if getStatus().GetEdgeSupported() {
		t.Fatal("a stale bound edge must fail the whole-asset compatibility projection")
	}
}
