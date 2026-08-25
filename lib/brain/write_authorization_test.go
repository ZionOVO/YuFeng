package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	runv1 "yufeng/proto/gen/runv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestWriteRPCRoleAndBindingsTable(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	adminName := "wa-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), adminName, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: adminName, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, role commonv1.UserRole) (string, string) {
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
	_, viewerTok := mk("wa-view-"+newTestSuffix(), commonv1.UserRole_USER_ROLE_VIEWER)
	opID, opTok := mk("wa-op-"+newTestSuffix(), commonv1.UserRole_USER_ROLE_OPERATOR)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gov := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	assets := NewAssetServer(st.Pool())
	runs := NewRunServer(st.Pool(), priv)
	assetID := "wa-asset-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), adminLogin.Msg.GetUser().GetUserId(), assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	seedTaxonomyGeneration(t, ctx, st.Pool(), assetID)
	relID := "wa-rel-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by)
		VALUES($1,'draft','{}',86400,$2)`, relID, opID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, assetID); err != nil {
		t.Fatal(err)
	}

	type call func(tok string) error
	cases := []struct {
		name string
		fn   call
		tool string
	}{
		{"propose", func(tok string) error {
			req := connect.NewRequest(&governv1.ProposeArtifactRequest{
				Intent: &governv1.ProposalIntent{
					Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
					DetectionKeys: []*commonv1.DetectionKey{{
						DetectorId: "crs", RuleId: "942100",
					}},
				},
				Scope: &artifactv1.Scope{AssetIds: []string{assetID}},
			})
			req.Header().Set("Authorization", "Bearer "+tok)
			setTestIdempotency(req)
			_, err := gov.ProposeArtifact(ctx, req)
			return err
		}, "govern.propose"},
		{"gate", func(tok string) error {
			req := connect.NewRequest(&governv1.GateArtifactRequest{ReleaseId: relID})
			req.Header().Set("Authorization", "Bearer "+tok)
			setTestIdempotency(req)
			_, err := gov.GateArtifact(ctx, req)
			return err
		}, "govern.gate"},
		{"start_shadow", func(tok string) error {
			req := connect.NewRequest(&governv1.StartShadowRequest{ReleaseId: relID})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := gov.StartShadow(ctx, req)
			return err
		}, "govern.start_shadow"},
		{"promote_canary", func(tok string) error {
			req := connect.NewRequest(&governv1.PromoteCanaryRequest{ReleaseId: relID, CanaryPercent: 5})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := gov.PromoteCanary(ctx, req)
			return err
		}, "govern.promote_canary"},
		{"promote_enforce", func(tok string) error {
			req := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := gov.PromoteEnforce(ctx, req)
			return err
		}, "govern.promote_enforce"},
		{"rollback", func(tok string) error {
			req := connect.NewRequest(&governv1.RollbackReleaseRequest{ReleaseId: relID, Reason: "x"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := gov.RollbackRelease(ctx, req)
			return err
		}, "govern.rollback"},
		{"retire", func(tok string) error {
			req := connect.NewRequest(&governv1.RetireReleaseRequest{ReleaseId: relID, Reason: "x"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := gov.RetireRelease(ctx, req)
			return err
		}, "govern.retire"},
		{"deny_feedback", func(tok string) error {
			req := connect.NewRequest(&governv1.DenyFeedbackRequest{ReleaseId: relID, EventId: "e1"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := gov.DenyFeedback(ctx, req)
			return err
		}, "govern.deny_feedback"},
		{"create_run", func(tok string) error {
			req := connect.NewRequest(&runv1.CreateRunRequest{Role: "worker"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := runs.CreateRun(ctx, req)
			return err
		}, "run.create"},
		{"cancel_run", func(tok string) error {
			req := connect.NewRequest(&runv1.CancelRunRequest{RunId: "missing"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := runs.CancelRun(ctx, req)
			return err
		}, "run.create"},
		{"create_asset", func(tok string) error {
			req := connect.NewRequest(&assetv1.CreateAssetRequest{Asset: &assetv1.Asset{DisplayName: "n"}})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := assets.CreateAsset(ctx, req)
			return err
		}, "asset.create"},
		{"update_asset", func(tok string) error {
			req := connect.NewRequest(&assetv1.UpdateAssetRequest{
				AssetId: assetID, Asset: &assetv1.Asset{DisplayName: "n2"},
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
			})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := assets.UpdateAsset(ctx, req)
			return err
		}, "asset.update"},
		{"attach_unit", func(tok string) error {
			req := connect.NewRequest(&assetv1.AttachUnitRequest{AssetId: assetID, UnitId: "u1"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := assets.AttachUnit(ctx, req)
			return err
		}, "asset.update"},
		{"detach_unit", func(tok string) error {
			req := connect.NewRequest(&assetv1.DetachUnitRequest{AssetId: assetID, UnitId: "u1"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := assets.DetachUnit(ctx, req)
			return err
		}, "asset.update"},
		{"create_user", func(tok string) error {
			req := connect.NewRequest(&userv1.CreateUserRequest{Username: "x", Password: "Operator123"})
			req.Header().Set("Authorization", "Bearer "+tok)
			_, err := users.CreateUser(ctx, req)
			return err
		}, "user.admin"},
	}

	for _, c := range cases {
		t.Run("viewer/"+c.name, func(t *testing.T) {
			err := c.fn(viewerTok)
			if connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("viewer %s want permission_denied got %v", c.name, err)
			}
		})
		t.Run("operator-no-grant/"+c.name, func(t *testing.T) {
			err := c.fn(opTok)
			if connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("operator %s want permission_denied got %v", c.name, err)
			}
		})
	}

	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'user',$2,$3::jsonb,$4::jsonb,'admin')`,
		"g-"+newTestSuffix(), opID,
		`["govern.propose","govern.gate","govern.start_shadow","run.create","asset.update"]`,
		fmt.Sprintf(`[{"kind":"asset","id":%q}]`, assetID)); err != nil {
		t.Fatal(err)
	}
	if err := cases[3].fn(opTok); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator promote_canary must stay denied, got %v", err)
	}
	if err := cases[4].fn(opTok); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator promote_enforce must stay denied, got %v", err)
	}
	if err := cases[14].fn(opTok); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator user.admin must stay denied, got %v", err)
	}
}
