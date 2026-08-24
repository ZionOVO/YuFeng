package modelgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteChatPostsBrainRPC(t *testing.T) {
	var gotPath, gotAuth, gotProto string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotProto = r.Header.Get("Connect-Protocol-Version")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"text":"ok-text"}`)
	}))
	t.Cleanup(srv.Close)
	p := NewCompleteChatProvider(srv.URL, func() string { return "agent-access" }, srv.Client())
	resp, err := p.Complete(context.Background(), ChatRequest{
		JSONMode: true,
		Messages: []Message{{Role: "user", Content: "kind=SESSION_MESSAGE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok-text" {
		t.Fatalf("text=%q", resp.Content)
	}
	if !strings.HasSuffix(gotPath, "/yufeng.model.v1.ModelGatewayService/CompleteChat") {
		t.Fatalf("path=%s", gotPath)
	}
	if gotAuth != "Bearer agent-access" {
		t.Fatalf("auth=%s", gotAuth)
	}
	if gotProto != "1" {
		t.Fatalf("connect proto=%s", gotProto)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("request body=%v", body)
	}
}

func TestCompleteChatEmptyTextFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":""}`)
	}))
	t.Cleanup(srv.Close)
	p := NewCompleteChatProvider(srv.URL, func() string { return "t" }, srv.Client())
	if _, err := p.Complete(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err == nil {
		t.Fatal("empty text must fail")
	}
}
