package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestGovernWriteGrantAndIdempotency(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	adminName := "gov-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), adminName, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: adminName, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, role commonv1.UserRole) (id, tok string) {
		t.Helper()
		req := connect.NewRequest(&userv1.CreateUserRequest{Username: name, Password: "Operator123", Role: role})
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
	_, viewerTok := mk("gov-view-"+newTestSuffix(), commonv1.UserRole_USER_ROLE_VIEWER)
	opID, opTok := mk("gov-op-"+newTestSuffix(), commonv1.UserRole_USER_ROLE_OPERATOR)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gov := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	asset := "asset-gov-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, asset); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), adminLogin.Msg.GetUser().GetUserId(), asset); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, asset); err != nil {
		t.Fatal(err)
	}
	clusterID := seedProposalCluster(t, ctx, st.Pool(), asset, "/api/items", "GET",
		commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED, []*commonv1.DetectionKey{{
			DetectorId: "crs", DetectorVersion: kernel.CRSVersion, DetectorManifestDigest: kernel.CRSTarballSHA256,
			RuleId: "942100", Phase: "request", TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			TargetSelector: "query.id", NormalizationProfileDigest: "profile-test",
		}})
	intent := &governv1.ProposeArtifactRequest{
		Intent: &governv1.ProposalIntent{
			Kind:      commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
			ClusterId: clusterID,
			DetectionKeys: []*commonv1.DetectionKey{{
				DetectorId: "crs", RuleId: "942100",
				TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			}},
		},
		Scope: &artifactv1.Scope{AssetIds: []string{asset}},
	}

	viewReq := connect.NewRequest(intent)
	viewReq.Header().Set("Authorization", "Bearer "+viewerTok)
	if _, err := gov.ProposeArtifact(ctx, viewReq); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer want permission_denied got %v", err)
	}

	noGrant := connect.NewRequest(intent)
	noGrant.Header().Set("Authorization", "Bearer "+opTok)
	if _, err := gov.ProposeArtifact(ctx, noGrant); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("empty grant want permission_denied got %v", err)
	}

	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'user',$2,'["govern.propose"]',$3::jsonb,'admin')`,
		"g-"+newTestSuffix(), opID, fmt.Sprintf(`[{"kind":"asset","id":%q}]`, asset)); err != nil {
		t.Fatal(err)
	}
	ok := connect.NewRequest(intent)
	ok.Header().Set("Authorization", "Bearer "+opTok)
	ok.Header().Set("Idempotency-Key", "idem-gov-"+newTestSuffix())
	first, err := gov.ProposeArtifact(ctx, ok)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gov.ProposeArtifact(ctx, ok)
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.ReleaseId == "" || first.Msg.ReleaseId != second.Msg.ReleaseId {
		t.Fatalf("idempotent propose %s vs %s", first.Msg.ReleaseId, second.Msg.ReleaseId)
	}

	adminTok := adminLogin.Msg.Token
	cu := connect.NewRequest(&userv1.CreateUserRequest{Username: "idem-u-" + newTestSuffix(), Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_VIEWER})
	cu.Header().Set("Authorization", "Bearer "+adminTok)
	cu.Header().Set("Idempotency-Key", "idem-user-"+newTestSuffix())
	u1, err := users.CreateUser(ctx, cu)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := users.CreateUser(ctx, cu)
	if err != nil {
		t.Fatal(err)
	}
	if u1.Msg.User.UserId != u2.Msg.User.UserId {
		t.Fatalf("idempotent create user %s vs %s", u1.Msg.User.UserId, u2.Msg.User.UserId)
	}

	assets := NewAssetServer(st.Pool())
	ca := connect.NewRequest(&assetv1.CreateAssetRequest{Asset: &assetv1.Asset{Id: "new-" + asset, DisplayName: "n"}})
	ca.Header().Set("Authorization", "Bearer "+adminTok)
	ca.Header().Set("Idempotency-Key", "idem-asset-"+newTestSuffix())
	a1, err := assets.CreateAsset(ctx, ca)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := assets.CreateAsset(ctx, ca)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Msg.Asset.Id != a2.Msg.Asset.Id {
		t.Fatalf("idempotent create asset %s vs %s", a1.Msg.Asset.Id, a2.Msg.Asset.Id)
	}
}
