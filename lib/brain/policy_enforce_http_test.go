package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	"yufeng/lib/edgecore"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	grantv1 "yufeng/proto/gen/grantv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestProductionPolicyEnforceThenRetireHTTP(t *testing.T) {
	for pass := 1; pass <= 2; pass++ {
		t.Run(fmt.Sprintf("pass%d", pass), func(t *testing.T) {
			runProductionPolicyEnforcePass(t)
		})
	}
}

func runProductionPolicyEnforcePass(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "pol-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	if adminLogin.Msg.GetUser() == nil || adminLogin.Msg.User.UserId == "" {
		t.Fatal("admin login missing user id")
	}
	opName := "pol-op-" + newTestSuffix()
	cu := connect.NewRequest(&userv1.CreateUserRequest{Username: opName, Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR})
	cu.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	setTestIdempotency(cu)
	op, err := users.CreateUser(ctx, cu)
	if err != nil {
		t.Fatal(err)
	}
	opLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: opName, Password: "Operator123"}))
	if err != nil {
		t.Fatal(err)
	}
	officerName := "pol-off-" + newTestSuffix()
	co := connect.NewRequest(&userv1.CreateUserRequest{Username: officerName, Password: "Officer123", Role: commonv1.UserRole_USER_ROLE_ADMIN})
	co.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
	setTestIdempotency(co)
	officer, err := users.CreateUser(ctx, co)
	if err != nil {
		t.Fatal(err)
	}
	officerLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: officerName, Password: "Officer123"}))
	if err != nil {
		t.Fatal(err)
	}
	assetID := "asset-pol-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	seedTaxonomyGeneration(t, ctx, st.Pool(), assetID)
	if err := writeAdminSystemGrant(ctx, st.Pool(), adminLogin.Msg.User.UserId, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	grants := NewGrantServer(st.Pool())
	put := func(subject string, tools []string) {
		t.Helper()
		req := connect.NewRequest(&grantv1.PutGrantRequest{
			SubjectUserId: subject,
			Tools:         tools,
			Bindings:      []*grantv1.BindingRef{{Kind: "asset", Id: assetID}},
		})
		req.Header().Set("Authorization", "Bearer "+adminLogin.Msg.Token)
		if _, err := grants.PutGrant(ctx, req); err != nil {
			t.Fatalf("put grant %s: %v", subject, err)
		}
	}
	put(op.Msg.User.UserId, []string{"govern.propose", "govern.gate", "govern.start_shadow"})
	put(officer.Msg.User.UserId, []string{"govern.promote_canary", "govern.promote_enforce", "govern.retire"})
	crs, err := edgecore.SharedCoraza()
	if err != nil {
		t.Fatal(err)
	}
	attack := edgecore.Request{Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+password"}
	view := edgecore.Canonicalize(attack.Method, attack.Path, attack.Query, nil, nil, edgecore.DefaultInspectionProfile())
	inspection, err := crs.Inspect(context.Background(), edgecore.InspectionInput{View: view})
	if err != nil {
		t.Fatal(err)
	}
	var keys []*commonv1.DetectionKey
	for _, d := range inspection.Detections {
		if !edgecore.CRSAutoGovernRule(d.RuleID) {
			continue
		}
		keys = append(keys, &commonv1.DetectionKey{
			DetectorId: d.InspectorID, DetectorVersion: d.Version, DetectorManifestDigest: d.ManifestDigest,
			RuleId: d.RuleID, Phase: d.Phase, TargetLocation: d.Location,
			TargetSelector: d.Selector, NormalizationProfileDigest: d.ProfileDigest,
		})
	}
	if len(keys) == 0 {
		t.Fatal("need attack-class crs key for GET /api/items")
	}
	clusterID := seedProposalCluster(t, ctx, st.Pool(), assetID, attack.Path, attack.Method,
		commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED, keys)
	gov := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	prop := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Intent: &governv1.ProposalIntent{
			Kind:          commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
			ClusterId:     clusterID,
			DetectionKeys: keys,
		},
		Scope: &artifactv1.Scope{AssetIds: []string{assetID}},
		Ttl:   durationpb.New(time.Hour),
	})
	prop.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	prop.Header().Set("Idempotency-Key", "propose-"+newTestSuffix())
	proposed, err := gov.ProposeArtifact(ctx, prop)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	relID := proposed.Msg.ReleaseId
	gate := connect.NewRequest(&governv1.GateArtifactRequest{ReleaseId: relID})
	gate.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	gate.Header().Set("Idempotency-Key", "gate-"+newTestSuffix())
	gated, err := gov.GateArtifact(ctx, gate)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if gated.Msg.State != commonv1.ReleaseState_RELEASE_STATE_SIGNED {
		t.Fatalf("gate state=%s report=%v", gated.Msg.State, gated.Msg.ReplayReport)
	}
	shadow := connect.NewRequest(&governv1.StartShadowRequest{ReleaseId: relID})
	shadow.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	shadow.Header().Set("Idempotency-Key", "shadow-"+newTestSuffix())
	if _, err := gov.StartShadow(ctx, shadow); err != nil {
		t.Fatalf("shadow: %v", err)
	}
	enf := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
	enf.Header().Set("Authorization", "Bearer "+officerLogin.Msg.Token)
	enf.Header().Set("Idempotency-Key", "enforce-"+newTestSuffix())
	if _, err := gov.PromoteEnforce(ctx, enf); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	rel, err := loadRelease(ctx, st.Pool(), relID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	set := edgecore.NewReleaseSet()
	if err := edgecore.InstallSignedCRS(set, pub, priv); err != nil {
		t.Fatal(err)
	}
	if err := set.Apply(&artifactv1.ReleaseItem{
		ReleaseId: relID, Artifact: rel.Artifact(), Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE,
	}, pub); err != nil {
		t.Fatalf("apply enforce: %v", err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(up.Close)
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := edgecore.NewReleaseProxy(set, nil, u, assetID)
	edge := httptest.NewServer(proxy)
	t.Cleanup(edge.Close)
	resp, err := http.Get(edge.URL + "/api/items?id=1+UNION+SELECT+pw")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("enforce want 403 got %d", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(edge.URL + "/api/items?page=2")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("benign want 200 got %d", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	ret := connect.NewRequest(&governv1.RetireReleaseRequest{ReleaseId: relID, Reason: "manual"})
	ret.Header().Set("Authorization", "Bearer "+officerLogin.Msg.Token)
	ret.Header().Set("Idempotency-Key", "retire-"+newTestSuffix())
	if _, err := gov.RetireRelease(ctx, ret); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: relID, Artifact: rel.Artifact(), Retired: true}, pub); err != nil {
		t.Fatalf("apply retire: %v", err)
	}
	resp, err = http.Get(edge.URL + "/api/items?id=1+UNION+SELECT+pw")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retired want 200 got %d", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
}
