package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	"yufeng/lib/observability"
	agentv1 "yufeng/proto/gen/agentv1"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	governv1 "yufeng/proto/gen/governv1"
	registryv1 "yufeng/proto/gen/registryv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestUploadEventsUnitQPS(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, assetID, token := seedUnitAsset(t, ctx, st, "qps")
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tel := NewTelemetryServer(st.Pool(), nil, NewAgentServer(st.Pool(), "boot", priv), "j")
	var tripped bool
	for i := 0; i < kernel.UnitRPCQPS+2; i++ {
		req := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{
			{Id: "qps-" + newTestSuffix(), OccurredAt: timestamppb.Now(), AssetId: assetID, Source: "t"},
		}})
		req.Header().Set("Authorization", "Bearer "+token)
		if _, err := tel.UploadEvents(ctx, req); connect.CodeOf(err) == connect.CodeResourceExhausted {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatal("unit rpc qps must trip")
	}
}

func TestLoginAndPublicAuthRate(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	now := time.Now()
	for i := 0; i < kernel.LoginRatePerMinute; i++ {
		if !auth.loginRate.Allow("u|1.1.1.1", now) {
			t.Fatal("within login quota")
		}
	}
	if auth.loginRate.Allow("u|1.1.1.1", now) {
		t.Fatal("login rate must trip")
	}
	if !auth.loginRate.Allow("u|1.1.1.1", now.Add(time.Minute+time.Second)) {
		t.Fatal("login rate must recover")
	}
	req := connect.NewRequest(&authv1.LoginRequest{Username: "nouser", Password: "x"})
	req.Header().Set("X-Forwarded-For", "203.0.113.9")
	if _, err := auth.Login(ctx, req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("want unauthenticated got %v", err)
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistryServer(st.Pool(), pub, "boot")
	src := "198.51.100.7"
	for i := 0; i < kernel.PublicAuthRatePerMinute; i++ {
		if !reg.publicAuth.Allow(src, now) {
			t.Fatal("within public quota")
		}
	}
	if reg.publicAuth.Allow(src, now) {
		t.Fatal("public auth must trip")
	}
	if !reg.publicAuth.Allow(src, now.Add(time.Minute+time.Second)) {
		t.Fatal("public auth must recover")
	}
}

func TestPollAndToolLimiters(t *testing.T) {
	now := time.Now()
	polls := newPollGate()
	for i := 0; i < kernel.LongPollConcurrencyPerAgent; i++ {
		if err := polls.acquire("a1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := polls.acquire("a1"); err == nil {
		t.Fatal("concurrency must trip")
	}
	polls.release("a1")
	if err := polls.acquire("a1"); err != nil {
		t.Fatal(err)
	}
	pr := newWindowLimiter(kernel.AgentPollQPS, time.Second)
	for i := 0; i < kernel.AgentPollQPS; i++ {
		if !pr.Allow("a1", now) {
			t.Fatal("poll qps")
		}
	}
	if pr.Allow("a1", now) {
		t.Fatal("poll qps must trip")
	}
	if !pr.Allow("a1", now.Add(1100*time.Millisecond)) {
		t.Fatal("poll qps recover")
	}
	tr := newWindowLimiter(kernel.ToolInvokeQPS, time.Second)
	for i := 0; i < kernel.ToolInvokeQPS; i++ {
		if !tr.Allow("jti", now) {
			t.Fatal("tool qps")
		}
	}
	if tr.Allow("jti", now) {
		t.Fatal("tool qps must trip")
	}
	if !tr.Allow("jti", now.Add(1100*time.Millisecond)) {
		t.Fatal("tool qps recover")
	}
}

func TestUnitRefreshLifecycle(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-" + newTestSuffix()
	reg := NewRegistryServer(st.Pool(), pub, boot)
	unit := "unit-ref-" + newTestSuffix()
	first := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unit, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: "t",
		ContractVersion: "v1", PubkeyHint: kernel.KeyID(pub),
		Asset: &assetv1.Asset{Id: unit}, Capabilities: testEdgeCapabilities(),
	})
	first.Header().Set("Authorization", "Bearer "+boot)
	ok, err := reg.Register(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Msg.RefreshToken == "" || ok.Msg.Token == "" {
		t.Fatal("register must return access and refresh")
	}
	oldRefresh := ok.Msg.RefreshToken
	oldAccess := ok.Msg.Token
	got, err := reg.Refresh(ctx, connect.NewRequest(&registryv1.RefreshRequest{UnitId: unit, RefreshToken: oldRefresh}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Refresh(ctx, connect.NewRequest(&registryv1.RefreshRequest{UnitId: unit, RefreshToken: oldRefresh})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("old refresh want unauthenticated got %v", err)
	}
	if err := InvalidateAccessTokens(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	hb := connect.NewRequest(&registryv1.HeartbeatRequest{UnitId: unit, Generation: 1})
	hb.Header().Set("Authorization", "Bearer "+oldAccess)
	if _, err := reg.Heartbeat(ctx, hb); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("restart must invalidate access, got %v", err)
	}
	live, err := reg.Refresh(ctx, connect.NewRequest(&registryv1.RefreshRequest{UnitId: unit, RefreshToken: got.Msg.RefreshToken}))
	if err != nil {
		t.Fatal(err)
	}
	okHB := connect.NewRequest(&registryv1.HeartbeatRequest{UnitId: unit, Generation: 2})
	okHB.Header().Set("Authorization", "Bearer "+live.Msg.Token)
	if _, err := reg.Heartbeat(ctx, okHB); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatGenerationDoesNotGoBackwards(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, _, token := seedUnitAsset(t, ctx, st, "gen")
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistryServer(st.Pool(), pub, "boot")
	rel := "rel-gen-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds) VALUES($1,'enforce','{}',86400)`, rel); err != nil {
		t.Fatal(err)
	}
	send := func(gen uint64, reqs uint64) {
		t.Helper()
		req := connect.NewRequest(&registryv1.HeartbeatRequest{
			UnitId: unitID, Generation: gen,
			ReleaseCounters: []*registryv1.ReleaseCounter{{
				ReleaseId: rel, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, RequestsTotal: reqs,
			}},
		})
		req.Header().Set("Authorization", "Bearer "+token)
		if _, err := reg.Heartbeat(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	send(2, 10)
	send(1, 99)
	var gotGen uint64
	var gotReq int64
	if err := st.Pool().QueryRow(ctx, `SELECT generation, requests_total FROM release_counters WHERE unit_id=$1 AND release_id=$2`, unitID, rel).Scan(&gotGen, &gotReq); err != nil {
		t.Fatal(err)
	}
	if gotGen != 2 || gotReq != 10 {
		t.Fatalf("old generation must not overwrite: gen=%d req=%d", gotGen, gotReq)
	}
}

func TestCrossUnitFeedAndSupersedeTombstone(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, a1, tok1 := seedUnitAsset(t, ctx, st, "f1")
	_, a2, tok2 := seedUnitAsset(t, ctx, st, "f2")
	art := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_POLICY, PayloadSchema: "policy/v1", CreatedBy: "t"}
	raw, err := protoJSON(art)
	if err != nil {
		t.Fatal(err)
	}
	r1, r2 := "rel-x1-"+newTestSuffix(), "rel-x2-"+newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, artifact_id, ttl_seconds) VALUES($1,'enforce',$2::jsonb,'art-1',86400),($3,'enforce',$2::jsonb,'art-2',86400)`, r1, raw, r2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2),($3,$4)`, r1, a1, r2, a2); err != nil {
		t.Fatal(err)
	}
	rel1, err := loadRelease(ctx, st.Pool(), r1)
	if err != nil {
		t.Fatal(err)
	}
	rel2, err := loadRelease(ctx, st.Pool(), r2)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishFeed(ctx, st.Pool(), rel1, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := publishFeed(ctx, st.Pool(), rel2, false, 0); err != nil {
		t.Fatal(err)
	}
	arts := NewArtifactServer(st.Pool())
	list := func(tok, cursor string, full bool) *artifactv1.ListReleasesResponse {
		t.Helper()
		req := connect.NewRequest(&artifactv1.ListReleasesRequest{Cursor: cursor, FullSnapshot: full})
		req.Header().Set("Authorization", "Bearer "+tok)
		resp, err := arts.ListReleases(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	snap1 := list(tok1, "", true)
	if err := publishFeed(ctx, st.Pool(), rel1, true, commonv1.RetireReason_RETIRE_REASON_SUPERSEDED); err != nil {
		t.Fatal(err)
	}
	after := list(tok1, snap1.NextCursor, false)
	found := false
	for _, it := range after.Items {
		if it.ReleaseId == r1 && it.Retired {
			found = true
		}
	}
	if !found {
		t.Fatal("unit1 incremental must see superseded tombstone")
	}
	other := list(tok2, "", true)
	for _, it := range other.Items {
		if it.ReleaseId == r1 {
			t.Fatal("unit2 must not see unit1 feed")
		}
	}
}

func TestCommitReleaseChangeAtomic(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, assetID, _ := seedUnitAsset(t, ctx, st, "atm")
	relID := "rel-atm-" + newTestSuffix()
	art := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_POLICY, PayloadSchema: "policy/v1", CreatedBy: "t", Id: "art-" + relID}
	raw, err := protoJSON(art)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, artifact_id, ttl_seconds) VALUES($1,'signed',$2::jsonb,$3,86400)`, relID, raw, art.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, assetID); err != nil {
		t.Fatal(err)
	}
	shadow := &kernel.Shadow{ID: relID, Envelope: art}
	if err := commitReleaseChange(ctx, st.Pool(), releaseWrite{
		rel: shadow, feed: true, actorType: "user", actorID: "u", action: "release.start_shadow",
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1`, relID).Scan(&state); err != nil || state != "shadow" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	var tl, feed, audit int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM release_timeline WHERE release_id=$1 AND to_state='shadow'`, relID).Scan(&tl); err != nil || tl < 1 {
		t.Fatalf("timeline %d %v", tl, err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM release_feed WHERE release_id=$1`, relID).Scan(&feed); err != nil || feed < 1 {
		t.Fatalf("feed %d %v", feed, err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE object_id=$1 AND action='release.start_shadow'`, relID).Scan(&audit); err != nil || audit < 1 {
		t.Fatalf("audit %d %v", audit, err)
	}
	beforeFeed, beforeAudit := feed, audit
	if err := commitReleaseChange(ctx, st.Pool(), releaseWrite{rel: fakeRel{}, feed: true, action: "x"}); err == nil {
		t.Fatal("unknown type must fail")
	}
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM release_feed WHERE release_id=$1`, relID).Scan(&feed)
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE object_id=$1`, relID).Scan(&audit)
	if feed != beforeFeed || audit != beforeAudit {
		t.Fatal("failed write must not add feed or audit")
	}
}

type fakeRel struct{}

func (fakeRel) ReleaseID() string              { return "nope" }
func (fakeRel) State() commonv1.ReleaseState   { return commonv1.ReleaseState_RELEASE_STATE_DRAFT }
func (fakeRel) Artifact() *artifactv1.Artifact { return &artifactv1.Artifact{} }

func TestListReleasesPageTokenAndMaxBytes(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "adm-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	login, err := NewAuthServer(st.Pool(), time.Hour, false, 8).Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	assetID := "asset-page-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.User.UserId, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gov := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	for i := 0; i < 3; i++ {
		relID := "rel-page-" + newTestSuffix()
		if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds) VALUES($1,'draft','{}',86400)`, relID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, assetID); err != nil {
			t.Fatal(err)
		}
	}
	req := connect.NewRequest(&governv1.ListReleasesRequest{PageSize: 1})
	req.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	first, err := gov.ListReleases(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.NextPageToken == "" || len(first.Msg.Releases) != 1 {
		t.Fatalf("page token missing: %+v", first.Msg)
	}
	req2 := connect.NewRequest(&governv1.ListReleasesRequest{PageSize: 1, PageToken: first.Msg.NextPageToken})
	req2.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	second, err := gov.ListReleases(ctx, req2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Msg.Releases) != 1 || second.Msg.Releases[0].ReleaseId == first.Msg.Releases[0].ReleaseId {
		t.Fatal("page_token must advance")
	}

	_, _, utok := seedUnitAsset(t, ctx, st, "bytes")
	arts := NewArtifactServer(st.Pool())
	br := connect.NewRequest(&artifactv1.ListReleasesRequest{MaxBytes: 1, FullSnapshot: true})
	br.Header().Set("Authorization", "Bearer "+utok)
	if _, err := arts.ListReleases(ctx, br); err != nil {
		t.Fatal(err)
	}
}

func TestEventGetHidesRawQuery(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, assetID, token := seedUnitAsset(t, ctx, st, "evq")
	eid := "evt-q-" + newTestSuffix()
	tel := NewTelemetryServer(st.Pool(), nil, NewAgentServer(st.Pool(), "boot", priv), "j")
	up := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{{
		Id: eid, OccurredAt: timestamppb.Now(), AssetId: assetID, Source: "t",
		Traffic: &eventv1.Event_Http{Http: &eventv1.Http{Method: "GET", Path: "/x", QueryRedacted: "id=1+UNION+SELECT+pw"}},
	}}})
	up.Header().Set("Authorization", "Bearer "+token)
	if _, err := tel.UploadEvents(ctx, up); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := st.Pool().QueryRow(ctx, `SELECT payload::text FROM events WHERE event_id=$1`, eid).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(stored), "UNION") || strings.Contains(stored, "pw") {
		t.Fatalf("ledger stored raw query: %s", stored)
	}
	gw := NewToolGatewayServer(st.Pool(), priv)
	gw.demoTriage = true
	agentID := "ag-q-" + newTestSuffix()
	boot := "b-" + newTestSuffix()
	as := NewAgentServer(st.Pool(), boot, priv)
	reg, err := as.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	capTok, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: agentID, AuthorizedParty: agentID, Audience: "tools",
		TokenID: "jti-q-" + newTestSuffix(), Tools: []string{"event.get"}, MaxCalls: 2, Bindings: []string{"asset:" + assetID},
		ExpiresAt: now.Add(time.Hour).Unix(), IssuedAt: now.Unix(),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	inv := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: "event.get", ArgsJson: `{"event_id":"` + eid + `"}`})
	inv.Header().Set("Authorization", "Bearer "+capTok)
	_ = reg
	out, err := gw.InvokeTool(ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Msg.ResultJson, "UNION") || strings.Contains(out.Msg.ResultJson, "SELECT") {
		t.Fatalf("event.get leaked raw query: %s", out.Msg.ResultJson)
	}
}

func TestDefaultPasswordRefused(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), "admin", "admin"); err == nil {
		t.Fatal("default password must fail")
	}
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), "admin-"+newTestSuffix(), "SafePass#2026"); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredMetricsNamed(t *testing.T) {
	body := observability.Default().Prometheus()
	for _, n := range observability.RequiredMetricNames {
		if !strings.Contains(body, n) {
			t.Fatalf("missing metric %s", n)
		}
	}
}

func TestListUsersPageToken(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "adm-u-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	login, err := NewAuthServer(st.Pool(), time.Hour, false, 8).Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	users := NewUserServer(st.Pool(), 8)
	for i := 0; i < 2; i++ {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO users(user_id, username, display_name, role, state, password_hash)
			VALUES($1,$2,$2,'viewer','active','x')`, "usr-p-"+newTestSuffix(), "puser-"+newTestSuffix()); err != nil {
			t.Fatal(err)
		}
	}
	req := connect.NewRequest(&userv1.ListUsersRequest{PageSize: 1})
	req.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	first, err := users.ListUsers(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.NextPageToken == "" {
		t.Fatal("users page token")
	}
}
