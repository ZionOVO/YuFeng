package brain

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	assetv1 "yufeng/proto/gen/assetv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestAssetCRUDAdminOnly(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	adminName := "aa-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), adminName, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	assets := NewAssetServer(st.Pool())
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: adminName, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	opReq := connect.NewRequest(&userv1.CreateUserRequest{Username: "aa-op-" + newTestSuffix(), Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR, DisplayName: "op"})
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

	local := "aa-local-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, local); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), adminLogin.Msg.User.UserId, local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, local); err != nil {
		t.Fatal(err)
	}

	create := func(tok, name string) (*connect.Response[assetv1.CreateAssetResponse], error) {
		req := connect.NewRequest(&assetv1.CreateAssetRequest{Asset: &assetv1.Asset{DisplayName: name}})
		req.Header().Set("Authorization", "Bearer "+tok)
		setTestIdempotency(req)
		return assets.CreateAsset(ctx, req)
	}
	if _, err := create(opLogin.Msg.Token, "denied"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator create want permission_denied got %v", err)
	}
	created, err := create(adminLogin.Msg.Token, "web-app")
	if err != nil {
		t.Fatal(err)
	}
	extra := created.Msg.Asset.Id
	if extra == "" {
		t.Fatal("create must assign id")
	}

	upd := connect.NewRequest(&assetv1.UpdateAssetRequest{
		AssetId:    extra,
		Asset:      &assetv1.Asset{DisplayName: "web-app-renamed"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
	})
	upd.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	if _, err := assets.UpdateAsset(ctx, upd); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator update want permission_denied got %v", err)
	}
	upd.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	got, err := assets.UpdateAsset(ctx, upd)
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.Asset.DisplayName != "web-app-renamed" {
		t.Fatalf("display=%s", got.Msg.Asset.DisplayName)
	}
	if got.Msg.Asset.UpdatedAt == nil {
		t.Fatal("updated_at must be returned")
	}
	stale := connect.NewRequest(&assetv1.UpdateAssetRequest{
		AssetId:           extra,
		Asset:             &assetv1.Asset{DisplayName: "stale"},
		UpdateMask:        &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
		ExpectedUpdatedAt: timestamppb.New(got.Msg.Asset.UpdatedAt.AsTime().Add(-time.Hour)),
	})
	stale.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	if _, err := assets.UpdateAsset(ctx, stale); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "version_mismatch") {
		t.Fatalf("stale updated_at want version_mismatch got %v", err)
	}

	list := connect.NewRequest(&assetv1.ListAssetsRequest{})
	list.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	page, err := assets.ListAssets(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range page.Msg.Assets {
		if d.GetAsset() != nil {
			seen[d.Asset.Id] = true
		}
	}
	if !seen[local] || !seen[extra] {
		t.Fatalf("onboarding list want local+created, got %v", seen)
	}

	delOp := connect.NewRequest(&assetv1.DeleteAssetRequest{AssetId: extra})
	delOp.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	if _, err := assets.DeleteAsset(ctx, delOp); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator delete want permission_denied got %v", err)
	}
	del := connect.NewRequest(&assetv1.DeleteAssetRequest{AssetId: extra})
	del.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	if _, err := assets.DeleteAsset(ctx, del); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.GetAsset(ctx, func() *connect.Request[assetv1.GetAssetRequest] {
		r := connect.NewRequest(&assetv1.GetAssetRequest{AssetId: extra})
		r.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
		return r
	}()); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("deleted get want permission_denied got %v", err)
	}

	if err := writeAdminSystemGrant(ctx, st.Pool(), adminLogin.Msg.User.UserId, local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, local); err != nil {
		t.Fatal(err)
	}
	after, err := create(adminLogin.Msg.Token, "after-complete")
	if err != nil {
		t.Fatal(err)
	}
	list.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	page, err = assets.ListAssets(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	seen = map[string]bool{}
	for _, d := range page.Msg.Assets {
		if d.GetAsset() != nil {
			seen[d.Asset.Id] = true
		}
	}
	if !seen[local] || !seen[after.Msg.Asset.Id] {
		t.Fatalf("completed list want local+created, got %v", seen)
	}
}
