package modelgateway

import (
	"context"
	"encoding/json"
	"strings"
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

// FakeProvider 只在测试二进制中提供确定性模型剧本。
type FakeProvider struct {
	Demo bool
}

func (p FakeProvider) Complete(_ context.Context, req ChatRequest) (ChatResponse, error) {
	if req.JSONMode {
		content, err := fakeDefenseTurn(req.Messages, p.Demo)
		if err != nil {
			return ChatResponse{}, err
		}
		return ChatResponse{Content: content, InputTokens: 1, OutputTokens: 1}, nil
	}
	last := ""
	if len(req.Messages) > 0 {
		last = req.Messages[len(req.Messages)-1].Content
	}
	return ChatResponse{Content: last, InputTokens: 1, OutputTokens: 1}, nil
}

func fakeDefenseTurn(msgs []Message, demo bool) (string, error) {
	last := ""
	if len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	if strings.HasPrefix(last, "kind=SESSION_MESSAGE") {
		ref := payloadRefOf(last)
		raw, err := json.Marshal(map[string]any{"tool": "session.reply", "args": map[string]string{"session_id": ref, "content": "已收到"}})
		return string(raw), err
	}
	if strings.HasPrefix(last, "kind=EVENT_TRIAGE") {
		ref := payloadRefOf(last)
		tool, key := "cluster.get", "cluster_id"
		if demo {
			tool, key = "event.get", "event_id"
		}
		raw, err := json.Marshal(map[string]any{"tool": tool, "args": map[string]string{key: ref}})
		return string(raw), err
	}
	if after, ok := strings.CutPrefix(last, "tool_result="); ok {
		var body map[string]any
		if err := json.Unmarshal([]byte(after), &body); err != nil {
			return `{"done":true}`, nil
		}
		if _, ok := body["eventId"]; ok {
			asset, _ := body["assetId"].(string)
			return fakePropose(asset, demo)
		}
		if _, ok := body["clusterId"]; ok {
			asset, _ := body["assetId"].(string)
			return fakePropose(asset, demo)
		}
		releaseID, _ := body["releaseId"].(string)
		switch body["state"] {
		case "DRAFT":
			raw, err := json.Marshal(map[string]any{"tool": "govern.gate", "args": map[string]string{"release_id": releaseID}})
			return string(raw), err
		case "SIGNED":
			raw, err := json.Marshal(map[string]any{"tool": "govern.start_shadow", "args": map[string]string{"release_id": releaseID}})
			return string(raw), err
		}
	}
	return `{"done":true}`, nil
}

func fakePropose(assetID string, demo bool) (string, error) {
	if demo {
		raw, err := json.Marshal(map[string]any{"tool": "govern.propose", "args": map[string]any{
			"kind": "KIND_RULE", "payload_schema": "rules/v1", "payload": string(demoRepairRuleJSON()),
			"scope": map[string]any{"asset_ids": []string{assetID}}, "ttl": "86400s",
		}})
		return string(raw), err
	}
	raw, err := json.Marshal(map[string]any{"tool": "govern.propose", "args": map[string]any{
		"intent": map[string]any{"kind": "PROPOSAL_KIND_POLICY", "detection_keys": []map[string]any{{
			"detectorId": "crs", "ruleId": "942100", "targetLocation": "INSPECTION_SURFACE_QUERY",
		}}}, "scope": map[string]any{"asset_ids": []string{assetID}},
	}})
	return string(raw), err
}

func payloadRefOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "payload_ref="); ok {
			return after
		}
	}
	return ""
}
