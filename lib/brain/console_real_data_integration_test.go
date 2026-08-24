package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "yufeng/proto/gen/agentv1"
	"yufeng/proto/gen/agentv1/agentv1connect"
	authv1 "yufeng/proto/gen/authv1"
	"yufeng/proto/gen/authv1/authv1connect"
	casev1 "yufeng/proto/gen/casev1"
	"yufeng/proto/gen/casev1/casev1connect"
	consolev1 "yufeng/proto/gen/consolev1"
	"yufeng/proto/gen/consolev1/consolev1connect"
	grantv1 "yufeng/proto/gen/grantv1"
	modelv1 "yufeng/proto/gen/modelv1"
	"yufeng/proto/gen/modelv1/modelv1connect"
	modulev1 "yufeng/proto/gen/modulev1"
	"yufeng/proto/gen/modulev1/modulev1connect"
)

// TestConsoleHTTPServicesReadPostgreSQL 验证控制台使用的真实网络服务从 PostgreSQL 读取同一份业务数据。
func TestConsoleHTTPServicesReadPostgreSQL(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()

	const (
		username = "console-real-admin"
		password = "ConsoleReal12345"
		assetID  = "console-real-asset"
		caseID   = "console-real-case"
	)
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), username, password); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$2,'L1')`, assetID, "真实控制台资产"); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title, summary)
		VALUES($1,'traffic-interception',$2,'open',91,'真实流量案件','来自 PostgreSQL 的冻结摘要')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}

	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewMux(st, BuildInfo{Version: "test", ContractVersion: "v1"}, Options{
		SessionTTL:        time.Hour,
		PasswordMinLength: MinPasswordLength,
		SigningKey:        signingKey,
		DevInsecure:       true,
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	authClient := authv1connect.NewAuthServiceClient(server.Client(), server.URL)
	login, err := authClient.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: username, Password: password}))
	if err != nil {
		t.Fatal(err)
	}
	token := login.Msg.GetToken()
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.GetUser().GetUserId(), assetID); err != nil {
		t.Fatal(err)
	}

	consoleClient := consolev1connect.NewConsoleServiceClient(server.Client(), server.URL)
	dashboard, err := consoleClient.Dashboard(ctx, bearerReq(token, &consolev1.DashboardRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Msg.GetAssetsTotal() != 1 {
		t.Fatalf("dashboard assets_total = %d, want 1", dashboard.Msg.GetAssetsTotal())
	}

	profileClient := agentv1connect.NewAgentProfileServiceClient(server.Client(), server.URL)
	created, err := profileClient.CreateAgentProfile(ctx, bearerReq(token, &agentv1.CreateAgentProfileRequest{
		DisplayName: "真实流量审查 Agent",
		Tools:       []string{"case.get", "case.request_evidence", "run.create", "case.complete"},
		Bindings:    []*grantv1.BindingRef{{Kind: "asset", Id: assetID}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := profileClient.ListAgentProfiles(ctx, bearerReq(token, &agentv1.ListAgentProfilesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles.Msg.GetProfiles()) != 1 || profiles.Msg.GetProfiles()[0].GetAgentId() != created.Msg.GetProfile().GetAgentId() {
		t.Fatalf("agent profiles did not return the persisted profile: %+v", profiles.Msg.GetProfiles())
	}

	caseClient := casev1connect.NewCaseServiceClient(server.Client(), server.URL)
	cases, err := caseClient.ListCases(ctx, bearerReq(token, &casev1.ListCasesRequest{AssetId: assetID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases.Msg.GetCases()) != 1 || cases.Msg.GetCases()[0].GetCaseId() != caseID {
		t.Fatalf("cases did not return the persisted case: %+v", cases.Msg.GetCases())
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
		SELECT $1,'run_progress','activity-'||sequence,'真实分页活动 '||sequence FROM generate_series(1,205) sequence`, caseID); err != nil {
		t.Fatal(err)
	}
	after := int64(0)
	seenActivities := map[int64]bool{}
	for range 3 {
		page, err := caseClient.PollCaseActivities(ctx, bearerReq(token, &casev1.PollCaseActivitiesRequest{
			CaseId: caseID, AfterSequence: after,
		}))
		if err != nil {
			t.Fatal(err)
		}
		for _, activity := range page.Msg.GetActivities() {
			if seenActivities[activity.GetSequence()] {
				t.Fatalf("duplicate case activity sequence %d", activity.GetSequence())
			}
			seenActivities[activity.GetSequence()] = true
		}
		after = page.Msg.GetNextAfterSequence()
	}
	if len(seenActivities) != 205 {
		t.Fatalf("case activity cursor returned %d rows want 205", len(seenActivities))
	}

	moduleClient := modulev1connect.NewModuleCatalogServiceClient(server.Client(), server.URL)
	modules, err := moduleClient.ListModules(ctx, bearerReq(token, &modulev1.ListModulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(modules.Msg.GetModules()) != 1 || modules.Msg.GetModules()[0].GetModuleId() != "traffic-interception" {
		t.Fatalf("module catalog = %+v", modules.Msg.GetModules())
	}

	modelClient := modelv1connect.NewModelGatewayServiceClient(server.Client(), server.URL)
	gateway, err := modelClient.GetModelGateway(ctx, bearerReq(token, &modelv1.GetModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if gateway.Msg.GetStatus() != modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_UNCONFIGURED {
		t.Fatalf("model gateway status = %s, want unconfigured", gateway.Msg.GetStatus())
	}
}
