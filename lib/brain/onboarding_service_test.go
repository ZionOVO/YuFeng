package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	"yufeng/lib/store"

	agentv1 "yufeng/proto/gen/agentv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	grantv1 "yufeng/proto/gen/grantv1"
	modelv1 "yufeng/proto/gen/modelv1"
	onboardingv1 "yufeng/proto/gen/onboardingv1"
	"yufeng/proto/gen/onboardingv1/onboardingv1connect"
	userv1 "yufeng/proto/gen/userv1"
)

func TestCheckRequiredServicesRefuseStart(t *testing.T) {
	registered := make(map[string]struct{}, len(requiredServicePaths()))
	for _, path := range requiredServicePaths() {
		registered[path] = struct{}{}
	}
	delete(registered, "/yufeng.console.v1.ConsoleService/")
	if err := CheckRequiredServices(registered); err == nil {
		t.Fatal("missing ConsoleService must refuse start")
	}
	registered["/yufeng.console.v1.ConsoleService/"] = struct{}{}
	if err := CheckRequiredServices(registered); err != nil {
		t.Fatal(err)
	}
}

func TestNewMuxRegistersOnboarding(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewMux(st, BuildInfo{Version: "test", ContractVersion: "v1"}, Options{
		SessionTTL: time.Hour, PasswordMinLength: MinPasswordLength, SigningKey: priv,
		JarvisAgentID: defaultJarvisAgentID,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := onboardingv1connect.NewOnboardingServiceClient(srv.Client(), srv.URL)
	_, err = client.GetOnboarding(ctx, connect.NewRequest(&onboardingv1.GetOnboardingRequest{}))
	if connect.CodeOf(err) == connect.CodeUnimplemented || connect.CodeOf(err) == connect.CodeUnknown && err != nil && strings.Contains(err.Error(), "404") {
		t.Fatalf("OnboardingService must be registered, got %v", err)
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("GetOnboarding without login want unauthenticated, got %v", err)
	}
}

func TestOnboardingConstantsReferenced(t *testing.T) {
	ob := NewOnboardingServer(nil, "")
	if ob.defaultModel != kernel.DefaultChatModel {
		t.Fatalf("defaultModel=%s want kernel.DefaultChatModel", ob.defaultModel)
	}
	if kernel.EdgeOnlineWindow <= 0 {
		t.Fatal("edge online window must be positive")
	}
}

func TestOnboardingTransitionTable(t *testing.T) {
	cases := []struct {
		name        string
		from        string
		action      onboardingAction
		hasSecret   bool
		modelLive   bool
		wantIllegal bool
	}{
		{name: "pending put", from: OnboardingStatePending, action: actionPutModelConfig},
		{name: "pending complete", from: OnboardingStatePending, action: actionCompleteOnboarding},
		{name: "pending test no secret", from: OnboardingStatePending, action: actionTestModel, wantIllegal: true},
		{name: "pending specification", from: OnboardingStatePending, action: actionPutDeploymentSpec, wantIllegal: true},
		{name: "configured test", from: OnboardingStateModelConfigured, action: actionTestModel, hasSecret: true},
		{name: "configured specification", from: OnboardingStateModelConfigured, action: actionPutDeploymentSpec, hasSecret: true, wantIllegal: true},
		{name: "live specification", from: OnboardingStateModelLive, action: actionPutDeploymentSpec, hasSecret: true, modelLive: true},
		{name: "edge live specification", from: OnboardingStateEdgeLive, action: actionPutDeploymentSpec, hasSecret: true, modelLive: true},
		{name: "failed put", from: OnboardingStateFailed, action: actionPutModelConfig, hasSecret: true},
		{name: "failed test", from: OnboardingStateFailed, action: actionTestModel, hasSecret: true},
		{name: "failed specification without live", from: OnboardingStateFailed, action: actionPutDeploymentSpec, hasSecret: true, wantIllegal: true},
		{name: "failed specification after live", from: OnboardingStateFailed, action: actionPutDeploymentSpec, hasSecret: true, modelLive: true},
		{name: "completed put", from: OnboardingStateCompleted, action: actionPutModelConfig, wantIllegal: true},
		{name: "completed test", from: OnboardingStateCompleted, action: actionTestModel, hasSecret: true, modelLive: true, wantIllegal: true},
		{name: "completed specification", from: OnboardingStateCompleted, action: actionPutDeploymentSpec, hasSecret: true, modelLive: true, wantIllegal: true},
		{name: "completed complete", from: OnboardingStateCompleted, action: actionCompleteOnboarding, wantIllegal: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := onboardingEdgeError(tc.from, tc.action, tc.hasSecret, tc.modelLive)
			if tc.wantIllegal {
				if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
					t.Fatalf("want failed_precondition, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("legal edge: %v", err)
			}
		})
	}
}

func TestPendingCompleteAndFailedKeepsSecret(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	req := connect.NewRequest(&onboardingv1.CompleteOnboardingRequest{})
	req.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(req)
	_, err := ob.CompleteOnboarding(ctx, req)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("PENDING CompleteOnboarding want failed_precondition, got %v", err)
	}
	gate := onboardingGateOf(t, err)
	if !containsInt(gate.MissingPredicates, 1) {
		t.Fatalf("missing=%v want 1", gate.MissingPredicates)
	}
	if got := mustOnboardingState(t, ctx, st); got != OnboardingStatePending {
		t.Fatalf("state=%s want PENDING", got)
	}

	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return "", errors.New("upstream refused")
	}
	put := connect.NewRequest(&onboardingv1.PutModelConfigRequest{
		BaseUrl: "https://api.example.com/v1", Secret: "sk-keep-me-secret", Model: kernel.DefaultChatModel,
	})
	put.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(put)
	if _, err := ob.PutModelConfig(ctx, put); err != nil {
		t.Fatal(err)
	}
	test := connect.NewRequest(&onboardingv1.TestModelConnectivityRequest{})
	test.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(test)
	if _, err := ob.TestModelConnectivity(ctx, test); connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("probe fail want unavailable/failed_precondition, got %v", err)
	}
	if got := mustOnboardingState(t, ctx, st); got != OnboardingStateFailed {
		t.Fatalf("state=%s want FAILED", got)
	}
	got := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if !got.HasSecret {
		t.Fatal("FAILED must retain secret")
	}
	if strings.Contains(got.GetSecretHint(), "sk-keep-me-secret") || strings.Contains(got.String(), "sk-keep-me-secret") {
		t.Fatal("GetOnboarding must not echo plaintext secret")
	}
	if _, ok := any(ob).(interface{ ResetOnboarding(context.Context) error }); ok {
		t.Fatal("reset onboarding rpc must not exist")
	}
}

func TestPutModelConfigHTTPSWriteOnly(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	bad := connect.NewRequest(&onboardingv1.PutModelConfigRequest{BaseUrl: "http://api.example.com/v1", Secret: "sk-x"})
	bad.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(bad)
	if _, err := ob.PutModelConfig(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("http base_url want invalid_argument, got %v", err)
	}
	put := connect.NewRequest(&onboardingv1.PutModelConfigRequest{BaseUrl: "https://api.example.com/v1", Secret: "sk-plain-must-not-leak"})
	put.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(put)
	if _, err := ob.PutModelConfig(ctx, put); err != nil {
		t.Fatal(err)
	}
	got := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if got.GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_MODEL_CONFIGURED {
		t.Fatalf("state=%s", got.GetState())
	}
	if got.GetModel() != kernel.DefaultChatModel {
		t.Fatalf("empty model want %s got %s", kernel.DefaultChatModel, got.GetModel())
	}
	if !got.HasSecret || got.GetBaseUrl() != "https://api.example.com/v1" {
		t.Fatalf("projection=%v", got)
	}
	if strings.Contains(got.String(), "sk-plain-must-not-leak") {
		t.Fatal("GetOnboarding leaked plaintext")
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM deployment_onboarding WHERE base_url LIKE '%sk-%' OR last_error LIKE '%sk-%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("plaintext secret must not land in ordinary columns")
	}

	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return "pong", nil
	}
	test := connect.NewRequest(&onboardingv1.TestModelConnectivityRequest{})
	test.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(test)
	if _, err := ob.TestModelConnectivity(ctx, test); err != nil {
		t.Fatal(err)
	}
	if mustGetOnboarding(t, ctx, ob, h.adminTok).GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_MODEL_LIVE {
		t.Fatal("probe should reach MODEL_LIVE")
	}
	again := connect.NewRequest(&onboardingv1.PutModelConfigRequest{BaseUrl: "https://api.example.com/v1", Secret: "sk-rotated"})
	again.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(again)
	if _, err := ob.PutModelConfig(ctx, again); err != nil {
		t.Fatal(err)
	}
	if mustGetOnboarding(t, ctx, ob, h.adminTok).GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_MODEL_CONFIGURED {
		t.Fatal("overwrite secret must return MODEL_CONFIGURED")
	}
}

func TestModelConnectivityBrainOutbound(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	var seenAuth, seenPath string
	var seenModel string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		seenModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello-probe"}}]}`))
	}))
	t.Cleanup(up.Close)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	ob.httpClient = up.Client()
	put := connect.NewRequest(&onboardingv1.PutModelConfigRequest{BaseUrl: up.URL + "/v1", Secret: "slot-secret-key"})
	put.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(put)
	if _, err := ob.PutModelConfig(ctx, put); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YUFENG_MODEL_API_KEY", "env-must-not-be-used")
	test := connect.NewRequest(&onboardingv1.TestModelConnectivityRequest{})
	test.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(test)
	if _, err := ob.TestModelConnectivity(ctx, test); err != nil {
		t.Fatal(err)
	}
	got := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if got.GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_MODEL_LIVE {
		t.Fatalf("state=%s", got.GetState())
	}
	if seenAuth != "Bearer slot-secret-key" {
		t.Fatalf("brain must send slot secret, auth=%q", seenAuth)
	}
	if seenAuth == "Bearer env-must-not-be-used" || os.Getenv("YUFENG_MODEL_API_KEY") == "" {
		t.Fatal("must not send env key")
	}
	if !strings.HasSuffix(seenPath, "/chat/completions") {
		t.Fatalf("path=%s", seenPath)
	}
	if seenModel != kernel.DefaultChatModel {
		t.Fatalf("model=%s", seenModel)
	}

	up.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	failedProbe := bearerReq(h.adminTok, &onboardingv1.TestModelConnectivityRequest{})
	if _, err := ob.TestModelConnectivity(ctx, failedProbe); connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("failed probe want unavailable/failed_precondition, got %v", err)
	}
	failed := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if failed.GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_FAILED {
		t.Fatalf("state=%s", failed.GetState())
	}
	if strings.TrimSpace(failed.GetLastError()) == "" || failed.GetLastError() != strings.ToLower(failed.GetLastError()) {
		t.Fatalf("last_error=%q", failed.GetLastError())
	}
	if strings.HasSuffix(strings.TrimSpace(failed.GetLastError()), ".") {
		t.Fatal("error must not end with punctuation")
	}
}

func TestModelConnectivityDiscardsStaleProbe(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	started := make(chan struct{})
	release := make(chan struct{})
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		close(started)
		<-release
		return "stale-pong", nil
	}
	if _, err := ob.PutModelConfig(ctx, bearerReq(h.adminTok, &onboardingv1.PutModelConfigRequest{
		BaseUrl: "https://api.example.com/v1", Secret: "sk-first",
	})); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := ob.TestModelConnectivity(ctx, bearerReq(h.adminTok, &onboardingv1.TestModelConnectivityRequest{}))
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not start")
	}
	if _, err := ob.PutModelConfig(ctx, bearerReq(h.adminTok, &onboardingv1.PutModelConfigRequest{
		BaseUrl: "https://api.example.com/v1", Secret: "sk-rotated",
	})); err != nil {
		t.Fatal(err)
	}
	close(release)
	err := <-errCh
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale probe want aborted, got %v", err)
	}
	got := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if got.GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_MODEL_CONFIGURED {
		t.Fatalf("state=%s want MODEL_CONFIGURED", got.GetState())
	}
}

func TestCompleteChatFromSlot(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	var seenMax int
	var seenFmt string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer slot-secret-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			MaxTokens      int `json:"max_tokens"`
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		_ = json.Unmarshal(raw, &body)
		seenMax = body.MaxTokens
		seenFmt = body.ResponseFormat.Type
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"jarvis-reply"}}]}`))
	}))
	t.Cleanup(up.Close)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	ob.httpClient = up.Client()
	t.Setenv("YUFENG_MODEL_API_KEY", "env-must-not-be-used")
	empty := connect.NewRequest(&modelv1.CompleteChatRequest{Messages: []*modelv1.ChatMessage{{Role: "user", Content: "hi"}}})
	empty.Header().Set("Authorization", "Bearer "+h.agentTok)
	if _, err := ob.CompleteChat(ctx, empty); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("empty slot want failed_precondition, got %v", err)
	}
	put := connect.NewRequest(&onboardingv1.PutModelConfigRequest{BaseUrl: up.URL + "/v1", Secret: "slot-secret-key"})
	put.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(put)
	if _, err := ob.PutModelConfig(ctx, put); err != nil {
		t.Fatal(err)
	}
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return "ready", nil
	}
	if _, err := ob.TestModelConnectivity(ctx, bearerReq(h.adminTok, &onboardingv1.TestModelConnectivityRequest{})); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, "asset-egress"); err != nil {
		t.Fatal(err)
	}
	ob.completeFn = nil
	out, err := ob.CompleteChat(ctx, bearerReq(h.agentTok, &modelv1.CompleteChatRequest{
		Messages: []*modelv1.ChatMessage{{Role: "user", Content: "hi"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Msg.GetText() != "jarvis-reply" {
		t.Fatalf("text=%q", out.Msg.GetText())
	}
	if seenMax != kernel.ChatCompleteMaxTokens {
		t.Fatalf("max_tokens=%d want %d", seenMax, kernel.ChatCompleteMaxTokens)
	}
	if seenFmt != "json_object" {
		t.Fatalf("response_format=%q", seenFmt)
	}
	userReq := bearerReq(h.adminTok, &modelv1.CompleteChatRequest{Messages: []*modelv1.ChatMessage{{Role: "user", Content: "hi"}}})
	if _, err := ob.CompleteChat(ctx, userReq); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("user session want unauthenticated, got %v", err)
	}
}

func TestDeploymentSpecificationAndManualEdgeReadinessCompleteOnboarding(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ob.signingKey = signingKey
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return "ok", nil
	}
	if _, err := ob.PutModelConfig(ctx, bearerReq(h.adminTok, &onboardingv1.PutModelConfigRequest{
		BaseUrl: "https://api.example.com/v1", Secret: "sk-g2",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := ob.TestModelConnectivity(ctx, bearerReq(h.adminTok, &onboardingv1.TestModelConnectivityRequest{})); err != nil {
		t.Fatal(err)
	}
	unitID := "edge-manual-" + newTestSuffix()
	specification := &onboardingv1.PutDeploymentSpecificationRequest{
		UnitId: unitID, AssetId: h.local,
		Posture:    commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "manual-edge", ModelProfile: modelProfileSpecification(kernel.DefaultModelProfile()),
		Target: &onboardingv1.PutDeploymentSpecificationRequest_ReverseProxy{ReverseProxy: &onboardingv1.ReverseProxyTarget{
			ListenAddress: ":18080", UpstreamUrl: "http://app:8080",
		}},
	}
	firstSpecification, err := ob.PutDeploymentSpecification(ctx, bearerReq(h.adminTok, specification))
	if err != nil {
		t.Fatal(err)
	}
	secondSpecification, err := ob.PutDeploymentSpecification(ctx, bearerReq(h.adminTok, specification))
	if err != nil {
		t.Fatal(err)
	}
	if firstSpecification.Msg.GetGenerationId() == "" || firstSpecification.Msg.GetGenerationSeq() <= 0 ||
		firstSpecification.Msg.GetListenPlanVersion() == 0 ||
		firstSpecification.Msg.GetGenerationId() != secondSpecification.Msg.GetGenerationId() ||
		firstSpecification.Msg.GetListenPlanVersion() != secondSpecification.Msg.GetListenPlanVersion() {
		t.Fatalf("deterministic specification coordinates first=%v second=%v", firstSpecification.Msg, secondSpecification.Msg)
	}
	var deploymentInstructions int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE kind LIKE 'ONBOARDING%'`).Scan(&deploymentInstructions); err != nil {
		t.Fatal(err)
	}
	if deploymentInstructions != 0 {
		t.Fatal("deployment specification must not create a Jarvis instruction")
	}
	var modelSideUnitID, modelSideAssetID, modelSideCertificate string
	if err := st.Pool().QueryRow(ctx, `SELECT unit_id,asset_id,client_cert_sha256 FROM modelside_identities WHERE modelside_id=$1`, modelSideIDForUnit(unitID)).
		Scan(&modelSideUnitID, &modelSideAssetID, &modelSideCertificate); err != nil {
		t.Fatal(err)
	}
	if modelSideUnitID != unitID || modelSideAssetID != h.local || modelSideCertificate != "" {
		t.Fatalf("predeclared ModelSide identity unit=%q asset=%q certificate=%q", modelSideUnitID, modelSideAssetID, modelSideCertificate)
	}
	if err := touchJarvis(ctx, st, h.jarvisID); err != nil {
		t.Fatal(err)
	}
	extraAsset := "asset-x-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, extraAsset); err != nil {
		t.Fatal(err)
	}

	_, err = ob.CompleteOnboarding(ctx, bearerReq(h.adminTok, &onboardingv1.CompleteOnboardingRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("offline manual Edge must block completion: %v", err)
	}
	if !containsInt(onboardingGateOf(t, err).MissingPredicates, 3) {
		t.Fatalf("missing=%v want edge readiness", onboardingGateOf(t, err).MissingPredicates)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET last_heartbeat_at=now(), current_generation_id=$2,
		current_generation_seq=$3,current_listen_plan_version=$4 WHERE unit_id=$1`, unitID,
		firstSpecification.Msg.GetGenerationId(), firstSpecification.Msg.GetGenerationSeq(), firstSpecification.Msg.GetListenPlanVersion()); err != nil {
		t.Fatal(err)
	}
	if got := mustGetOnboarding(t, ctx, ob, h.adminTok); got.GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_EDGE_LIVE || !got.GetEdgeReady() {
		t.Fatalf("manual edge readiness projection=%v", got)
	}

	users := NewUserServer(st.Pool(), 8)
	opName := "g2op-" + newTestSuffix()
	op, err := users.CreateUser(ctx, bearerReq(h.adminTok, &userv1.CreateUserRequest{
		Username: opName, Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR,
	}))
	if err != nil {
		t.Fatal(err)
	}
	grants := NewGrantServer(st.Pool())
	if _, err := grants.PutGrant(ctx, bearerReq(h.adminTok, &grantv1.PutGrantRequest{
		SubjectUserId: op.Msg.User.UserId,
		Tools:         []string{"govern.promote_canary"},
		Bindings:      []*grantv1.BindingRef{{Kind: "asset", Id: h.local}},
	})); err != nil {
		t.Fatal(err)
	}
	_, err = ob.CompleteOnboarding(ctx, bearerReq(h.adminTok, &onboardingv1.CompleteOnboardingRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("canary-only want failed_precondition, got %v", err)
	}
	if !containsInt(onboardingGateOf(t, err).MissingPredicates, 4) {
		t.Fatalf("canary-only missing=%v", onboardingGateOf(t, err).MissingPredicates)
	}

	if _, err := grants.PutGrant(ctx, bearerReq(h.adminTok, &grantv1.PutGrantRequest{
		SubjectUserId: op.Msg.User.UserId,
		Tools:         []string{"govern.promote_enforce"},
		Bindings:      []*grantv1.BindingRef{{Kind: "asset", Id: h.local}},
	})); err != nil {
		t.Fatal(err)
	}
	var grantsBefore int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM grants WHERE subject_id=$1`, op.Msg.User.UserId).Scan(&grantsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := ob.CompleteOnboarding(ctx, bearerReq(h.adminTok, &onboardingv1.CompleteOnboardingRequest{})); err != nil {
		t.Fatal(err)
	}
	if mustOnboardingState(t, ctx, st) != OnboardingStateCompleted {
		t.Fatal("want COMPLETED")
	}
	var grantsAfter int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM grants WHERE subject_id=$1`, op.Msg.User.UserId).Scan(&grantsAfter); err != nil {
		t.Fatal(err)
	}
	if grantsAfter != grantsBefore {
		t.Fatalf("CompleteOnboarding must not rewrite other grants: before=%d after=%d", grantsBefore, grantsAfter)
	}

	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	adminMe, err := auth.GetMe(ctx, bearerReq(h.adminTok, &authv1.GetMeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(adminMe.Msg.Access.GetTools(), "grant.write", "user.admin", "catalog.manage", "console.read") {
		t.Fatalf("admin tools=%v", adminMe.Msg.Access.GetTools())
	}
	if hasAnyPrefix(adminMe.Msg.Access.GetTools(), "govern.") {
		t.Fatalf("system grant must not include govern.*: %v", adminMe.Msg.Access.GetTools())
	}
	if !bindingHas(adminMe.Msg.Access.GetBindings(), "asset", h.local) {
		t.Fatalf("admin bindings=%v want %s", adminMe.Msg.Access.GetBindings(), h.local)
	}
	if !bindingHas(adminMe.Msg.Access.GetBindings(), "asset", extraAsset) {
		t.Fatalf("admin bindings=%v want extra %s", adminMe.Msg.Access.GetBindings(), extraAsset)
	}
	opLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: opName, Password: "Operator123"}))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(opLogin.Msg.Access.GetTools(), "govern.promote_enforce") {
		t.Fatalf("operator tools=%v", opLogin.Msg.Access.GetTools())
	}
	if !bindingHas(opLogin.Msg.Access.GetBindings(), "asset", h.local) {
		t.Fatalf("operator bindings=%v", opLogin.Msg.Access.GetBindings())
	}
}

type onboardHarness struct {
	adminTok string
	agentTok string
	jarvisID string
	local    string
	adminID  string
}

func newOnboardHarness(t *testing.T, st *store.Store) onboardHarness {
	t.Helper()
	ctx := context.Background()
	admin := "obadm-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	jarvisID := "jarvis-ob-" + newTestSuffix()
	local := "asset-ob-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, local); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "ob-boot-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	if err := SeedAgentBootstrap(ctx, st.Pool(), jarvisID, boot); err != nil {
		t.Fatal(err)
	}
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: jarvisID, BootstrapToken: boot, AgentPublicKey: "ob-pub",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return onboardHarness{
		adminTok: login.Msg.Token, agentTok: reg.Msg.AccessToken,
		jarvisID: jarvisID, local: local, adminID: login.Msg.User.UserId,
	}
}

func mustGetOnboarding(t *testing.T, ctx context.Context, ob *OnboardingServer, tok string) *onboardingv1.GetOnboardingResponse {
	t.Helper()
	got, err := ob.GetOnboarding(ctx, bearerReq(tok, &onboardingv1.GetOnboardingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return got.Msg
}

func mustOnboardingState(t *testing.T, ctx context.Context, st *store.Store) string {
	t.Helper()
	var state string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM deployment_onboarding WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func onboardingGateOf(t *testing.T, err error) *onboardingv1.OnboardingGate {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want connect error, got %T %v", err, err)
	}
	details := ce.Details()
	if len(details) != 1 {
		t.Fatalf("details=%d want 1", len(details))
	}
	if !strings.Contains(details[0].Type(), "yufeng.onboarding.v1.OnboardingGate") {
		t.Fatalf("detail type=%s", details[0].Type())
	}
	msg, err := details[0].Value()
	if err != nil {
		t.Fatal(err)
	}
	gate, ok := msg.(*onboardingv1.OnboardingGate)
	if !ok {
		t.Fatalf("detail=%T", msg)
	}
	prev := int32(-1)
	for _, n := range gate.MissingPredicates {
		if n < 1 || n > 4 || n <= prev {
			t.Fatalf("missing_predicates=%v", gate.MissingPredicates)
		}
		prev = n
	}
	return gate
}

func bearerReq[T any](tok string, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+tok)
	setTestIdempotency(req)
	return req
}

func setTestIdempotency[T any](req *connect.Request[T]) {
	req.Header().Set("Idempotency-Key", "test-"+newTestSuffix())
}

func touchJarvis(ctx context.Context, st *store.Store, agentID string) error {
	_, err := st.Pool().Exec(ctx, `UPDATE agents SET last_heartbeat_at=now() WHERE agent_id=$1`, agentID)
	return err
}
