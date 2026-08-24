package brain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	modelv1 "yufeng/proto/gen/modelv1"
	onboardingv1 "yufeng/proto/gen/onboardingv1"
)

func TestGetModelGatewayRequiresCompletedAdmin(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)

	if _, err := ob.GetModelGateway(ctx, bearerReq(h.adminTok, &modelv1.GetModelGatewayRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("incomplete want failed_precondition, got %v", err)
	}
	seedCompletedSlot(t, ctx, h, ob, "https://api.x.ai/v1", "sk-admin-slot")

	if _, err := ob.GetModelGateway(ctx, bearerReq(h.agentTok, &modelv1.GetModelGatewayRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("agent want unauthenticated, got %v", err)
	}
	got, err := ob.GetModelGateway(ctx, bearerReq(h.adminTok, &modelv1.GetModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetStatus() != modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_READY {
		t.Fatalf("status=%s want READY", got.Msg.GetStatus())
	}
	if got.Msg.GetProviderCount() != 1 || got.Msg.GetWindowSeconds() != int64(kernel.ModelGatewayStatsWindow.Seconds()) {
		t.Fatalf("count=%d window=%d", got.Msg.GetProviderCount(), got.Msg.GetWindowSeconds())
	}
	if got.Msg.GetHasSecret() != true || strings.Contains(got.Msg.String(), "sk-admin-slot") {
		t.Fatalf("must project hint without plaintext: %v", got.Msg)
	}
}

func TestUpdateModelGatewayKeepsCompletedAndEmptySecret(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	seedCompletedSlot(t, ctx, h, ob, "https://api.x.ai/v1", "sk-keep-old")

	put := bearerReq(h.adminTok, &onboardingv1.PutModelConfigRequest{
		BaseUrl: "https://api.example.com/v1", Secret: "sk-illegal",
	})
	if _, err := ob.PutModelConfig(ctx, put); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("completed PutModelConfig want failed_precondition, got %v", err)
	}

	bad := bearerReq(h.adminTok, &modelv1.UpdateModelGatewayRequest{BaseUrl: "http://api.example.com/v1"})
	if _, err := ob.UpdateModelGateway(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("http want invalid_argument, got %v", err)
	}

	out, err := ob.UpdateModelGateway(ctx, bearerReq(h.adminTok, &modelv1.UpdateModelGatewayRequest{
		BaseUrl: "https://api.openai.com/v1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if mustOnboardingState(t, ctx, st) != OnboardingStateCompleted {
		t.Fatal("update must not change onboarding state")
	}
	if out.Msg.GetBaseUrl() != "https://api.openai.com/v1" {
		t.Fatalf("base_url=%s", out.Msg.GetBaseUrl())
	}
	if out.Msg.GetModel() != kernel.DefaultChatModel {
		t.Fatalf("empty model want default, got %s", out.Msg.GetModel())
	}
	if out.Msg.GetProviderCount() != 1 {
		t.Fatalf("current host must count, provider_count=%d", out.Msg.GetProviderCount())
	}
	if !out.Msg.GetHasSecret() || strings.Contains(out.Msg.GetSecretHint(), "sk-keep-old") {
		t.Fatalf("empty secret must keep old key, hint=%s", out.Msg.GetSecretHint())
	}

	rotated, err := ob.UpdateModelGateway(ctx, bearerReq(h.adminTok, &modelv1.UpdateModelGatewayRequest{
		BaseUrl: "https://api.openai.com/v1",
		Secret:  "sk-rotated-key",
		Model:   "grok-custom",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Msg.GetModel() != "grok-custom" {
		t.Fatalf("model=%s", rotated.Msg.GetModel())
	}
	if strings.Contains(rotated.Msg.String(), "sk-rotated-key") {
		t.Fatal("update must not echo secret")
	}
	if mustOnboardingState(t, ctx, st) != OnboardingStateCompleted {
		t.Fatal("rotate must not change state")
	}
}

func TestProbeModelGatewayRecordsWithoutStateChange(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	seedCompletedSlot(t, ctx, h, ob, "https://api.x.ai/v1", "sk-probe")
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return "pong", nil
	}
	ok, err := ob.ProbeModelGateway(ctx, bearerReq(h.adminTok, &modelv1.ProbeModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !ok.Msg.GetOk() {
		t.Fatal("probe want ok")
	}
	if mustOnboardingState(t, ctx, st) != OnboardingStateCompleted {
		t.Fatal("probe must not change state")
	}
	got, err := ob.GetModelGateway(ctx, bearerReq(h.adminTok, &modelv1.GetModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetCallsTotal() != 1 || got.Msg.GetCallsOk() != 1 {
		t.Fatalf("calls total=%d ok=%d", got.Msg.GetCallsTotal(), got.Msg.GetCallsOk())
	}
	if got.Msg.GetStatus() != modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_LIVE {
		t.Fatalf("status=%s", got.Msg.GetStatus())
	}
	if got.Msg.GetLastCallAt() == nil {
		t.Fatal("last_call_at required after probe")
	}

	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return "", errors.New("upstream refused")
	}
	if _, err := ob.ProbeModelGateway(ctx, bearerReq(h.adminTok, &modelv1.ProbeModelGatewayRequest{})); connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("failed probe want unavailable/failed_precondition, got %v", err)
	}
	if mustOnboardingState(t, ctx, st) != OnboardingStateCompleted {
		t.Fatal("failed probe must not write FAILED")
	}
	down, err := ob.GetModelGateway(ctx, bearerReq(h.adminTok, &modelv1.GetModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if down.Msg.GetStatus() != modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_DEGRADED {
		t.Fatalf("mixed window want DEGRADED, got %s", down.Msg.GetStatus())
	}
	if down.Msg.GetCallsTotal() != 2 || down.Msg.GetCallsOk() != 1 {
		t.Fatalf("after fail total=%d ok=%d", down.Msg.GetCallsTotal(), down.Msg.GetCallsOk())
	}
	if down.Msg.GetLastError() == "" || down.Msg.GetLastError() != strings.ToLower(down.Msg.GetLastError()) {
		t.Fatalf("last_error=%q", down.Msg.GetLastError())
	}
}

func TestCompleteChatRecordsCall(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	seedCompletedSlot(t, ctx, h, ob, "https://api.x.ai/v1", "sk-chat")
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return "jarvis-reply", nil
	}
	if _, err := ob.CompleteChat(ctx, bearerReq(h.agentTok, &modelv1.CompleteChatRequest{
		Messages: []*modelv1.ChatMessage{{Role: "user", Content: "hi"}},
	})); err != nil {
		t.Fatal(err)
	}
	got, err := ob.GetModelGateway(ctx, bearerReq(h.adminTok, &modelv1.GetModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetCallsTotal() != 1 || got.Msg.GetCallsOk() != 1 {
		t.Fatalf("complete must record, total=%d ok=%d", got.Msg.GetCallsTotal(), got.Msg.GetCallsOk())
	}
	if len(got.Msg.GetProviders()) != 1 || got.Msg.GetProviders()[0].GetHost() != "api.x.ai" {
		t.Fatalf("providers=%v", got.Msg.GetProviders())
	}
}

func seedCompletedSlot(t *testing.T, ctx context.Context, h onboardHarness, ob *OnboardingServer, baseURL, secret string) {
	t.Helper()
	// 走真实槽写入，再把引导行钉在 COMPLETED，避免 PutModelConfig 的非法边。
	if err := writeModelSecret(ctx, ob.pool, secret); err != nil {
		t.Fatal(err)
	}
	if err := writeOnboardingRow(ctx, ob.pool, OnboardingStateCompleted, h.local, baseURL, kernel.DefaultChatModel, "", true); err != nil {
		t.Fatal(err)
	}
}
