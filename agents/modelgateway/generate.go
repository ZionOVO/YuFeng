package modelgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const generateResponseBodyLimit = 1 << 20

// GenerateProvider 调用中台的持久 Generate 接口，不直接连接模型供应商。
type GenerateProvider struct {
	BaseURL    string
	Token      func() string
	HTTPClient *http.Client
}

// NewGenerateProvider 构造统一智能代理座架模型客户端。
func NewGenerateProvider(brainURL string, token func() string, client *http.Client) *GenerateProvider {
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	return &GenerateProvider{BaseURL: strings.TrimRight(brainURL, "/"), Token: token, HTTPClient: client}
}

// Complete 把语义消息编码为 Generate；Turn、租约和序号缺失时失败关闭。
func (p *GenerateProvider) Complete(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var zero ChatResponse
	if p == nil || p.BaseURL == "" || req.Turn == nil {
		return zero, errors.New("generate turn context is required")
	}
	turn := req.Turn
	if turn.ThreadID == "" || turn.TurnID == "" || turn.StepID == "" || turn.GenerationID == "" ||
		turn.ExpectedItemSequence <= 0 || turn.LeaseID == "" || turn.LeaseEpoch <= 0 || turn.CapabilityToken == "" {
		return zero, errors.New("generate turn lease context is incomplete")
	}
	items := make([]map[string]any, 0, len(req.Messages))
	itemIDs := make([]string, 0, len(req.Messages))
	itemDigests := make([]string, 0, len(req.Messages))
	for i, message := range req.Messages {
		digest := messageDigest(message)
		itemID := fmt.Sprintf("input-%d", i+1)
		items = append(items, map[string]any{
			"itemId": itemID, "role": message.Role, "content": message.Content,
			"contentDigest": digest, "trustLevel": messageTrust(message.Role),
		})
		itemIDs = append(itemIDs, itemID)
		itemDigests = append(itemDigests, digest)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	body, err := json.Marshal(map[string]any{
		"threadId": turn.ThreadID, "turnId": turn.TurnID, "stepId": turn.StepID,
		"generationId": turn.GenerationID, "expectedItemSequence": turn.ExpectedItemSequence,
		"contextManifest": map[string]any{
			"itemIds": itemIDs, "itemDigests": itemDigests, "systemPromptVersion": "jarvis/v1",
			"roleProfileVersion": "orchestrator/v1", "toolCatalogVersion": "brain/v1",
			"modelSlotId": "onboarding/default", "adapterVersion": "generate/v1",
			"capabilityProjectionDigest": messageDigest(Message{Role: "capability", Content: turn.TurnID}),
		},
		"inputItems":       items,
		"generationLimits": map[string]any{"maxOutputTokens": maxTokens, "jsonMode": req.JSONMode},
		"leaseId":          turn.LeaseID, "leaseEpoch": turn.LeaseEpoch,
	})
	if err != nil {
		return zero, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.BaseURL+"/yufeng.model.v1.ModelGatewayService/Generate", bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	access := turn.AccessToken
	if p.Token != nil && p.Token() != "" {
		access = p.Token()
	}
	httpReq.Header.Set("Authorization", "Bearer "+access)
	httpReq.Header.Set("X-Yufeng-Capability", "Bearer "+turn.CapabilityToken)
	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close() //nolint:errcheck // 响应体只读，关闭失败没有可恢复动作。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, generateResponseBodyLimit))
	if err != nil {
		return zero, err
	}
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("generate status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		GenerationID      string `json:"generationId"`
		AcceptedAttemptID string `json:"acceptedAttemptId"`
		OutputItems       []struct {
			Kind          string `json:"kind"`
			Content       string `json:"content"`
			CallID        string `json:"callId"`
			ToolName      string `json:"toolName"`
			ArgumentsJSON string `json:"argumentsJson"`
		} `json:"outputItems"`
		Usage struct {
			InputTokens  string `json:"inputTokens"`
			OutputTokens string `json:"outputTokens"`
		} `json:"usage"`
		NextItemSequence string `json:"nextItemSequence"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, err
	}
	if len(out.OutputItems) == 0 {
		return zero, errors.New("generate returned no output items")
	}
	first := out.OutputItems[0]
	content := strings.TrimSpace(first.Content)
	if first.Kind == "GENERATE_OUTPUT_KIND_TOOL_CALL" {
		if strings.TrimSpace(first.CallID) == "" || strings.TrimSpace(first.ToolName) == "" || strings.TrimSpace(first.ArgumentsJSON) == "" {
			return zero, errors.New("generate returned incomplete tool call")
		}
		var args any
		if err := json.Unmarshal([]byte(first.ArgumentsJSON), &args); err != nil {
			return zero, errors.New("generate returned invalid tool arguments")
		}
		normalized, err := json.Marshal(map[string]any{"tool": first.ToolName, "args": args})
		if err != nil {
			return zero, err
		}
		content = string(normalized)
	}
	if content == "" {
		return zero, errors.New("generate returned empty output")
	}
	inputTokens, _ := strconv.Atoi(out.Usage.InputTokens)
	outputTokens, _ := strconv.Atoi(out.Usage.OutputTokens)
	nextSequence, _ := strconv.ParseInt(out.NextItemSequence, 10, 64)
	return ChatResponse{
		Content: content, ToolCallID: first.CallID, InputTokens: inputTokens, OutputTokens: outputTokens,
		GenerationID: out.GenerationID, AttemptID: out.AcceptedAttemptID, NextItemSequence: nextSequence,
	}, nil
}

func messageDigest(message Message) string {
	sum := sha256.Sum256([]byte(message.Role + "\x00" + message.Content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func messageTrust(role string) string {
	if strings.EqualFold(strings.TrimSpace(role), "system") {
		return "platform"
	}
	return "user"
}
