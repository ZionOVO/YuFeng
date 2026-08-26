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
	modelv1 "yufeng/proto/gen/modelv1"
	onboardingv1 "yufeng/proto/gen/onboardingv1"
	"yufeng/proto/gen/onboardingv1/onboardingv1connect"
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
		configured  bool
		modelLive   bool
		wantIllegal bool
	}{
		{name: "pending put", from: OnboardingStatePending, action: actionPutModelConfig},
		{name: "pending complete", from: OnboardingStatePending, action: actionCompleteOnboarding},
		{name: "pending test no endpoint", from: OnboardingStatePending, action: actionTestModel, wantIllegal: true},
		{name: "pending specification", from: OnboardingStatePending, action: actionPutDeploymentSpec, wantIllegal: true},
		{name: "configured test", from: OnboardingStateModelConfigured, action: actionTestModel, configured: true},
		{name: "configured specification", from: OnboardingStateModelConfigured, action: actionPutDeploymentSpec, configured: true, wantIllegal: true},
		{name: "live specification", from: OnboardingStateModelLive, action: actionPutDeploymentSpec, configured: true, modelLive: true},
		{name: "edge live specification", from: OnboardingStateEdgeLive, action: actionPutDeploymentSpec, configured: true, modelLive: true},
		{name: "failed put", from: OnboardingStateFailed, action: actionPutModelConfig, configured: true},
		{name: "failed test", from: OnboardingStateFailed, action: actionTestModel, configured: true},
		{name: "failed specification without live", from: OnboardingStateFailed, action: actionPutDeploymentSpec, configured: true, wantIllegal: true},
		{name: "failed specification after live", from: OnboardingStateFailed, action: actionPutDeploymentSpec, configured: true, modelLive: true},
		{name: "completed put", from: OnboardingStateCompleted, action: actionPutModelConfig, wantIllegal: true},
		{name: "completed test", from: OnboardingStateCompleted, action: actionTestModel, configured: true, modelLive: true, wantIllegal: true},
		{name: "completed specification", from: OnboardingStateCompleted, action: actionPutDeploymentSpec, configured: true, modelLive: true, wantIllegal: true},
		{name: "completed complete", from: OnboardingStateCompleted, action: actionCompleteOnboarding, wantIllegal: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := onboardingEdgeError(tc.from, tc.action, tc.configured, tc.modelLive)
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
	bad := connect.NewRequest(&onboardingv1.PutModelConfigRequest{BaseUrl: "ftp://api.example.com/v1", Secret: "sk-x"})
	bad.Header().Set("Authorization", "Bearer "+h.adminTok)
	setTestIdempotency(bad)
	if _, err := ob.PutModelConfig(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ftp base_url want invalid_argument, got %v", err)
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

func TestPutModelConfigAllowsHTTPWithoutSecret(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)

	if _, err := ob.PutModelConfig(ctx, bearerReq(h.adminTok, &onboardingv1.PutModelConfigRequest{
		BaseUrl: "http://model.internal:8000/v1",
		Model:   "local-real-model",
	})); err != nil {
		t.Fatal(err)
	}
	configured := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if configured.GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_MODEL_CONFIGURED {
		t.Fatalf("state=%s", configured.GetState())
	}
	if configured.GetHasSecret() || configured.GetBaseUrl() != "http://model.internal:8000/v1" {
		t.Fatalf("projection=%v", configured)
	}
	var seenSecret string
	ob.completeFn = func(_ context.Context, _ string, secret string, _ string, _ []chatMessage) (string, error) {
		seenSecret = secret
		return "real-upstream-pong", nil
	}
	if _, err := ob.TestModelConnectivity(ctx, bearerReq(h.adminTok, &onboardingv1.TestModelConnectivityRequest{})); err != nil {
		t.Fatal(err)
	}
	if seenSecret != "" {
		t.Fatalf("secret=%q want empty", seenSecret)
	}
	if got := mustGetOnboarding(t, ctx, ob, h.adminTok); got.GetState() != onboardingv1.OnboardingState_ONBOARDING_STATE_MODEL_LIVE || got.GetHasSecret() {
		t.Fatalf("live projection=%v", got)
	}
}

func TestPutModelConfigRejectsSecretAndClearTogether(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)

	_, err := ob.PutModelConfig(ctx, bearerReq(h.adminTok, &onboardingv1.PutModelConfigRequest{
		BaseUrl:     "http://model.internal:8000/v1",
		Secret:      "key",
		ClearSecret: true,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("secret plus clear want invalid_argument, got %v", err)
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

func TestRetiredDeploymentSpecificationAndTwoPredicateCompletion(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
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
	if _, err := ob.PutDeploymentSpecification(ctx, bearerReq(h.adminTok, &onboardingv1.PutDeploymentSpecificationRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("retired deployment specification want unimplemented, got %v", err)
	}
	_, err := ob.CompleteOnboarding(ctx, bearerReq(h.adminTok, &onboardingv1.CompleteOnboardingRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("offline Jarvis must block completion: %v", err)
	}
	if gate := onboardingGateOf(t, err); len(gate.MissingPredicates) != 1 || gate.MissingPredicates[0] != 2 {
		t.Fatalf("missing=%v want only Jarvis", gate.MissingPredicates)
	}
	if err := touchJarvis(ctx, st, h.jarvisID); err != nil {
		t.Fatal(err)
	}
	if _, err := ob.CompleteOnboarding(ctx, bearerReq(h.adminTok, &onboardingv1.CompleteOnboardingRequest{})); err != nil {
		t.Fatal(err)
	}
	if mustOnboardingState(t, ctx, st) != OnboardingStateCompleted {
		t.Fatal("want COMPLETED")
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
	view := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if view.GetEdgeReady() || view.GetLocalUnitId() != "" || view.GetDeploymentSpecDigest() != "" {
		t.Fatalf("retired Edge onboarding fields must be empty: %v", view)
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
		if n < 1 || n > 2 || n <= prev {
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
