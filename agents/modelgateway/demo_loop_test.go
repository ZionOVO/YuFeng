package modelgateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFakeProviderEventTriagePlaybook(t *testing.T) {
	p := FakeProvider{Demo: true}
	msgs := []Message{{Role: "user", Content: "kind=EVENT_TRIAGE\npayload_ref=evt-1"}}
	resp, err := p.Complete(context.Background(), ChatRequest{JSONMode: true, Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &first); err != nil {
		t.Fatal(err)
	}
	if first.Tool != "event.get" {
		t.Fatalf("第一步应为 event.get，实际 %s", resp.Content)
	}

	msgs = append(msgs,
		Message{Role: "assistant", Content: resp.Content},
		Message{Role: "user", Content: `tool_result={"eventId":"evt-1","assetId":"asset-1","http":{"method":"GET","path":"/api/items","query":"id=1+UNION+SELECT+password"}}`},
	)
	resp, err = p.Complete(context.Background(), ChatRequest{JSONMode: true, Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &second); err != nil {
		t.Fatal(err)
	}
	if second.Tool != "govern.propose" {
		t.Fatalf("第二步应为 govern.propose，实际 %s", resp.Content)
	}
	if second.Args["payload"] != string(demoRepairRuleJSON()) {
		t.Fatalf("提案规则必须与测试夹具一致")
	}
}

func TestFakeProviderProductionWritesPolicyIntent(t *testing.T) {
	p := FakeProvider{}
	msgs := []Message{{Role: "user", Content: "kind=EVENT_TRIAGE\npayload_ref=clu-1"}}
	resp, err := p.Complete(context.Background(), ChatRequest{JSONMode: true, Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &first); err != nil {
		t.Fatal(err)
	}
	if first.Tool != "cluster.get" {
		t.Fatalf("生产第一步应为 cluster.get，实际 %s", resp.Content)
	}
	msgs = append(msgs,
		Message{Role: "assistant", Content: resp.Content},
		Message{Role: "user", Content: `tool_result={"clusterId":"clu-1","assetId":"asset-1"}`},
	)
	resp, err = p.Complete(context.Background(), ChatRequest{JSONMode: true, Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "PROPOSAL_KIND_POLICY") || strings.Contains(resp.Content, "KIND_RULE") {
		t.Fatalf("生产提案必须是检测键意图: %s", resp.Content)
	}
}
