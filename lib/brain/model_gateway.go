package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"yufeng/lib/kernel"
	modelv1 "yufeng/proto/gen/modelv1"
)

// 槽上方言存 proto 枚举全名；空列与 UNSPECIFIED 按 OpenAI Chat 解释。
//
// [模型方言]: ../../docs/glossary.md#model-dialect
const (
	modelDialectOpenAIChat      = "MODEL_DIALECT_OPENAI_CHAT"
	modelDialectOpenAIResponses = "MODEL_DIALECT_OPENAI_RESPONSES"
	modelDialectClaudeMessages  = "MODEL_DIALECT_CLAUDE_MESSAGES"

	anthropicAPIVersion    = "2023-06-01"
	modelResponseBodyLimit = 256 << 10
)

// chatMessage 是内部补全消息，出网时按方言映射。
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model          string           `json:"model"`
	Messages       []chatMessage    `json:"messages"`
	MaxTokens      int              `json:"max_tokens,omitempty"`
	ResponseFormat *chatResponseFmt `json:"response_format,omitempty"`
}

type chatResponseFmt struct {
	Type string `json:"type"`
}

type chatCompletionSpec struct {
	MaxTokens int
	JSONMode  bool
	Sensitive bool
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// modelSlot 是出网时用的一条凭据槽投影。
type modelSlot struct {
	BaseURL string
	Secret  string
	Model   string
	Dialect string
}

type responsesRequest struct {
	Model           string            `json:"model"`
	Input           []chatMessage     `json:"input"`
	Instructions    string            `json:"instructions,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Text            *responsesTextFmt `json:"text,omitempty"`
}

type responsesTextFmt struct {
	Format *chatResponseFmt `json:"format,omitempty"`
}

type claudeRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []chatMessage `json:"messages"`
}

// normalizeModelDialect 把空值与 UNSPECIFIED 收成 OpenAI Chat；未知方言报错。
func normalizeModelDialect(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", "MODEL_DIALECT_UNSPECIFIED":
		return modelDialectOpenAIChat, nil
	case modelDialectOpenAIChat, modelDialectOpenAIResponses, modelDialectClaudeMessages:
		return strings.TrimSpace(raw), nil
	default:
		return "", errors.New("unsupported model dialect")
	}
}

func dialectFromProto(d modelv1.ModelDialect) (string, error) {
	switch d {
	case modelv1.ModelDialect_MODEL_DIALECT_UNSPECIFIED:
		return "", nil
	case modelv1.ModelDialect_MODEL_DIALECT_OPENAI_CHAT:
		return modelDialectOpenAIChat, nil
	case modelv1.ModelDialect_MODEL_DIALECT_OPENAI_RESPONSES:
		return modelDialectOpenAIResponses, nil
	case modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES:
		return modelDialectClaudeMessages, nil
	default:
		return "", errors.New("unsupported model dialect")
	}
}

func protoModelDialect(raw string) modelv1.ModelDialect {
	n, err := normalizeModelDialect(raw)
	if err != nil {
		return modelv1.ModelDialect_MODEL_DIALECT_UNSPECIFIED
	}
	switch n {
	case modelDialectOpenAIResponses:
		return modelv1.ModelDialect_MODEL_DIALECT_OPENAI_RESPONSES
	case modelDialectClaudeMessages:
		return modelv1.ModelDialect_MODEL_DIALECT_CLAUDE_MESSAGES
	default:
		return modelv1.ModelDialect_MODEL_DIALECT_OPENAI_CHAT
	}
}

func slotFromView(view onboardingView, fallbackModel string) modelSlot {
	model := strings.TrimSpace(view.Model)
	if model == "" {
		model = fallbackModel
	}
	return modelSlot{
		BaseURL: view.BaseURL,
		Secret:  view.SecretPlain,
		Model:   model,
		Dialect: view.Dialect,
	}
}

// postModelCompletion 按槽方言把内部消息转发到供应商端点，只回收非空文本。
func postModelCompletion(ctx context.Context, client *http.Client, slot modelSlot, messages []chatMessage, spec chatCompletionSpec) (string, error) {
	dialect, err := normalizeModelDialect(slot.Dialect)
	if err != nil {
		return "", err
	}
	slot.Dialect = dialect
	switch dialect {
	case modelDialectOpenAIResponses:
		return postOpenAIResponses(ctx, client, slot, messages, spec)
	case modelDialectClaudeMessages:
		return postClaudeMessages(ctx, client, slot, messages, spec)
	default:
		return postChatCompletion(ctx, client, slot.BaseURL, slot.Secret, slot.Model, messages, spec)
	}
}

// postChatCompletion 由中台出网调用 OpenAI Chat Completions，不读环境变量密钥。
func postChatCompletion(ctx context.Context, client *http.Client, baseURL, secret, model string, messages []chatMessage, spec chatCompletionSpec) (string, error) {
	text, err := doChatCompletion(ctx, client, baseURL, secret, model, messages, spec)
	if err != nil && spec.JSONMode && !spec.Sensitive && strings.Contains(err.Error(), "status 4") {
		spec.JSONMode = false
		return doChatCompletion(ctx, client, baseURL, secret, model, messages, spec)
	}
	return text, err
}

func doChatCompletion(ctx context.Context, client *http.Client, baseURL, secret, model string, messages []chatMessage, spec chatCompletionSpec) (string, error) {
	slot := modelSlot{BaseURL: baseURL, Secret: secret, Model: model}
	if err := prepareModelSlot(&slot); err != nil {
		return "", err
	}
	maxTok := specMaxTokens(spec)
	wire := chatCompletionRequest{Model: slot.Model, Messages: messages, MaxTokens: maxTok}
	if spec.JSONMode {
		wire.ResponseFormat = &chatResponseFmt{Type: "json_object"}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	raw, err := postModelJSON(ctx, client, joinModelPath(slot.BaseURL, "/chat/completions"), slot.Secret, false, body)
	if err != nil {
		return "", err
	}
	var out chatCompletionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("model endpoint returned invalid json")
	}
	if len(out.Choices) == 0 {
		return "", errors.New("model endpoint returned empty text")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("model endpoint returned empty text")
	}
	return text, nil
}

func postOpenAIResponses(ctx context.Context, client *http.Client, slot modelSlot, messages []chatMessage, spec chatCompletionSpec) (string, error) {
	text, err := doOpenAIResponses(ctx, client, slot, messages, spec)
	if err != nil && spec.JSONMode && !spec.Sensitive && strings.Contains(err.Error(), "status 4") {
		spec.JSONMode = false
		return doOpenAIResponses(ctx, client, slot, messages, spec)
	}
	return text, err
}

func doOpenAIResponses(ctx context.Context, client *http.Client, slot modelSlot, messages []chatMessage, spec chatCompletionSpec) (string, error) {
	if err := prepareModelSlot(&slot); err != nil {
		return "", err
	}
	system, rest := splitSystemMessages(messages)
	wire := responsesRequest{
		Model:           slot.Model,
		Input:           rest,
		Instructions:    system,
		MaxOutputTokens: specMaxTokens(spec),
	}
	if spec.JSONMode {
		wire.Text = &responsesTextFmt{Format: &chatResponseFmt{Type: "json_object"}}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	raw, err := postModelJSON(ctx, client, joinModelPath(slot.BaseURL, "/responses"), slot.Secret, false, body)
	if err != nil {
		return "", err
	}
	return textFromResponses(raw)
}

func postClaudeMessages(ctx context.Context, client *http.Client, slot modelSlot, messages []chatMessage, spec chatCompletionSpec) (string, error) {
	if err := prepareModelSlot(&slot); err != nil {
		return "", err
	}
	system, rest := splitSystemMessages(messages)
	if err := validateClaudeMessages(rest); err != nil {
		return "", err
	}
	wire := claudeRequest{
		Model:     slot.Model,
		MaxTokens: specMaxTokens(spec),
		System:    system,
		Messages:  rest,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	raw, err := postModelJSON(ctx, client, joinModelPath(slot.BaseURL, "/messages"), slot.Secret, true, body)
	if err != nil {
		return "", err
	}
	return textFromClaude(raw)
}

func prepareModelSlot(slot *modelSlot) error {
	if slot == nil {
		return errors.New("model slot is nil")
	}
	if strings.TrimSpace(slot.BaseURL) == "" {
		return errors.New("model base_url is empty")
	}
	if strings.TrimSpace(slot.Model) == "" {
		slot.Model = kernel.DefaultChatModel
	}
	return nil
}

func specMaxTokens(spec chatCompletionSpec) int {
	if spec.MaxTokens > 0 {
		return spec.MaxTokens
	}
	return kernel.ChatProbeMaxTokens
}

func joinModelPath(baseURL, suffix string) string {
	return strings.TrimRight(baseURL, "/") + suffix
}

func splitSystemMessages(messages []chatMessage) (string, []chatMessage) {
	var sys []string
	rest := make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		if strings.EqualFold(strings.TrimSpace(m.Role), "system") {
			if strings.TrimSpace(m.Content) != "" {
				sys = append(sys, m.Content)
			}
			continue
		}
		rest = append(rest, m)
	}
	return strings.Join(sys, "\n\n"), rest
}

func validateClaudeMessages(messages []chatMessage) error {
	hasUser := false
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "user":
			hasUser = true
		case "assistant":
		default:
			return errors.New("claude messages reject role " + role)
		}
	}
	if !hasUser {
		return errors.New("claude messages require a user message")
	}
	return nil
}

func postModelJSON(ctx context.Context, client *http.Client, endpoint, secret string, claude bool, body []byte) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: kernel.ChatCompleteTimeout}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if claude {
		if strings.TrimSpace(secret) != "" {
			req.Header.Set("x-api-key", secret)
		}
		req.Header.Set("anthropic-version", anthropicAPIVersion)
	} else if strings.TrimSpace(secret) != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := clientCopy.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model endpoint unreachable: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 模型响应体只读，返回前关闭失败没有可恢复动作。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, modelResponseBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > modelResponseBodyLimit {
		return nil, errors.New("model endpoint response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("model endpoint status %d", resp.StatusCode)
	}
	return raw, nil
}

func textFromResponses(raw []byte) (string, error) {
	var out struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("model endpoint returned invalid json")
	}
	if text := strings.TrimSpace(out.OutputText); text != "" {
		return text, nil
	}
	var b strings.Builder
	for _, item := range out.Output {
		for _, c := range item.Content {
			if c.Type == "" || c.Type == "output_text" || c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return "", errors.New("model endpoint returned empty text")
	}
	return text, nil
}

func textFromClaude(raw []byte) (string, error) {
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("model endpoint returned invalid json")
	}
	var b strings.Builder
	for _, c := range out.Content {
		if c.Type == "" || c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return "", errors.New("model endpoint returned empty text")
	}
	return text, nil
}
