package modelgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateProviderUsesPersistentGenerateRPC(t *testing.T) {
	var path, access, capability string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		access = r.Header.Get("Authorization")
		capability = r.Header.Get("X-Yufeng-Capability")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"generationId":"gen-1","acceptedAttemptId":"attempt-1","outputItems":[{"kind":"GENERATE_OUTPUT_KIND_TEXT","content":"ok"}],"usage":{"inputTokens":"2","outputTokens":"1"},"nextItemSequence":"4"}`))
	}))
	t.Cleanup(srv.Close)
	provider := NewGenerateProvider(srv.URL, func() string { return "agent-access" }, srv.Client())
	got, err := provider.Complete(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}}, JSONMode: true,
		Turn: &TurnContext{
			ThreadID: "thr-1", TurnID: "turn-1", StepID: "step-1", GenerationID: "gen-1",
			ExpectedItemSequence: 2, LeaseID: "lease-1", LeaseEpoch: 1, CapabilityToken: "capability",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/yufeng.model.v1.ModelGatewayService/Generate" {
		t.Fatalf("path=%q", path)
	}
	if access != "Bearer agent-access" || capability != "Bearer capability" {
		t.Fatalf("headers access=%q capability=%q", access, capability)
	}
	if got.Content != "ok" || got.AttemptID != "attempt-1" || got.NextItemSequence != 4 {
		t.Fatalf("response=%+v", got)
	}
}

func TestGenerateProviderNormalizesToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"generationId":"gen-1","acceptedAttemptId":"attempt-1","outputItems":[{"kind":"GENERATE_OUTPUT_KIND_TOOL_CALL","callId":"call-1","toolName":"session.reply","argumentsJson":"{\"session_id\":\"ses-1\",\"content\":\"ok\"}"}],"usage":{"inputTokens":"2","outputTokens":"1"},"nextItemSequence":"5"}`))
	}))
	t.Cleanup(srv.Close)
	provider := NewGenerateProvider(srv.URL, func() string { return "agent-access" }, srv.Client())
	got, err := provider.Complete(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Turn: &TurnContext{
			ThreadID: "thr-1", TurnID: "turn-1", StepID: "step-1", GenerationID: "gen-1",
			ExpectedItemSequence: 2, LeaseID: "lease-1", LeaseEpoch: 1, CapabilityToken: "capability",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolCallID != "call-1" || got.Content != `{"args":{"content":"ok","session_id":"ses-1"},"tool":"session.reply"}` {
		t.Fatalf("response=%+v", got)
	}
}
