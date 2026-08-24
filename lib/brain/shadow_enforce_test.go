package brain

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestSingleUnitShadowToEnforce(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	propID, offTok, assetID := seedPromotePair(t, ctx, st.Pool(), "g0e")
	unitID := "unit-one-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash) VALUES($1,'edge',$2)`, unitID, "h-"+unitID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,$2)`, unitID, assetID); err != nil {
		t.Fatal(err)
	}
	relID := insertShadowRelease(t, ctx, st.Pool(), assetID, propID)
	gov := NewGovernServer(st.Pool(), mustKey(t), 0, 0, 0, 0)
	canary := connect.NewRequest(&governv1.PromoteCanaryRequest{ReleaseId: relID, CanaryPercent: 5})
	canary.Header().Set("Authorization", "Bearer "+offTok)
	canary.Header().Set("Idempotency-Key", "canary-"+newTestSuffix())
	if _, err := gov.PromoteCanary(ctx, canary); !isReason(err, connect.CodeFailedPrecondition, "canary_cohort_too_small") {
		t.Fatalf("single-unit PromoteCanary want canary_cohort_too_small got %v", err)
	}
	var state string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1`, relID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "shadow" {
		t.Fatalf("canary reject must leave shadow, state=%s", state)
	}
	enf := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
	enf.Header().Set("Authorization", "Bearer "+offTok)
	enf.Header().Set("Idempotency-Key", "enforce-"+newTestSuffix())
	got, err := gov.PromoteEnforce(ctx, enf)
	if err != nil {
		t.Fatalf("single-unit shadow PromoteEnforce: %v", err)
	}
	if got.Msg.Release.GetState() != commonv1.ReleaseState_RELEASE_STATE_ENFORCE {
		t.Fatalf("state=%s", got.Msg.Release.GetState())
	}
}

func TestMultiUnitMustCanary(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	propID, offTok, assetID := seedPromotePair(t, ctx, st.Pool(), "g0em")
	attachNUnits(t, ctx, st.Pool(), assetID, kernel.CanaryMinUnits(25))
	relID := insertShadowRelease(t, ctx, st.Pool(), assetID, propID)
	gov := NewGovernServer(st.Pool(), mustKey(t), 0, 0, 0, 0)
	enf := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
	enf.Header().Set("Authorization", "Bearer "+offTok)
	enf.Header().Set("Idempotency-Key", "enforce-"+newTestSuffix())
	if _, err := gov.PromoteEnforce(ctx, enf); !isReason(err, connect.CodeFailedPrecondition, "release_state_conflict") {
		t.Fatalf("multi-unit shadow PromoteEnforce want release_state_conflict got %v", err)
	}
	canary := connect.NewRequest(&governv1.PromoteCanaryRequest{ReleaseId: relID, CanaryPercent: 25})
	canary.Header().Set("Authorization", "Bearer "+offTok)
	canary.Header().Set("Idempotency-Key", "canary-"+newTestSuffix())
	got, err := gov.PromoteCanary(ctx, canary)
	if err != nil {
		t.Fatalf("multi-unit PromoteCanary: %v", err)
	}
	if got.Msg.Release.GetState() != commonv1.ReleaseState_RELEASE_STATE_CANARY {
		t.Fatalf("state=%s", got.Msg.Release.GetState())
	}
}

func seedPromotePair(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tag string) (propID, offTok, assetID string) {
	t.Helper()
	admin := tag + "-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, pool, admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(pool, time.Hour, false, 8)
	users := NewUserServer(pool, 8)
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	assetID = "asset-" + tag + "-" + newTestSuffix()
	if _, err := pool.Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) (id, tok string) {
		t.Helper()
		req := connect.NewRequest(&userv1.CreateUserRequest{Username: name, Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR})
		req.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
		setTestIdempotency(req)
		u, err := users.CreateUser(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		lr, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: name, Password: "Operator123"}))
		if err != nil {
			t.Fatal(err)
		}
		return u.Msg.User.UserId, lr.Msg.Token
	}
	var offID string
	propID, _ = mk(tag + "-prop-" + newTestSuffix())
	offID, offTok = mk(tag + "-off-" + newTestSuffix())
	if err := insertUserGrant(ctx, pool, propID, []string{"govern.propose"}, assetID); err != nil {
		t.Fatal(err)
	}
	if err := insertUserGrant(ctx, pool, offID, []string{"govern.promote_canary", "govern.promote_enforce"}, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, pool, OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	return propID, offTok, assetID
}

func insertShadowRelease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, assetID, createdBy string) string {
	t.Helper()
	art, err := protojson.Marshal(&artifactv1.Artifact{
		Kind:          artifactv1.Kind_KIND_POLICY,
		PayloadSchema: "policy/v1",
		Scope:         &artifactv1.Scope{AssetIds: []string{assetID}},
		ReplayReport: &artifactv1.ReplayReport{
			Passed: true, MaliciousTotal: 1, MaliciousBlocked: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	relID := "rel-g0e-" + newTestSuffix()
	if _, err := pool.Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by, shadow_started_at)
		VALUES($1,'shadow',$2::jsonb,86400,$3,now())`, relID, string(art), createdBy); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, assetID); err != nil {
		t.Fatal(err)
	}
	return relID
}

func attachNUnits(t *testing.T, ctx context.Context, pool *pgxpool.Pool, assetID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		uid := fmt.Sprintf("unit-%s-%d", newTestSuffix(), i)
		if _, err := pool.Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash) VALUES($1,'edge',$2)`, uid, "h-"+uid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,$2)`, uid, assetID); err != nil {
			t.Fatal(err)
		}
	}
}
