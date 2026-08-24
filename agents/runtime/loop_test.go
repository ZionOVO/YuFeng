package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yufeng/agents/modelgateway"

	agentv1 "yufeng/proto/gen/agentv1"
)

type scriptCaller struct {
	calls []string
	fn    func(name, args string) (string, error)
}

func (s *scriptCaller) Invoke(_ context.Context, _, _, name, args string) (string, error) {
	s.calls = append(s.calls, name)
	return s.fn(name, args)
}

func TestHandleEventTriageUsesClosedPlaybook(t *testing.T) {
	caller := &scriptCaller{fn: func(name, args string) (string, error) {
		switch name {
		case "cluster.get":
			return `{"clusterId":"clu-1","assetId":"asset-1","detectionKeys":[{"detectorId":"crs","ruleId":"942100","targetLocation":"INSPECTION_SURFACE_QUERY"}]}`, nil
		case "govern.propose":
			if strings.Contains(args, "KIND_RULE") || strings.Contains(args, "sql-union") {
				return "", errors.New("demo rule leak")
			}
			if !strings.Contains(args, "PROPOSAL_KIND_POLICY") || !strings.Contains(args, "942100") || !strings.Contains(args, "clu-1") {
				return "", errors.New("want policy intent pinned to the cluster")
			}
			return `{"releaseId":"rel-1","state":"DRAFT"}`, nil
		case "govern.gate":
			return `{"releaseId":"rel-1","state":"SIGNED"}`, nil
		case "govern.start_shadow":
			return `{"releaseId":"rel-1","state":"SHADOW"}`, nil
		default:
			return "", errors.New("unexpected " + name)
		}
	}}
	err := Handle(context.Background(), badJSONProvider{}, caller, &agentv1.AgentInstruction{
		Kind: "EVENT_TRIAGE", PayloadRef: "clu-1", CapabilityToken: "tok",
	}, "access")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cluster.get", "govern.propose", "govern.gate", "govern.start_shadow"}
	if strings.Join(caller.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("工具顺序 %v want %v", caller.calls, want)
	}
}

func TestHandleCaseReviewRequestsEvidenceWithoutCallingModel(t *testing.T) {
	caller := &scriptCaller{fn: func(name, args string) (string, error) {
		if !strings.Contains(args, `"case_id":"case-open"`) {
			return "", errors.New("missing case binding")
		}
		switch name {
		case "case.get":
			return `{"case_id":"case-open","state":"INVESTIGATION_CASE_STATE_OPEN"}`, nil
		case "case.request_evidence":
			return `{"approval_id":"approval-1","state":"pending"}`, nil
		default:
			return "", errors.New("unexpected " + name)
		}
	}}
	err := Handle(context.Background(), badJSONProvider{}, caller, &agentv1.AgentInstruction{
		Kind: "CASE_REVIEW", PayloadRef: "case-open", CapabilityToken: "tok",
	}, "access")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(caller.calls, ","); got != "case.get,case.request_evidence" {
		t.Fatalf("case review tools=%s", got)
	}
}

func TestHandleQueuedCaseReviewCreatesSensitiveRunWithoutCallingModel(t *testing.T) {
	caller := &scriptCaller{fn: func(name, args string) (string, error) {
		switch name {
		case "case.get":
			return `{"case_id":"case-queued","state":"INVESTIGATION_CASE_STATE_QUEUED","sensitive_content_ref":{"ref_id":"sensitive-1"}}`, nil
		case "run.create":
			if !strings.Contains(args, `"case_id":"case-queued"`) || !strings.Contains(args, `"sensitive_content_ref":"sensitive-1"`) {
				return "", errors.New("run binding is incomplete")
			}
			return `{"run_id":"run-1","created":true}`, nil
		default:
			return "", errors.New("unexpected " + name)
		}
	}}
	err := Handle(context.Background(), badJSONProvider{}, caller, &agentv1.AgentInstruction{
		Kind: "CASE_REVIEW", PayloadRef: "case-queued", CapabilityToken: "tok",
	}, "access")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(caller.calls, ","); got != "case.get,run.create" {
		t.Fatalf("case review tools=%s", got)
	}
}

func TestHandleQueuedCaseReviewFailsClosedWithoutSensitiveReference(t *testing.T) {
	caller := &scriptCaller{fn: func(name, _ string) (string, error) {
		if name != "case.get" {
			return "", errors.New("unexpected " + name)
		}
		return `{"case_id":"case-queued","state":"INVESTIGATION_CASE_STATE_QUEUED"}`, nil
	}}
	err := Handle(context.Background(), badJSONProvider{}, caller, &agentv1.AgentInstruction{
		Kind: "CASE_REVIEW", PayloadRef: "case-queued", CapabilityToken: "tok",
	}, "access")
	if err == nil || !strings.Contains(err.Error(), "sensitive_content_ref") {
		t.Fatalf("missing sensitive reference must fail closed, got %v", err)
	}
}

func TestHandleProductionTriageOnlySubmitsDecision(t *testing.T) {
	caller := &scriptCaller{fn: func(name, args string) (string, error) {
		switch name {
		case "triage.get":
			if !strings.Contains(args, "turn-live") {
				return "", errors.New("missing turn binding")
			}
			return `{"turnId":"turn-live","projection":{"clusterId":"clu-live","reason":"TRIAGE_REASON_DETECTED_UNMITIGATED"}}`, nil
		case "triage.complete":
			if !strings.Contains(args, "TRIAGE_DISPOSITION_PROPOSE_POLICY") || !strings.Contains(args, "clu-live") {
				return "", errors.New("missing typed triage decision")
			}
			for _, forbidden := range []string{"detectionKeys", "assetId", "evidenceRefs", "createdBy", "scopeRisk"} {
				if strings.Contains(args, forbidden) {
					return "", errors.New("trusted field leaked: " + forbidden)
				}
			}
			return `{"turnId":"turn-live","releaseId":"rel-live","state":"SHADOW"}`, nil
		default:
			return "", errors.New("unexpected " + name)
		}
	}}
	err := Handle(context.Background(), staticProvider{content: `{"clusterId":"clu-live","disposition":"TRIAGE_DISPOSITION_PROPOSE_POLICY","rationale":"known mapped rule has no enforce policy"}`}, caller,
		&agentv1.AgentInstruction{Kind: "EVENT_TRIAGE", PayloadRef: "turn-live", TurnId: "turn-live", CapabilityToken: "tok"}, "access")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(caller.calls, ","); got != "triage.get,triage.complete" {
		t.Fatalf("production triage tools=%s", got)
	}
}

func TestHandleProductionTriageRejectsExtraModelFields(t *testing.T) {
	caller := &scriptCaller{fn: func(name, _ string) (string, error) {
		if name == "triage.get" {
			return `{"projection":{"clusterId":"clu-live"}}`, nil
		}
		return "", errors.New("triage.complete must not be called")
	}}
	err := Handle(context.Background(), staticProvider{content: `{"clusterId":"clu-live","disposition":"TRIAGE_DISPOSITION_REPORT_ONLY","rationale":"report","detectionKeys":[{"ruleId":"942100"}]}`}, caller,
		&agentv1.AgentInstruction{Kind: "EVENT_TRIAGE", PayloadRef: "turn-live", TurnId: "turn-live", CapabilityToken: "tok"}, "access")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("extra trusted field must be rejected, got %v", err)
	}
}

func TestParseToolCallExtractsWrappedJSON(t *testing.T) {
	call, err := parseToolCall("<think>plan</think>\n```json\n{\"done\":true}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if !call.Done {
		t.Fatalf("wrapped json must parse: %+v", call)
	}
}

type requestCaptureProvider struct {
	request modelgateway.ChatRequest
}

func (p *requestCaptureProvider) Complete(_ context.Context, request modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	p.request = request
	return modelgateway.ChatResponse{Content: `{"done":true}`}, nil
}

func TestCompleteInstructionModelLeavesModelSelectionToBrain(t *testing.T) {
	provider := &requestCaptureProvider{}
	_, err := completeInstructionModel(context.Background(), provider, &agentv1.AgentInstruction{}, []modelgateway.Message{{Role: "user", Content: "review"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if provider.request.Model != "" {
		t.Fatalf("runtime must not override the brain model slot, got %q", provider.request.Model)
	}
}

func TestParseToolCallFirstObjectWhenConcatenated(t *testing.T) {
	call, err := parseToolCall(`{"tool":"session.reply","args":{"session_id":"ses-1","content":"pong"}},{"done":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if call.Name != "session.reply" || !strings.Contains(call.Args, "pong") {
		t.Fatalf("want first object session.reply, got %+v", call)
	}
}

func TestHandleSessionMessageRepliesFromConcatenatedJSON(t *testing.T) {
	caller := &scriptCaller{fn: func(name, args string) (string, error) {
		if name != "session.reply" {
			return "", errors.New("unexpected " + name)
		}
		if !strings.Contains(args, "ses-live") || !strings.Contains(args, "pong") {
			return "", errors.New(args)
		}
		return `{"ok":true}`, nil
	}}
	err := Handle(context.Background(), concatJSONProvider{}, caller, &agentv1.AgentInstruction{
		Kind: "SESSION_MESSAGE", PayloadRef: "ses-live", CapabilityToken: "tok",
	}, "access")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(caller.calls, ",") != "session.reply" {
		t.Fatalf("calls=%v", caller.calls)
	}
}

func TestHandleRejectsInvalidModelJSON(t *testing.T) {
	err := Handle(context.Background(), badJSONProvider{}, &scriptCaller{fn: func(string, string) (string, error) {
		return "", nil
	}}, &agentv1.AgentInstruction{Kind: "PLAN_REQUEST", PayloadRef: "x"}, "access")
	if err == nil || !strings.Contains(err.Error(), "not json") {
		t.Fatalf("非法模型输出应报错，实际 %v", err)
	}
}

type concatJSONProvider struct{}

func (concatJSONProvider) Complete(context.Context, modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	return modelgateway.ChatResponse{Content: `{"tool":"session.reply","args":{"session_id":"ses-live","content":"pong"}},{"done":true}`}, nil
}

type badJSONProvider struct{}

func (badJSONProvider) Complete(context.Context, modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	return modelgateway.ChatResponse{Content: "not-json"}, nil
}

type staticProvider struct {
	content string
}

func (p staticProvider) Complete(context.Context, modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	return modelgateway.ChatResponse{Content: p.content}, nil
}
