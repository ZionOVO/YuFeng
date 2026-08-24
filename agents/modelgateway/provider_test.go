package modelgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFakeProviderJSONMode(t *testing.T) {
	resp, err := (FakeProvider{}).Complete(context.Background(), ChatRequest{
		Model: "fake", JSONMode: true, Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, `"done"`) && !strings.Contains(resp.Content, `"tool"`) {
		t.Fatalf("假模型 JSON 输出异常: %s", resp.Content)
	}
}

func TestFakeProviderSanitizesUnprintable(t *testing.T) {
	resp, err := (FakeProvider{}).Complete(context.Background(), ChatRequest{
		Model: "fake", JSONMode: true, Messages: []Message{{Role: "user", Content: "a\nb\"c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 输出必须是可解析的 JavaScript 对象表示法：不可打印字符已被替换。
	var probe map[string]any
	if err := json.Unmarshal([]byte(resp.Content), &probe); err != nil {
		t.Fatalf("假模型输出不是合法 JSON: %v (%s)", err, resp.Content)
	}
}

func TestHTTPProviderJSONModeWireFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Errorf("路径异常: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("请求体解析失败: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "test-key")
	resp, err := p.Complete(context.Background(), ChatRequest{Model: "m", JSONMode: true, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_object" {
		t.Fatalf("JSONMode 应映射为 response_format.type=json_object，实际: %v", gotBody["response_format"])
	}
	if resp.Content != `{"ok":true}` || resp.InputTokens != 3 || resp.OutputTokens != 5 {
		t.Fatalf("响应解析异常: %+v", resp)
	}
}

func TestHTTPProviderNoJSONModeOmitsResponseFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"plain"}}]}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "")
	if _, err := p.Complete(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["response_format"]; present {
		t.Fatalf("未开 JSONMode 不应发送 response_format: %v", gotBody)
	}
}

func TestHTTPProviderStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "")
	if _, err := p.Complete(context.Background(), ChatRequest{Model: "m"}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("非 200 应返回含状态码的错误，实际: %v", err)
	}
}

func TestHTTPProviderEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "")
	if _, err := p.Complete(context.Background(), ChatRequest{Model: "m"}); err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("空 choices 应报错，实际: %v", err)
	}
}
