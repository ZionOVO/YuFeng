package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestProposeAndPromoteRequireAllScopeAssets(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	adminName := "scope-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), adminName, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: adminName, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	opReq := connect.NewRequest(&userv1.CreateUserRequest{Username: "scope-op-" + newTestSuffix(), Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR})
	opReq.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	setTestIdempotency(opReq)
	op, err := users.CreateUser(ctx, opReq)
	if err != nil {
		t.Fatal(err)
	}
	opLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: op.Msg.User.Username, Password: "Operator123"}))
	if err != nil {
		t.Fatal(err)
	}
	assetA := "asset-a-" + newTestSuffix()
	assetB := "asset-b-" + newTestSuffix()
	for _, id := range []string{assetA, assetB} {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'user',$2,'["govern.propose","govern.gate","govern.start_shadow","govern.promote_enforce"]',$3::jsonb,'admin')`,
		"g-scope-"+newTestSuffix(), op.Msg.User.UserId, fmt.Sprintf(`[{"kind":"asset","id":%q}]`, assetA)); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gov := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	prop := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Intent: &governv1.ProposalIntent{
			Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
			DetectionKeys: []*commonv1.DetectionKey{{
				DetectorId: "crs", RuleId: "942100",
				TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			}},
		},
		Scope: &artifactv1.Scope{AssetIds: []string{assetA, assetB}},
	})
	prop.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	prop.Header().Set("Idempotency-Key", "scope-prop-"+newTestSuffix())
	if _, err := gov.ProposeArtifact(ctx, prop); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("partial scope propose want permission_denied got %v", err)
	}

	relID := "rel-scope-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by)
		VALUES($1,'shadow','{}',86400,'other')`, relID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2),($1,$3)`, relID, assetA, assetB); err != nil {
		t.Fatal(err)
	}
	enf := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
	enf.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	enf.Header().Set("Idempotency-Key", "scope-enf-"+newTestSuffix())
	if _, err := gov.PromoteEnforce(ctx, enf); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("partial scope promote want permission_denied got %v", err)
	}
}

func TestAccessScopeCoversReleaseRequiresAllAssets(t *testing.T) {
	s := accessScope{assets: map[string]bool{"A": true}, releases: map[string]bool{}}
	if s.coversRelease("r1", []string{"A", "B"}) {
		t.Fatal("intersection must not cover a multi-asset release")
	}
	if !s.coversRelease("r1", []string{"A"}) {
		t.Fatal("full coverage must pass")
	}
	s.releases["r2"] = true
	if !s.coversRelease("r2", []string{"A", "B"}) {
		t.Fatal("explicit release binding still covers")
	}
}
