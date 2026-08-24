package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/agents/modelgateway"

	agentv1 "yufeng/proto/gen/agentv1"
	modelv1 "yufeng/proto/gen/modelv1"
)

const maxToolSteps = 8

// ToolCaller 调用中台工具网关。
type ToolCaller interface {
	Invoke(ctx context.Context, accessToken, capabilityToken, name, argsJSON string) (string, error)
}

// ModelCaller 由监督进程代持凭据调用统一模型网关。
type ModelCaller interface {
	Generate(ctx context.Context, accessToken, capabilityToken string, req *modelv1.GenerateRequest) (*modelv1.GenerateResponse, error)
}

type toolCall struct {
	Done bool
	Name string
	Args string
}

// Handle 处理一条指令：固定控制流按闭集推进，其余指令由模型网关选工具直到结束。
// 案件与显式演示研判不把确定性状态转换交给模型；生产研判只让模型提交类型化结论。
func Handle(ctx context.Context, provider modelgateway.Provider, tools ToolCaller, ins *agentv1.AgentInstruction, accessToken string) error {
	if provider == nil {
		return fmt.Errorf("model provider is nil")
	}
	if ins == nil {
		return fmt.Errorf("instruction is nil")
	}
	if ins.Kind == "CASE_REVIEW" {
		return handleCaseReview(ctx, tools, ins, accessToken)
	}
	if ins.Kind == "EVENT_TRIAGE" {
		if strings.TrimSpace(ins.GetTurnId()) != "" {
			return handleProductionTriage(ctx, provider, tools, ins, accessToken)
		}
		return handleDemoEventTriage(ctx, tools, ins, accessToken)
	}
	if ins.Kind == "SESSION_MESSAGE" {
		return handleSessionMessage(ctx, provider, tools, ins, accessToken)
	}
	msgs := []modelgateway.Message{
		{Role: "system", Content: jarvisSystemPrompt(ins.Kind)},
		{Role: "user", Content: fmt.Sprintf("kind=%s\npayload_ref=%s", ins.Kind, ins.PayloadRef)},
	}
	for step := 0; step < maxToolSteps; step++ {
		resp, err := completeInstructionModel(ctx, provider, ins, msgs, true)
		if err != nil {
			return fmt.Errorf("model: %w", err)
		}
		call, err := parseToolCall(resp.Content)
		if err != nil {
			return err
		}
		if call.Done {
			return nil
		}
		result, err := tools.Invoke(ctx, accessToken, ins.CapabilityToken, call.Name, call.Args)
		if err != nil {
			return fmt.Errorf("tool %s: %w", call.Name, err)
		}
		msgs = append(msgs,
			modelgateway.Message{Role: "assistant", Content: resp.Content},
			modelgateway.Message{Role: "user", Content: "tool_result=" + result},
		)
	}
	return fmt.Errorf("tool step budget exceeded")
}

// handleCaseReview 按案件状态推进固定的证据审批与短命调查控制流。
//
// Jarvis 只读取脱敏案件与敏感引用；证据正文只由短命调查经模型网关消费。
func handleCaseReview(ctx context.Context, tools ToolCaller, ins *agentv1.AgentInstruction, accessToken string) error {
	caseID := strings.TrimSpace(ins.GetPayloadRef())
	if caseID == "" {
		return fmt.Errorf("case review missing payload_ref")
	}
	caseArgs := mustJSON(map[string]string{"case_id": caseID})
	raw, err := tools.Invoke(ctx, accessToken, ins.GetCapabilityToken(), "case.get", caseArgs)
	if err != nil {
		return fmt.Errorf("tool case.get: %w", err)
	}
	state := strings.TrimPrefix(jsonStringField(raw, "state"), "INVESTIGATION_CASE_STATE_")
	switch strings.ToLower(state) {
	case "open":
		if _, err := tools.Invoke(ctx, accessToken, ins.GetCapabilityToken(), "case.request_evidence", caseArgs); err != nil {
			return fmt.Errorf("tool case.request_evidence: %w", err)
		}
		return nil
	case "queued":
		ref := jsonNestedStringField(raw, "sensitive_content_ref", "ref_id")
		if ref == "" {
			return fmt.Errorf("queued case is missing sensitive_content_ref")
		}
		args := mustJSON(map[string]string{"case_id": caseID, "sensitive_content_ref": ref})
		if _, err := tools.Invoke(ctx, accessToken, ins.GetCapabilityToken(), "run.create", args); err != nil {
			return fmt.Errorf("tool run.create: %w", err)
		}
		return nil
	case "waiting_evidence_approval", "investigating", "finding_ready", "shadow_observing", "resolved", "failed", "evidence_expired":
		return nil
	default:
		return fmt.Errorf("case review returned unsupported state %q", state)
	}
}

func jarvisSystemPrompt(kind string) string {
	base := "You are yufeng-jarvis. Reply with a single JSON object only: {\"tool\":\"...\",\"args\":{...}} or {\"done\":true}. No markdown, no XML, no <think>."
	switch kind {
	case "EVENT_TRIAGE":
		return base + " Production playbook: 1) cluster.get with cluster_id=payload_ref 2) govern.propose with intent.kind=PROPOSAL_KIND_POLICY and detectionKeys from the cluster or crs/942100/INSPECTION_SURFACE_QUERY; never KIND_RULE or rules/v1 3) govern.gate 4) govern.start_shadow 5) done."
	case "SESSION_MESSAGE":
		return base + " Call session.reply with session_id=payload_ref and a short reply, then done."
	default:
		return base
	}
}

func handleDemoEventTriage(ctx context.Context, tools ToolCaller, ins *agentv1.AgentInstruction, accessToken string) error {
	ref := strings.TrimSpace(ins.PayloadRef)
	if ref == "" {
		return fmt.Errorf("event triage missing payload_ref")
	}
	raw, err := tools.Invoke(ctx, accessToken, ins.CapabilityToken, "cluster.get", mustJSON(map[string]string{"cluster_id": ref}))
	if err != nil {
		raw, err = tools.Invoke(ctx, accessToken, ins.CapabilityToken, "event.get", mustJSON(map[string]string{"event_id": ref}))
		if err != nil {
			return fmt.Errorf("tool cluster.get/event.get: %w", err)
		}
	}
	assetID := jsonStringField(raw, "assetId", "asset_id")
	if assetID == "" {
		return fmt.Errorf("triage lookup missing asset id")
	}
	clusterID := jsonStringField(raw, "clusterId", "cluster_id")
	if clusterID == "" {
		return fmt.Errorf("triage lookup missing cluster id")
	}
	keys := productionPolicyKeys(raw)
	if len(keys) == 0 {
		return fmt.Errorf("triage cluster has no detection keys")
	}
	propose, err := json.Marshal(map[string]any{
		"intent": map[string]any{
			"kind":          "PROPOSAL_KIND_POLICY",
			"clusterId":     clusterID,
			"detectionKeys": keys,
		},
		"scope": map[string]any{"assetIds": []string{assetID}},
	})
	if err != nil {
		return err
	}
	out, err := tools.Invoke(ctx, accessToken, ins.CapabilityToken, "govern.propose", string(propose))
	if err != nil {
		return fmt.Errorf("tool govern.propose: %w", err)
	}
	rel := jsonStringField(out, "releaseId", "release_id")
	if rel == "" {
		return fmt.Errorf("govern.propose missing release id")
	}
	relArgs := mustJSON(map[string]string{"release_id": rel})
	gated, err := tools.Invoke(ctx, accessToken, ins.CapabilityToken, "govern.gate", relArgs)
	if err != nil {
		return fmt.Errorf("tool govern.gate: %w", err)
	}
	if jsonStringField(gated, "state") != "SIGNED" {
		return fmt.Errorf("govern.gate did not sign")
	}
	if _, err := tools.Invoke(ctx, accessToken, ins.CapabilityToken, "govern.start_shadow", relArgs); err != nil {
		return fmt.Errorf("tool govern.start_shadow: %w", err)
	}
	return nil
}

func handleProductionTriage(ctx context.Context, provider modelgateway.Provider, tools ToolCaller, ins *agentv1.AgentInstruction, accessToken string) error {
	turnID := strings.TrimSpace(ins.GetTurnId())
	if turnID == "" || strings.TrimSpace(ins.GetPayloadRef()) != turnID {
		return fmt.Errorf("event triage requires matching turn_id and payload_ref")
	}
	projection, err := tools.Invoke(ctx, accessToken, ins.GetCapabilityToken(), "triage.get", mustJSON(map[string]string{"turn_id": turnID}))
	if err != nil {
		return fmt.Errorf("tool triage.get: %w", err)
	}
	resp, err := completeInstructionModel(ctx, provider, ins, []modelgateway.Message{
		{Role: "system", Content: triageDecisionPrompt()},
		{Role: "user", Content: "turn_id=" + turnID + "\ntriage_projection=" + projection},
	}, true)
	if err != nil {
		return fmt.Errorf("model: %w", err)
	}
	decision, err := parseTriageDecision(resp.Content)
	if err != nil {
		return err
	}
	rawDecision, err := protojson.Marshal(decision)
	if err != nil {
		return err
	}
	var decisionObject map[string]any
	if err := json.Unmarshal(rawDecision, &decisionObject); err != nil {
		return err
	}
	args, err := json.Marshal(map[string]any{"turn_id": turnID, "decision": decisionObject})
	if err != nil {
		return err
	}
	if _, err := tools.Invoke(ctx, accessToken, ins.GetCapabilityToken(), "triage.complete", string(args)); err != nil {
		return fmt.Errorf("tool triage.complete: %w", err)
	}
	return nil
}

func triageDecisionPrompt() string {
	return "You are yufeng-jarvis. Return one JSON object matching TriageDecision only: " +
		"{\"clusterId\":\"...\",\"disposition\":\"TRIAGE_DISPOSITION_PROPOSE_POLICY|TRIAGE_DISPOSITION_PROPOSE_SHAPE|TRIAGE_DISPOSITION_REPORT_ONLY|TRIAGE_DISPOSITION_ESCALATE_HUMAN|TRIAGE_DISPOSITION_INSUFFICIENT_EVIDENCE\",\"rationale\":\"...\",\"optionalShapeDraft\":{...}}. " +
		"For TRIAGE_REASON_DETECTED_UNMITIGATED choose PROPOSE_POLICY. For TRIAGE_REASON_DETECTED_UNMAPPED choose REPORT_ONLY unless human review is required. For TRIAGE_REASON_SUSPECTED_MISS choose PROPOSE_SHAPE only when the projection supports a valid shape. " +
		"Never include detection keys, asset identifiers, target selectors, evidence references, created_by, scope risk, or evidence class. No markdown, XML, tool call, or extra fields."
}

func parseTriageDecision(raw string) (*agentv1.TriageDecision, error) {
	object := extractJSONObject(raw)
	var decision agentv1.TriageDecision
	if err := protojson.Unmarshal([]byte(object), &decision); err != nil {
		return nil, fmt.Errorf("triage decision: %w", err)
	}
	if strings.TrimSpace(decision.GetClusterId()) == "" {
		return nil, fmt.Errorf("triage decision cluster_id is required")
	}
	if decision.GetDisposition() == agentv1.TriageDisposition_TRIAGE_DISPOSITION_UNSPECIFIED {
		return nil, fmt.Errorf("triage decision disposition is required")
	}
	if strings.TrimSpace(decision.GetRationale()) == "" {
		return nil, fmt.Errorf("triage decision rationale is required")
	}
	return &decision, nil
}

// productionPolicyKeys 只转交聚类钉死版本中的检测键，不补造事件中不存在的事实。
func productionPolicyKeys(raw string) []map[string]any {
	return detectionKeysFromToolResult(raw)
}

func detectionKeysFromToolResult(raw string) []map[string]any {
	var body map[string]any
	if json.Unmarshal([]byte(raw), &body) != nil {
		return nil
	}
	if listed, _ := body["detectionKeys"].([]any); len(listed) > 0 {
		keys := make([]map[string]any, 0, len(listed))
		for _, item := range listed {
			key, _ := item.(map[string]any)
			if key != nil && strings.TrimSpace(jsonStringMap(key, "ruleId", "rule_id")) != "" {
				keys = append(keys, key)
			}
		}
		return keys
	}
	var keys []map[string]any
	dets, _ := body["detections"].([]any)
	for _, item := range dets {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if nested, _ := m["key"].(map[string]any); nested != nil {
			m = nested
		}
		rule := jsonStringMap(m, "ruleId", "rule_id")
		if strings.TrimSpace(rule) == "" {
			continue
		}
		keys = append(keys, map[string]any{
			"detectorId":     "crs",
			"ruleId":         rule,
			"targetLocation": "INSPECTION_SURFACE_QUERY",
		})
	}
	return keys
}

func jsonStringMap(v map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, _ := v[key].(string); value != "" {
			return value
		}
	}
	return ""
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func jsonStringField(raw string, keys ...string) string {
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := body[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func jsonNestedStringField(raw, objectKey, fieldKey string) string {
	var value map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &value) != nil {
		return ""
	}
	nestedRaw, ok := value[objectKey]
	if !ok {
		return ""
	}
	var nested map[string]any
	if json.Unmarshal(nestedRaw, &nested) != nil {
		return ""
	}
	field, _ := nested[fieldKey].(string)
	return strings.TrimSpace(field)
}

func handleSessionMessage(ctx context.Context, provider modelgateway.Provider, tools ToolCaller, ins *agentv1.AgentInstruction, accessToken string) error {
	ref := strings.TrimSpace(ins.GetSourceRef())
	if ref == "" {
		ref = strings.TrimSpace(ins.GetPayloadRef())
	}
	if ref == "" {
		return fmt.Errorf("session message missing payload_ref")
	}
	resp, err := completeInstructionModel(ctx, provider, ins, []modelgateway.Message{
		{Role: "system", Content: jarvisSystemPrompt(ins.Kind)},
		{Role: "user", Content: fmt.Sprintf("kind=%s\nsession_id=%s", ins.Kind, ref)},
	}, true)
	if err != nil {
		return fmt.Errorf("model: %w", err)
	}
	content := sessionReplyContent(resp.Content)
	if content == "" {
		return fmt.Errorf("session reply empty")
	}
	args, err := json.Marshal(map[string]string{"session_id": ref, "content": content})
	if err != nil {
		return err
	}
	if _, err := tools.Invoke(ctx, accessToken, ins.CapabilityToken, "session.reply", string(args)); err != nil {
		return fmt.Errorf("tool session.reply: %w", err)
	}
	return nil
}

func completeInstructionModel(ctx context.Context, provider modelgateway.Provider, ins *agentv1.AgentInstruction,
	messages []modelgateway.Message, jsonMode bool) (modelgateway.ChatResponse, error) {
	checkpoint := strings.TrimSpace(ins.GetCheckpointJson())
	if checkpoint != "" && checkpoint != "{}" {
		messages = append([]modelgateway.Message{{Role: "system", Content: "recovery_checkpoint=" + checkpoint}}, messages...)
	}
	req := modelgateway.ChatRequest{JSONMode: jsonMode, MaxTokens: 1024, Messages: messages}
	if strings.TrimSpace(ins.GetTurnId()) != "" {
		generationID := strings.TrimSpace(ins.GetResumeGenerationId())
		if generationID == "" {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", ins.GetTurnId(), ins.GetExpectedItemSequence())))
			generationID = "gen_" + hex.EncodeToString(sum[:12])
		}
		req.Turn = &modelgateway.TurnContext{
			ThreadID: ins.GetThreadId(), TurnID: ins.GetTurnId(), StepID: ins.GetStepId(),
			GenerationID: generationID, ExpectedItemSequence: ins.GetExpectedItemSequence(),
			LeaseID: ins.GetLeaseId(), LeaseEpoch: ins.GetLeaseEpoch(), CapabilityToken: ins.GetCapabilityToken(),
		}
	}
	resp, err := provider.Complete(ctx, req)
	if err == nil && resp.NextItemSequence > 0 {
		ins.ExpectedItemSequence = resp.NextItemSequence
		ins.ResumeGenerationId = ""
	}
	return resp, err
}

func sessionReplyContent(raw string) string {
	if call, err := parseToolCall(raw); err == nil && call.Name == "session.reply" {
		if c := jsonStringField(call.Args, "content"); c != "" {
			return c
		}
	}
	text := strings.TrimSpace(raw)
	if i := strings.Index(text, "{"); i >= 0 {
		if extracted := extractJSONObject(text); extracted != text {
			text = strings.TrimSpace(strings.Replace(text, extracted, "", 1))
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```JSON")
		raw = strings.TrimPrefix(raw, "```")
		if i := strings.LastIndex(raw, "```"); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
	}
	start := strings.Index(raw, "{")
	if start < 0 {
		return raw
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(raw[start : i+1])
			}
		}
	}
	if j := strings.LastIndex(raw, "}"); j > start {
		return strings.TrimSpace(raw[start : j+1])
	}
	return raw
}

func parseToolCall(raw string) (toolCall, error) {
	var zero toolCall
	var parsed struct {
		Done bool            `json:"done"`
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &parsed); err != nil {
		return zero, fmt.Errorf("model output is not json: %w", err)
	}
	if parsed.Done {
		return toolCall{Done: true}, nil
	}
	if strings.TrimSpace(parsed.Tool) == "" {
		return zero, fmt.Errorf("model output missing tool")
	}
	args := string(parsed.Args)
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	return toolCall{Name: parsed.Tool, Args: args}, nil
}
