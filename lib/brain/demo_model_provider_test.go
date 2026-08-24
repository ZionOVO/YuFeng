package brain

import (
	"context"
	"encoding/json"
	"strings"

	"yufeng/agents/modelgateway"
)

const (
	demoAttackMethod = "GET"
	demoAttackPath   = "/api/items"
	demoAttackQuery  = "id=1+UNION+SELECT+password"
)

func demoRepairRuleJSON() []byte {
	raw, err := json.Marshal([]map[string]string{
		{"id": "sql-union", "pattern": `(?i)union\s+select`},
		{"id": "xss-script", "pattern": `(?i)<script`},
		{"id": "path-traversal", "pattern": `\.\./`},
	})
	if err != nil {
		return []byte("[]")
	}
	return raw
}

type scriptedDemoProvider struct{}

func (scriptedDemoProvider) Complete(_ context.Context, request modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	last := ""
	if len(request.Messages) > 0 {
		last = request.Messages[len(request.Messages)-1].Content
	}
	var response any = map[string]any{"done": true}
	if strings.HasPrefix(last, "kind=EVENT_TRIAGE") {
		response = map[string]any{"tool": "event.get", "args": map[string]string{"event_id": testPayloadRef(last)}}
	} else if after, ok := strings.CutPrefix(last, "tool_result="); ok {
		var result map[string]any
		if err := json.Unmarshal([]byte(after), &result); err != nil {
			return modelgateway.ChatResponse{}, err
		}
		if _, ok := result["eventId"]; ok {
			assetID, _ := result["assetId"].(string)
			response = map[string]any{"tool": "govern.propose", "args": map[string]any{
				"kind": "KIND_RULE", "payload_schema": "rules/v1", "payload": string(demoRepairRuleJSON()),
				"scope": map[string]any{"asset_ids": []string{assetID}}, "ttl": "86400s",
			}}
		} else {
			releaseID, _ := result["releaseId"].(string)
			switch result["state"] {
			case "DRAFT":
				response = map[string]any{"tool": "govern.gate", "args": map[string]string{"release_id": releaseID}}
			case "SIGNED":
				response = map[string]any{"tool": "govern.start_shadow", "args": map[string]string{"release_id": releaseID}}
			}
		}
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return modelgateway.ChatResponse{}, err
	}
	return modelgateway.ChatResponse{Content: string(raw), InputTokens: 1, OutputTokens: 1}, nil
}

func testPayloadRef(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "payload_ref="); ok {
			return value
		}
	}
	return ""
}
