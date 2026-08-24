package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	assetv1 "yufeng/proto/gen/assetv1"
	auditv1 "yufeng/proto/gen/auditv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	consolev1 "yufeng/proto/gen/consolev1"
	eventv1 "yufeng/proto/gen/eventv1"
	governv1 "yufeng/proto/gen/governv1"
	grantv1 "yufeng/proto/gen/grantv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestLoginGetMeAccessFromGrants(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "g0a-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	if login.Msg.Access == nil {
		t.Fatal("Login must not return empty access")
	}
	if !containsAll(login.Msg.Access.Tools, "user.admin", "grant.write", "console.read") {
		t.Fatalf("incomplete admin tools=%v", login.Msg.Access.Tools)
	}
	if hasAnyPrefix(login.Msg.Access.Tools, "govern.") {
		t.Fatalf("incomplete admin must not expand govern.*: %v", login.Msg.Access.Tools)
	}
	if len(login.Msg.Access.Bindings) != 0 {
		t.Fatalf("incomplete admin bindings=%v want empty", login.Msg.Access.Bindings)
	}

	meReq := connect.NewRequest(&authv1.GetMeRequest{})
	meReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	me, err := auth.GetMe(ctx, meReq)
	if err != nil {
		t.Fatal(err)
	}
	if me.Msg.Access == nil || !containsAll(me.Msg.Access.Tools, "grant.write") {
		t.Fatalf("GetMe access=%v", me.Msg.Access)
	}

	users := NewUserServer(st.Pool(), 8)
	opName := "g0op-" + newTestSuffix()
	cu := connect.NewRequest(&userv1.CreateUserRequest{Username: opName, Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR})
	cu.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	setTestIdempotency(cu)
	if _, err := users.CreateUser(ctx, cu); err != nil {
		t.Fatal(err)
	}
	opLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: opName, Password: "Operator123"}))
	if err != nil {
		t.Fatal(err)
	}
	if opLogin.Msg.Access == nil {
		t.Fatal("Login must not return empty access")
	}
	if hasAnyPrefix(opLogin.Msg.Access.Tools, "govern.") {
		t.Fatalf("no-grant operator tools=%v", opLogin.Msg.Access.Tools)
	}

	local := "asset-access-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, local); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.User.UserId, local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, local); err != nil {
		t.Fatal(err)
	}
	done, err := auth.GetMe(ctx, meReq)
	if err != nil {
		t.Fatal(err)
	}
	if done.Msg.Access == nil {
		t.Fatal("completed GetMe must not return empty access")
	}
	if !containsAll(done.Msg.Access.Tools, "grant.write", "user.admin", "catalog.manage", "console.read") {
		t.Fatalf("completed admin tools=%v", done.Msg.Access.Tools)
	}
	if hasAnyPrefix(done.Msg.Access.Tools, "govern.") {
		t.Fatalf("system grant must not include govern.*: %v", done.Msg.Access.Tools)
	}
	if !bindingHas(done.Msg.Access.Bindings, "asset", local) {
		t.Fatalf("completed bindings=%v want %s", done.Msg.Access.Bindings, local)
	}
}

func TestListCutsByBindingsAndOnboarding(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "g0b-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	assets := NewAssetServer(st.Pool())
	console := NewConsoleServer(st.Pool())
	gov := NewGovernServer(st.Pool(), mustKey(t), 0, 0, 0, 0)
	audit := NewAuditServer(st.Pool())

	dash := connect.NewRequest(&consolev1.DashboardRequest{})
	dash.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := console.Dashboard(ctx, dash); !isReason(err, connect.CodeFailedPrecondition, "onboarding_incomplete") {
		t.Fatalf("Dashboard want onboarding_incomplete got %v", err)
	}
	ev := connect.NewRequest(&consolev1.ListEventsRequest{})
	ev.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := console.ListEvents(ctx, ev); !isReason(err, connect.CodeFailedPrecondition, "onboarding_incomplete") {
		t.Fatalf("ListEvents want onboarding_incomplete got %v", err)
	}
	lr := connect.NewRequest(&governv1.ListReleasesRequest{})
	lr.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := gov.ListReleases(ctx, lr); !isReason(err, connect.CodeFailedPrecondition, "onboarding_incomplete") {
		t.Fatalf("ListReleases want onboarding_incomplete got %v", err)
	}

	local := "asset-local-" + newTestSuffix()
	other := "asset-other-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1'),($2,$2,'L1')`, local, other); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateEdgeLive, local); err != nil {
		t.Fatal(err)
	}
	listA := connect.NewRequest(&assetv1.ListAssetsRequest{})
	listA.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	got, err := assets.ListAssets(ctx, listA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.Assets) != 2 {
		t.Fatalf("dataplane-live onboarding must expose all existing assets, got %v", got.Msg.Assets)
	}

	users := NewUserServer(st.Pool(), 8)
	viewName := "g0v-" + newTestSuffix()
	cu := connect.NewRequest(&userv1.CreateUserRequest{Username: viewName, Password: "Viewer1234", Role: commonv1.UserRole_USER_ROLE_VIEWER})
	cu.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	setTestIdempotency(cu)
	view, err := users.CreateUser(ctx, cu)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.User.UserId, local); err != nil {
		t.Fatal(err)
	}
	if err := insertUserGrant(ctx, st.Pool(), view.Msg.User.UserId, []string{"console.read"}, local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, local); err != nil {
		t.Fatal(err)
	}
	viewLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: viewName, Password: "Viewer1234"}))
	if err != nil {
		t.Fatal(err)
	}
	listV := connect.NewRequest(&assetv1.ListAssetsRequest{})
	listV.Header().Set("Authorization", "Bearer "+viewLogin.Msg.Token)
	listed, err := assets.ListAssets(ctx, listV)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.Assets) != 1 || listed.Msg.Assets[0].GetAsset().GetId() != local {
		t.Fatalf("viewer ListAssets must not be role-denied, got %v err=%v", listed.Msg.Assets, err)
	}
	getOther := connect.NewRequest(&assetv1.GetAssetRequest{AssetId: other})
	getOther.Header().Set("Authorization", "Bearer "+viewLogin.Msg.Token)
	if _, err := assets.GetAsset(ctx, getOther); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("out-of-scope GetAsset want permission_denied got %v", err)
	}

	relID := "rel-access-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by)
		VALUES($1,'draft','{}',86400,'someone')`, relID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, other); err != nil {
		t.Fatal(err)
	}
	getRel := connect.NewRequest(&governv1.GetReleaseRequest{ReleaseId: relID})
	getRel.Header().Set("Authorization", "Bearer "+viewLogin.Msg.Token)
	if _, err := gov.GetRelease(ctx, getRel); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("out-of-scope GetRelease want permission_denied got %v", err)
	}

	eid := "ev-access-" + newTestSuffix()
	payload, err := protojson.Marshal(&eventv1.Event{Id: eid, AssetId: other, OccurredAt: timestamppb.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO events(event_id, asset_id, occurred_at, kind, verdict, payload)
		VALUES($1,$2,now(),'http_request','allow',$3::jsonb)`, eid, other, string(payload)); err != nil {
		t.Fatal(err)
	}
	getEv := connect.NewRequest(&consolev1.GetEventRequest{EventId: eid})
	getEv.Header().Set("Authorization", "Bearer "+viewLogin.Msg.Token)
	if _, err := console.GetEvent(ctx, getEv); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("out-of-scope GetEvent want permission_denied got %v", err)
	}
	if err := appendAudit(ctx, st.Pool(), "user", "u", "x", "asset", other, nil); err != nil {
		t.Fatal(err)
	}
	la := connect.NewRequest(&auditv1.ListAuditEntriesRequest{ObjectType: "asset", ObjectId: other})
	la.Header().Set("Authorization", "Bearer "+viewLogin.Msg.Token)
	aud, err := audit.ListAuditEntries(ctx, la)
	if err != nil {
		t.Fatal(err)
	}
	if len(aud.Msg.Entries) != 0 {
		t.Fatalf("audit list must hide out-of-scope, got %d", len(aud.Msg.Entries))
	}
}

func TestPromoteHonorsGrantNotRoleTemplate(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "g0p-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	assetID := "asset-prom-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), adminLogin.Msg.User.UserId, assetID); err != nil {
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
	propID, propTok := mk("g0prop-" + newTestSuffix())
	offID, offTok := mk("g0off-" + newTestSuffix())
	if err := insertUserGrant(ctx, st.Pool(), propID, []string{"govern.propose"}, assetID); err != nil {
		t.Fatal(err)
	}
	if err := insertUserGrant(ctx, st.Pool(), offID, []string{"govern.promote_enforce"}, assetID); err != nil {
		t.Fatal(err)
	}
	relID := "rel-prom-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by, canary_percent, canary_started_at)
		VALUES($1,'canary','{}',86400,$2,5,now()-interval '1 second')`, relID, propID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, assetID); err != nil {
		t.Fatal(err)
	}
	gov := NewGovernServer(st.Pool(), mustKey(t), 0, 0, 0, 0)
	self := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
	self.Header().Set("Authorization", "Bearer "+propTok)
	if err := insertUserGrant(ctx, st.Pool(), propID, []string{"govern.propose", "govern.promote_enforce"}, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := gov.PromoteEnforce(ctx, self); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("proposer promote want permission_denied got %v", err)
	}
	ok := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
	ok.Header().Set("Authorization", "Bearer "+offTok)
	ok.Header().Set("Idempotency-Key", "promote-"+newTestSuffix())
	got, err := gov.PromoteEnforce(ctx, ok)
	if err != nil {
		t.Fatalf("operator with promote grant must proceed: %v", err)
	}
	if got.Msg.Release.GetState() != commonv1.ReleaseState_RELEASE_STATE_ENFORCE {
		t.Fatalf("state=%s", got.Msg.Release.GetState())
	}
}

func TestGrantAuthOverlayAndScope(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "g0g-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	adminLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	local := "asset-g-" + newTestSuffix()
	other := "asset-other-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1'),($2,$2,'L1')`, local, other); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateEdgeLive, local); err != nil {
		t.Fatal(err)
	}
	opName := "g0gop-" + newTestSuffix()
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
	grants := NewGrantServer(st.Pool())
	put := func(tok, subject string, tools []string, asset string) error {
		req := connect.NewRequest(&grantv1.PutGrantRequest{
			SubjectUserId: subject,
			Tools:         tools,
			Bindings:      []*grantv1.BindingRef{{Kind: "asset", Id: asset}},
		})
		req.Header().Set("Authorization", "Bearer "+tok)
		_, err := grants.PutGrant(ctx, req)
		return err
	}
	if err := put(opLogin.Msg.Token, adminLogin.Msg.User.UserId, []string{"console.read"}, local); !isReason(err, connect.CodePermissionDenied, "grant_missing") && connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("login-only PutGrant want permission_denied got %v", err)
	}
	rev := connect.NewRequest(&grantv1.RevokeGrantRequest{GrantId: "missing"})
	rev.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	if _, err := grants.RevokeGrant(ctx, rev); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("login-only RevokeGrant want permission_denied got %v", err)
	}
	listOther := connect.NewRequest(&grantv1.ListGrantsRequest{SubjectUserId: adminLogin.Msg.User.UserId})
	listOther.Header().Set("Authorization", "Bearer "+opLogin.Msg.Token)
	if _, err := grants.ListGrants(ctx, listOther); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListGrants other want permission_denied got %v", err)
	}
	if err := put(adminLogin.Msg.Token, op.Msg.User.UserId, []string{"govern.promote_canary"}, other); err != nil {
		t.Fatalf("onboarding administrator may grant any existing asset: %v", err)
	}
	if err := put(adminLogin.Msg.Token, op.Msg.User.UserId, []string{"govern.promote_canary"}, local); err != nil {
		t.Fatal(err)
	}
	if err := put(adminLogin.Msg.Token, op.Msg.User.UserId, []string{"govern.promote_enforce"}, local); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM grants WHERE subject_kind='user' AND subject_id=$1`, op.Msg.User.UserId).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("PutGrant must overlay same subject, rows=%d", n)
	}
	var toolsRaw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT tools FROM grants WHERE subject_kind='user' AND subject_id=$1`, op.Msg.User.UserId).Scan(&toolsRaw); err != nil {
		t.Fatal(err)
	}
	var tools []string
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		t.Fatal(err)
	}
	if !containsAll(tools, "govern.promote_enforce") || containsAll(tools, "govern.promote_canary") {
		t.Fatalf("overlay should replace tools, got %v", tools)
	}

	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, local); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), adminLogin.Msg.User.UserId, local); err != nil {
		t.Fatal(err)
	}
	if err := put(adminLogin.Msg.Token, op.Msg.User.UserId, []string{"console.read"}, other); !isReason(err, connect.CodePermissionDenied, "grant_scope") {
		t.Fatalf("completed grant_scope want deny got %v", err)
	}
}

func TestBootNoBootstrapAssetGrant(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	name := "g0boot-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), name, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), name, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM grants g JOIN users u ON u.user_id=g.subject_id WHERE u.username=$1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("first start must not write grants, got %d", n)
	}
	var boot int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM grants g JOIN users u ON u.user_id=g.subject_id
		WHERE u.username=$1 AND bindings::text LIKE '%bootstrap%'`, name).Scan(&boot); err != nil {
		t.Fatal(err)
	}
	if boot != 0 {
		t.Fatal("fictional bootstrap asset binding must not exist")
	}

	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: name, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	local := "asset-boot-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, local); err != nil {
		t.Fatal(err)
	}
	check := completeCheck{
		AdminUserID: login.Msg.User.UserId, JarvisAgentID: "jarvis-access-" + newTestSuffix(),
		LocalAssetID: local, ModelLive: true, EdgeReady: true,
	}
	missing := missingCompletePredicates(ctx, st.Pool(), check)
	if !containsInt(missing, 2) || !containsInt(missing, 4) {
		t.Fatalf("missing predicates=%v want 2 and 4", missing)
	}
	if _, err := completeOnboarding(ctx, st.Pool(), check); err == nil {
		t.Fatal("complete without four predicates must fail")
	}
	var state string
	_ = st.Pool().QueryRow(ctx, `SELECT state FROM deployment_onboarding WHERE id=1`).Scan(&state)
	if state == OnboardingStateCompleted {
		t.Fatal("must not enter COMPLETED")
	}

	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, last_heartbeat_at)
		VALUES($1,'x','orchestrator',now())`, check.JarvisAgentID); err != nil {
		t.Fatal(err)
	}
	other := "usr-other-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO users(user_id, username, display_name, role, state, password_hash)
		VALUES($1,$1,$1,'operator','active','x')`, other); err != nil {
		t.Fatal(err)
	}
	if err := insertUserGrant(ctx, st.Pool(), other, []string{"govern.promote_enforce"}, local); err != nil {
		t.Fatal(err)
	}
	if missing = missingCompletePredicates(ctx, st.Pool(), check); len(missing) != 0 {
		t.Fatalf("all four should pass, missing=%v", missing)
	}
	if _, err := completeOnboarding(ctx, st.Pool(), check); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM deployment_onboarding WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != OnboardingStateCompleted {
		t.Fatalf("state=%s", state)
	}
	meReq := connect.NewRequest(&authv1.GetMeRequest{})
	meReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	me, err := auth.GetMe(ctx, meReq)
	if err != nil {
		t.Fatal(err)
	}
	if hasAnyPrefix(me.Msg.Access.GetTools(), "govern.") {
		t.Fatalf("system grant has govern.*: %v", me.Msg.Access.Tools)
	}
	if !bindingHas(me.Msg.Access.GetBindings(), "asset", local) {
		t.Fatalf("system grant bindings=%v", me.Msg.Access.Bindings)
	}
}

func TestReleaseProtoAndDashboardKeys(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "g0r-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	assetID := "asset-rel-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.User.UserId, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	relID := "rel-meta-" + newTestSuffix()
	proposed := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by, proposed_at, signed_at)
		VALUES($1,'signed','{}',86400,'jarvis-1',$2,$2)`, relID, proposed); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, assetID); err != nil {
		t.Fatal(err)
	}
	gov := NewGovernServer(st.Pool(), mustKey(t), 0, 0, 0, 0)
	get := connect.NewRequest(&governv1.GetReleaseRequest{ReleaseId: relID})
	get.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	got, err := gov.GetRelease(ctx, get)
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.Release.GetCreatedBy() != "jarvis-1" {
		t.Fatalf("created_by=%q", got.Msg.Release.GetCreatedBy())
	}
	if got.Msg.Release.GetProposedAt() == nil || got.Msg.Release.GetSignedAt() == nil {
		t.Fatalf("timestamps missing: %+v", got.Msg.Release)
	}
	list := connect.NewRequest(&governv1.ListReleasesRequest{AssetId: assetID})
	list.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	listed, err := gov.ListReleases(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range listed.Msg.Releases {
		if r.ReleaseId == relID {
			found = true
			if r.CreatedBy != "jarvis-1" || r.ProposedAt == nil {
				t.Fatalf("list release proto incomplete: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("ListReleases missing release")
	}
	console := NewConsoleServer(st.Pool())
	dash := connect.NewRequest(&consolev1.DashboardRequest{})
	dash.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	d, err := console.Dashboard(ctx, dash)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Msg.ReleasesByState["draft"]; ok {
		t.Fatal("Dashboard keys must not use short name draft")
	}
	if d.Msg.ReleasesByState["RELEASE_STATE_SIGNED"] < 1 {
		t.Fatalf("want RELEASE_STATE_SIGNED key, got %v", d.Msg.ReleasesByState)
	}
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func insertUserGrant(ctx context.Context, pool *pgxpool.Pool, subject string, tools []string, asset string) error {
	raw, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	id, err := newID("gr")
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'user',$2,$3::jsonb,$4::jsonb,'test')`,
		id, subject, raw, fmt.Sprintf(`[{"kind":"asset","id":%q}]`, asset))
	return err
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func hasAnyPrefix(have []string, prefix string) bool {
	for _, h := range have {
		if strings.HasPrefix(h, prefix) {
			return true
		}
	}
	return false
}

func bindingHas(binds []*grantv1.BindingRef, kind, id string) bool {
	for _, b := range binds {
		if b.GetKind() == kind && b.GetId() == id {
			return true
		}
	}
	return false
}

func containsInt(have []int32, want int32) bool {
	for _, h := range have {
		if h == want {
			return true
		}
	}
	return false
}

func isReason(err error, code connect.Code, reason string) bool {
	if connect.CodeOf(err) != code {
		return false
	}
	return err != nil && strings.Contains(err.Error(), reason)
}
