package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CompleteChatProvider 调用中台的 ModelGatewayService/CompleteChat，契约见 docs/api.md 第 19.4 节。
// 贾维斯生产补全只走该远程过程调用，禁止使用 -model-url 参数。
type CompleteChatProvider struct {
	BaseURL    string
	Token      func() string
	HTTPClient *http.Client
}

// NewCompleteChatProvider 构造 CompleteChat 客户端。brainURL 与 PollInstructions 使用同一个中台根地址。
func NewCompleteChatProvider(brainURL string, token func() string, client *http.Client) *CompleteChatProvider {
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	return &CompleteChatProvider{BaseURL: strings.TrimRight(brainURL, "/"), Token: token, HTTPClient: client}
}

// Complete 向中台 CompleteChat 请求一段非空文本。
func (p *CompleteChatProvider) Complete(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var zero ChatResponse
	if p == nil || p.BaseURL == "" {
		return zero, errors.New("complete chat base url is empty")
	}
	body, err := json.Marshal(map[string]any{"messages": req.Messages})
	if err != nil {
		return zero, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/yufeng.model.v1.ModelGatewayService/CompleteChat", bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	if p.Token != nil {
		if tok := p.Token(); tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close() //nolint:errcheck // 响应体只读，返回前关闭失败没有可恢复动作。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	if err != nil {
		return zero, err
	}
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("complete chat status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Text    string `json:"text"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, err
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return zero, errors.New("complete chat returned empty text")
	}
	return ChatResponse{Content: text, InputTokens: 1, OutputTokens: 1}, nil
}
