package brain

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	modelv1 "yufeng/proto/gen/modelv1"
	onboardingv1 "yufeng/proto/gen/onboardingv1"
)

// TestOnboardingProtoSourcesMatchAPI19 锁住引导接口字段进 proto 源文件。
func TestOnboardingProtoSourcesMatchAPI19(t *testing.T) {
	onboard := readRepoFile(t, filepath.Join("proto", "yufeng", "onboarding", "v1", "onboarding.proto"))
	model := readRepoFile(t, filepath.Join("proto", "yufeng", "model", "v1", "model.proto"))

	for _, want := range []string{
		"package yufeng.onboarding.v1",
		"service OnboardingService",
		"rpc GetOnboarding",
		"rpc PutModelConfig",
		"rpc TestModelConnectivity",
		"rpc PutDeploymentSpecification",
		"rpc CompleteOnboarding",
		"message OnboardingGate",
		"missing_predicates",
		"enum OnboardingState",
		"ONBOARDING_STATE_PENDING",
		"ONBOARDING_STATE_MODEL_CONFIGURED",
		"ONBOARDING_STATE_MODEL_LIVE",
		"ONBOARDING_STATE_EDGE_LIVE",
		"ONBOARDING_STATE_COMPLETED",
		"ONBOARDING_STATE_FAILED",
		"string base_url",
		"string secret",
		"bool has_secret",
		"string secret_hint",
		"bool jarvis_online",
		"bool edge_ready",
		"string local_unit_id",
		"string expected_generation_id",
		"string local_asset_id",
		"string last_error",
		"ModelDialect dialect",
	} {
		if !strings.Contains(onboard, want) {
			t.Errorf("onboarding.proto missing %q", want)
		}
	}
	if strings.Contains(onboard, "message GetOnboardingResponse") &&
		strings.Contains(sectionBetween(onboard, "message GetOnboardingResponse", "message PutModelConfigRequest"), "string secret =") {
		t.Error("GetOnboardingResponse must not return secret plaintext")
	}

	for _, want := range []string{
		"package yufeng.model.v1",
		"service ModelGatewayService",
		"rpc CompleteChat",
		"rpc GetModelGateway",
		"rpc UpdateModelGateway",
		"rpc ProbeModelGateway",
		"MODEL_GATEWAY_STATUS_LIVE",
		"enum ModelDialect",
		"MODEL_DIALECT_OPENAI_CHAT",
		"MODEL_DIALECT_OPENAI_RESPONSES",
		"MODEL_DIALECT_CLAUDE_MESSAGES",
		"repeated ChatMessage messages",
		"string role",
		"string content",
		"string text",
	} {
		if !strings.Contains(model, want) {
			t.Errorf("model.proto missing %q", want)
		}
	}
}

func TestOnboardingGeneratedDescriptorsMatchAPI19(t *testing.T) {
	resp := (&onboardingv1.GetOnboardingResponse{}).ProtoReflect().Descriptor()
	for _, name := range []string{
		"state", "base_url", "model", "has_secret", "secret_hint",
		"jarvis_online", "dataplane_ready", "local_asset_id", "last_error", "updated_at",
		"dialect",
	} {
		if resp.Fields().ByName(protoreflect.Name(name)) == nil {
			t.Errorf("GetOnboardingResponse missing %s", name)
		}
	}
	if resp.Fields().ByName(protoreflect.Name("secret")) != nil {
		t.Error("GetOnboardingResponse must not have secret")
	}
	put := (&onboardingv1.PutModelConfigRequest{}).ProtoReflect().Descriptor()
	for _, name := range []string{"base_url", "secret", "model", "dialect"} {
		if put.Fields().ByName(protoreflect.Name(name)) == nil {
			t.Errorf("PutModelConfigRequest missing %s", name)
		}
	}
	gate := (&onboardingv1.OnboardingGate{}).ProtoReflect().Descriptor()
	if gate.Fields().ByName(protoreflect.Name("missing_predicates")) == nil {
		t.Error("OnboardingGate missing missing_predicates")
	}
	if onboardingv1.OnboardingState_ONBOARDING_STATE_PENDING.String() != "ONBOARDING_STATE_PENDING" {
		t.Fatalf("pending name=%s", onboardingv1.OnboardingState_ONBOARDING_STATE_PENDING)
	}
	chat := (&modelv1.CompleteChatRequest{}).ProtoReflect().Descriptor()
	if chat.Fields().ByName(protoreflect.Name("messages")) == nil {
		t.Fatal("CompleteChatRequest missing messages")
	}
	msg := (&modelv1.ChatMessage{}).ProtoReflect().Descriptor()
	if msg.Fields().ByName(protoreflect.Name("role")) == nil || msg.Fields().ByName(protoreflect.Name("content")) == nil {
		t.Fatal("ChatMessage missing role/content")
	}
	out := (&modelv1.CompleteChatResponse{}).ProtoReflect().Descriptor()
	if out.Fields().ByName(protoreflect.Name("text")) == nil {
		t.Fatal("CompleteChatResponse missing text")
	}
	gw := (&modelv1.GetModelGatewayResponse{}).ProtoReflect().Descriptor()
	for _, name := range []string{
		"base_url", "model", "has_secret", "secret_hint", "status",
		"provider_count", "window_seconds", "calls_total", "calls_ok",
		"last_call_at", "last_error", "providers", "dialect",
	} {
		if gw.Fields().ByName(protoreflect.Name(name)) == nil {
			t.Errorf("GetModelGatewayResponse missing %s", name)
		}
	}
	if gw.Fields().ByName(protoreflect.Name("secret")) != nil {
		t.Error("GetModelGatewayResponse must not have secret")
	}
	if modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_LIVE.String() != "MODEL_GATEWAY_STATUS_LIVE" {
		t.Fatalf("live name=%s", modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_LIVE)
	}
	if modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES.String() != "MODEL_DIALECT_CLAUDE_MESSAGES" {
		t.Fatalf("claude dialect name=%s", modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES)
	}
	upd := (&modelv1.UpdateModelGatewayRequest{}).ProtoReflect().Descriptor()
	if upd.Fields().ByName(protoreflect.Name("dialect")) == nil {
		t.Fatal("UpdateModelGatewayRequest missing dialect")
	}
	updOut := (&modelv1.UpdateModelGatewayResponse{}).ProtoReflect().Descriptor()
	for _, name := range []string{"base_url", "model", "dialect", "has_secret", "status"} {
		if updOut.Fields().ByName(protoreflect.Name(name)) == nil {
			t.Errorf("UpdateModelGatewayResponse missing %s", name)
		}
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func sectionBetween(text, start, end string) string {
	i := strings.Index(text, start)
	if i < 0 {
		return ""
	}
	rest := text[i:]
	if j := strings.Index(rest[len(start):], end); j >= 0 {
		return rest[:len(start)+j]
	}
	return rest
}
