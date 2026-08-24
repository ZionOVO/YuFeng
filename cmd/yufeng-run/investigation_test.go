package main

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/agents/runtime"
	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestExecuteInvestigationReadsOnlyFrozenProjections(t *testing.T) {
	ticket := &eventv1.CheckTicket{
		EventId: "event-investigation", AssetId: "asset-investigation", Method: "GET",
		Forward:  commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE,
		Evidence: &eventv1.EvidenceProjection{Fields: map[string]string{"span_hash": "ab"}},
	}
	digest, err := kernel.CheckTicketDigest(ticket)
	if err != nil {
		t.Fatal(err)
	}
	broker := &investigationBrokerStub{results: map[string]string{
		"ticket.get":  `{"event_id":"event-investigation"}`,
		"cluster.get": `{"cluster_id":"cluster-investigation"}`,
	}}
	input := runtime.WorkInput{Purpose: "investigation", Ticket: ticket, TicketDigest: digest, ClusterID: "cluster-investigation"}
	raw, err := executeInvestigation(context.Background(), broker, input)
	if err != nil {
		t.Fatal(err)
	}
	var receipt workerv1.InvestigationReceipt
	if err := protojson.Unmarshal([]byte(raw), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := kernel.ValidateInvestigationReceipt(&workerv1.InvestigationInput{
		Ticket: ticket, TicketDigest: digest, ClusterId: input.ClusterID,
	}, &receipt); err != nil {
		t.Fatal(err)
	}
	if strings.Join(broker.calls, ",") != "ticket.get,cluster.get" {
		t.Fatalf("calls=%v", broker.calls)
	}
	for _, call := range broker.calls {
		if strings.HasPrefix(call, "govern.") || strings.Contains(call, "exec") || strings.Contains(call, "shell") {
			t.Fatalf("investigation called forbidden tool %s", call)
		}
	}
	if len(broker.audits) != 1 || !strings.Contains(broker.audits[0], digest) {
		t.Fatalf("audits=%v", broker.audits)
	}
}

func TestExecuteInvestigationRejectsTamperedOrCancelledInputBeforeToolCall(t *testing.T) {
	ticket := &eventv1.CheckTicket{
		EventId: "event-tampered", AssetId: "asset-tampered",
		Forward:  commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE,
		Evidence: &eventv1.EvidenceProjection{Fields: map[string]string{"method": "GET"}},
	}
	broker := &investigationBrokerStub{results: map[string]string{"ticket.get": `{}`}}
	input := runtime.WorkInput{Purpose: "investigation", Ticket: ticket, TicketDigest: "sha256:" + strings.Repeat("0", 64)}
	if _, err := executeInvestigation(context.Background(), broker, input); err == nil {
		t.Fatal("tampered ticket must fail closed")
	}
	if len(broker.calls) != 0 {
		t.Fatalf("tampered input invoked tools: %v", broker.calls)
	}

	digest, err := kernel.CheckTicketDigest(ticket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executeInvestigation(ctx, broker, runtime.WorkInput{Purpose: "investigation", Ticket: ticket, TicketDigest: digest}); err == nil {
		t.Fatal("cancelled investigation must stop")
	}
	if len(broker.calls) != 0 {
		t.Fatalf("cancelled input invoked tools: %v", broker.calls)
	}
}

type investigationBrokerStub struct {
	results map[string]string
	calls   []string
	audits  []string
}

func (b *investigationBrokerStub) Invoke(tool, _ string) (string, error) {
	b.calls = append(b.calls, tool)
	return b.results[tool], nil
}

func (b *investigationBrokerStub) Audit(kind, payload string) error {
	b.audits = append(b.audits, kind+":"+payload)
	return nil
}

func (*investigationBrokerStub) Done(string) error { return nil }
func (*investigationBrokerStub) Fail(string) error { return nil }
