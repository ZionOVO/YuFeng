package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yufeng/lib/kernel"
	modelv1 "yufeng/proto/gen/modelv1"
	onboardingv1 "yufeng/proto/gen/onboardingv1"
)

func TestPostModelCompletionDialects(t *testing.T) {
	t.Parallel()
	msgs := []chatMessage{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hi"},
	}
	cases := []struct {
		name     string
		dialect  string
		jsonMode bool
		path     string
		auth     string
		claude   bool
		reply    string
		check    func(t *testing.T, h http.Header, body []byte)
	}{
		{
			name:    "openai chat",
			dialect: modelDialectOpenAIChat,
			path:    "/v1/chat/completions",
			auth:    "Bearer slot-secret",
			reply:   `{"choices":[{"message":{"content":"chat-ok"}}]}`,
			check: func(t *testing.T, h http.Header, body []byte) {
				t.Helper()
				if h.Get("x-api-key") != "" {
					t.Fatal("chat must not send x-api-key")
				}
				var wire struct {
					Messages []chatMessage `json:"messages"`
					Model    string        `json:"model"`
				}
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Model != "unit-model" || len(wire.Messages) != 2 || wire.Messages[0].Role != "system" {
					t.Fatalf("chat wire=%s", body)
				}
			},
		},
		{
			name:     "openai responses",
			dialect:  modelDialectOpenAIResponses,
			jsonMode: true,
			path:     "/v1/responses",
			auth:     "Bearer slot-secret",
			reply:    `{"output_text":"resp-ok"}`,
			check: func(t *testing.T, h http.Header, body []byte) {
				t.Helper()
				var wire struct {
					Instructions string        `json:"instructions"`
					Input        []chatMessage `json:"input"`
					Text         struct {
						Format struct {
							Type string `json:"type"`
						} `json:"format"`
					} `json:"text"`
				}
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Instructions != "be brief" || len(wire.Input) != 1 || wire.Input[0].Role != "user" {
					t.Fatalf("responses wire=%s", body)
				}
				if wire.Text.Format.Type != "json_object" {
					t.Fatalf("responses json mode=%s", body)
				}
			},
		},
		{
			name:    "claude messages",
			dialect: modelDialectClaudeMessages,
			path:    "/v1/messages",
			claude:  true,
			reply:   `{"content":[{"type":"text","text":"claude-ok"}]}`,
			check: func(t *testing.T, h http.Header, body []byte) {
				t.Helper()
				if h.Get("Authorization") != "" {
					t.Fatal("claude must not send bearer")
				}
				if h.Get("x-api-key") != "slot-secret" || h.Get("anthropic-version") != anthropicAPIVersion {
					t.Fatalf("claude headers=%v", h)
				}
				var wire struct {
					System   string        `json:"system"`
					Messages []chatMessage `json:"messages"`
				}
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.System != "be brief" || len(wire.Messages) != 1 || wire.Messages[0].Role != "user" {
					t.Fatalf("claude wire=%s", body)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seenPath, seenAuth string
			var seenBody []byte
			var seenHeader http.Header
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenPath = r.URL.Path
				seenAuth = r.Header.Get("Authorization")
				seenHeader = r.Header.Clone()
				seenBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.reply))
			}))
			t.Cleanup(up.Close)
			got, err := postModelCompletion(context.Background(), up.Client(), modelSlot{
				BaseURL: up.URL + "/v1",
				Secret:  "slot-secret",
				Model:   "unit-model",
				Dialect: tc.dialect,
			}, msgs, chatCompletionSpec{MaxTokens: 32, JSONMode: tc.jsonMode})
			if err != nil {
				t.Fatal(err)
			}
			wantText := map[string]string{
				modelDialectOpenAIChat:      "chat-ok",
				modelDialectOpenAIResponses: "resp-ok",
				modelDialectClaudeMessages:  "claude-ok",
			}[tc.dialect]
			if got != wantText {
				t.Fatalf("text=%q want %q", got, wantText)
			}
			if seenPath != tc.path {
				t.Fatalf("path=%s want %s", seenPath, tc.path)
			}
			if tc.claude {
				if seenAuth != "" {
					t.Fatalf("claude auth=%q", seenAuth)
				}
			} else if seenAuth != tc.auth {
				t.Fatalf("auth=%q want %q", seenAuth, tc.auth)
			}
			if tc.check != nil {
				tc.check(t, seenHeader, seenBody)
			}
		})
	}
}

func TestPostModelCompletionResponsesOutputBlocks(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"from-blocks"}]}]}`))
	}))
	t.Cleanup(up.Close)
	got, err := postModelCompletion(context.Background(), up.Client(), modelSlot{
		BaseURL: up.URL, Secret: "k", Model: "m", Dialect: modelDialectOpenAIResponses,
	}, []chatMessage{{Role: "user", Content: "q"}}, chatCompletionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-blocks" {
		t.Fatalf("text=%q", got)
	}
}

func TestPostModelCompletionUnknownDialect(t *testing.T) {
	t.Parallel()
	_, err := postModelCompletion(context.Background(), nil, modelSlot{
		BaseURL: "https://api.example.com/v1", Secret: "k", Model: "m", Dialect: "MODEL_DIALECT_NOPE",
	}, []chatMessage{{Role: "user", Content: "q"}}, chatCompletionSpec{})
	if err == nil || !strings.Contains(err.Error(), "unsupported model dialect") {
		t.Fatalf("err=%v", err)
	}
}

func TestPostModelCompletionClaudeRequiresUser(t *testing.T) {
	t.Parallel()
	_, err := postModelCompletion(context.Background(), http.DefaultClient, modelSlot{
		BaseURL: "https://api.example.com/v1", Secret: "k", Model: "m", Dialect: modelDialectClaudeMessages,
	}, []chatMessage{{Role: "system", Content: "only system"}}, chatCompletionSpec{})
	if err == nil || !strings.Contains(err.Error(), "require a user message") {
		t.Fatalf("err=%v", err)
	}
}

func TestPostModelCompletionNeverFollowsRedirects(t *testing.T) {
	redirectedCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"must-not-arrive"}}]}`))
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	_, err := postModelCompletion(context.Background(), redirect.Client(), modelSlot{
		BaseURL: redirect.URL, Secret: "k", Model: "m", Dialect: modelDialectOpenAIChat,
	}, []chatMessage{{Role: "user", Content: "q"}}, chatCompletionSpec{Sensitive: true, JSONMode: true})
	if err == nil || redirectedCalls != 0 {
		t.Fatalf("redirect error=%v redirected_calls=%d", err, redirectedCalls)
	}
}

func TestSensitiveCompletionDoesNotRetryJSONModeFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unsupported response format", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	_, err := postModelCompletion(context.Background(), server.Client(), modelSlot{
		BaseURL: server.URL, Secret: "k", Model: "m", Dialect: modelDialectOpenAIChat,
	}, []chatMessage{{Role: "user", Content: "q"}}, chatCompletionSpec{Sensitive: true, JSONMode: true})
	if err == nil || calls != 1 {
		t.Fatalf("error=%v calls=%d want one", err, calls)
	}
}

func TestCompleteChatForwardsSlotDialect(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	var seenPath, seenKey string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenKey = r.Header.Get("x-api-key")
		if r.Header.Get("Authorization") != "" {
			t.Errorf("complete chat claude sent bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"forwarded"}]}`))
	}))
	t.Cleanup(up.Close)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	ob.httpClient = up.Client()
	seedCompletedSlot(t, ctx, h, ob, up.URL+"/v1", "claude-slot")
	if err := writeOnboardingSlot(ctx, st.Pool(), up.URL+"/v1", kernel.DefaultChatModel, modelDialectClaudeMessages); err != nil {
		t.Fatal(err)
	}

	got, err := ob.GetModelGateway(ctx, bearerReq(h.adminTok, &modelv1.GetModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetDialect() != modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES {
		t.Fatalf("get dialect=%s", got.Msg.GetDialect())
	}

	out, err := ob.CompleteChat(ctx, bearerReq(h.agentTok, &modelv1.CompleteChatRequest{
		Messages: []*modelv1.ChatMessage{{Role: "user", Content: "hi"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Msg.GetText() != "forwarded" {
		t.Fatalf("text=%q", out.Msg.GetText())
	}
	if !strings.HasSuffix(seenPath, "/messages") {
		t.Fatalf("path=%s", seenPath)
	}
	if seenKey != "claude-slot" {
		t.Fatalf("x-api-key=%q", seenKey)
	}
}

func TestUpdateModelGatewayKeepsAndSetsDialect(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)
	seedCompletedSlot(t, ctx, h, ob, "https://api.x.ai/v1", "sk-keep")

	first, err := ob.GetModelGateway(ctx, bearerReq(h.adminTok, &modelv1.GetModelGatewayRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.GetDialect() != modelv1.ModelDialect_MODEL_DIALECT_OPENAI_CHAT {
		t.Fatalf("default dialect=%s", first.Msg.GetDialect())
	}

	set, err := ob.UpdateModelGateway(ctx, bearerReq(h.adminTok, &modelv1.UpdateModelGatewayRequest{
		BaseUrl: "https://api.anthropic.com/v1",
		Dialect: modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if set.Msg.GetDialect() != modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES {
		t.Fatalf("set dialect=%s", set.Msg.GetDialect())
	}

	keep, err := ob.UpdateModelGateway(ctx, bearerReq(h.adminTok, &modelv1.UpdateModelGatewayRequest{
		BaseUrl: "https://api.anthropic.com/v1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if keep.Msg.GetDialect() != modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES {
		t.Fatalf("keep dialect=%s", keep.Msg.GetDialect())
	}
}

func TestPutModelConfigWritesDialect(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	ob := NewOnboardingServer(st.Pool(), h.jarvisID)

	if _, err := ob.PutModelConfig(ctx, bearerReq(h.adminTok, &onboardingv1.PutModelConfigRequest{
		BaseUrl: "https://api.example.com/v1",
		Secret:  "sk-put",
		Dialect: modelv1.ModelDialect_MODEL_DIALECT_OPENAI_RESPONSES,
	})); err != nil {
		t.Fatal(err)
	}
	got := mustGetOnboarding(t, ctx, ob, h.adminTok)
	if got.GetDialect() != modelv1.ModelDialect_MODEL_DIALECT_OPENAI_RESPONSES {
		t.Fatalf("put dialect=%s", got.GetDialect())
	}
}
