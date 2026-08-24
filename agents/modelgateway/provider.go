package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// 超文本传输协议调用超时与错误体截读上限。
const (
	httpTimeout    = 60 * time.Second
	errorBodyLimit = 4096
)

// Message 是模型网关的通用消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 是对话请求。JSONMode 要求模型只输出 JavaScript 对象表示法（映射为
// OpenAI 协议的 response_format.type=json_object）。
type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	JSONMode    bool
	Turn        *TurnContext
}

// TurnContext 把一次补全绑定到中台持久 Turn、租约与账本序号。
type TurnContext struct {
	ThreadID             string
	TurnID               string
	StepID               string
	GenerationID         string
	ExpectedItemSequence int64
	LeaseID              string
	LeaseEpoch           int64
	AccessToken          string
	CapabilityToken      string
}

// chatWire 是线上格式：ChatRequest 是语义结构，两者分开，
// JSONMode 这类布尔开关才能映射成协议里的嵌套对象。
type chatWire struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

// ChatResponse 是模型返回结果。
type ChatResponse struct {
	Content          string
	ToolCallID       string
	InputTokens      int
	OutputTokens     int
	GenerationID     string
	AttemptID        string
	NextItemSequence int64
}

// Provider 是模型提供者接口。智能代理只依赖本接口，不感知 OpenAI、Python 或假模型。
type Provider interface {
	Complete(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// HTTPProvider 是兼容 OpenAI 接口的超文本传输协议提供者。
// Python 模型服务只要实现 /v1/chat/completions 兼容协议即可接入。
type HTTPProvider struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewHTTPProvider 构造 OpenAI 兼容提供者。
func NewHTTPProvider(baseURL, apiKey string) *HTTPProvider {
	return &HTTPProvider{BaseURL: baseURL, APIKey: apiKey, HTTPClient: &http.Client{Timeout: httpTimeout}}
}

// Complete 调用远程模型。
func (p *HTTPProvider) Complete(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var zero ChatResponse
	if p.BaseURL == "" {
		return zero, errors.New("model gateway base url is empty")
	}
	wire := chatWire{
		Model: req.Model, Messages: req.Messages,
		Temperature: req.Temperature, MaxTokens: req.MaxTokens,
	}
	if req.JSONMode {
		wire.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return zero, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close() //nolint:errcheck // 响应体只读，返回前关闭失败没有可恢复动作。
	if resp.StatusCode != http.StatusOK {
		// 错误体读失败时带空体报错即可：状态码本身就是拒因。
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return zero, fmt.Errorf("model gateway status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, err
	}
	if len(out.Choices) == 0 {
		return zero, errors.New("model gateway returned no choices")
	}
	return ChatResponse{Content: out.Choices[0].Message.Content, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}, nil
}
