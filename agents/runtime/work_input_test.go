package runtime

import (
	"strings"
	"testing"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestInvestigationInputAndReceiptFailClosed(t *testing.T) {
	ticket := &eventv1.CheckTicket{
		EventId: "event", AssetId: "asset",
		Forward:  commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE,
		Evidence: &eventv1.EvidenceProjection{Fields: map[string]string{"method": "GET"}},
	}
	digest, err := kernel.CheckTicketDigest(ticket)
	if err != nil {
		t.Fatal(err)
	}
	input := WorkInputFromProto(&workerv1.InvestigationInput{Ticket: ticket, TicketDigest: digest})
	if !input.IsInvestigation() || input.Validate() != nil {
		t.Fatalf("input=%#v", input)
	}
	reads := []*workerv1.InvestigationToolRead{{ToolName: "ticket.get", ResultDigest: "sha256:" + strings.Repeat("a", 64)}}
	receipt := &workerv1.InvestigationReceipt{
		EventId: ticket.GetEventId(), TicketDigest: digest, Status: "succeeded", Reads: reads,
		OutputDigest: InvestigationOutputDigest(reads),
	}
	if err := ValidateInvestigationReceipt(input, receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Reads = append(receipt.Reads, &workerv1.InvestigationToolRead{ToolName: "govern.propose", ResultDigest: "sha256:" + strings.Repeat("b", 64)})
	receipt.OutputDigest = InvestigationOutputDigest(receipt.Reads)
	if err := ValidateInvestigationReceipt(input, receipt); err == nil {
		t.Fatal("write tool in investigation receipt must fail closed")
	}
	if err := (WorkInput{Purpose: "unknown"}).Validate(); err == nil {
		t.Fatal("unknown work input purpose must fail closed")
	}
	if err := (WorkInput{TicketDigest: digest}).Validate(); err == nil {
		t.Fatal("coordinates without a purpose must fail closed")
	}
}
