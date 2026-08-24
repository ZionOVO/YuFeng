package brain

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	evidencev1 "yufeng/proto/gen/evidencev1"
	modelv1 "yufeng/proto/gen/modelv1"
)

func TestValidateTrafficFindingAcceptsTypedReferencesWithoutRawEvidence(t *testing.T) {
	entry := sensitiveRelayEntry{fragments: []*evidencev1.EvidenceFragment{
		{EvidenceHandle: "evidence-1", Field: "path", Content: []byte("/checkout/confirmation")},
		{EvidenceHandle: "evidence-1", Field: "body", Content: []byte("private-card-token-123456789")},
	}}
	raw := marshalTrafficFinding(t, &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MALICIOUS,
		Confidence:  0.91, EvidenceRefs: []string{"evidence-1"}, RouteTemplate: "/checkout/confirmation",
		AttackClass: "injection", Rationale: "请求形状与同步检测结果存在高风险偏差",
	})
	finding, normalized, err := validateTrafficFinding(raw, entry)
	if err != nil {
		t.Fatalf("validate typed traffic finding: %v", err)
	}
	if finding.GetRouteTemplate() != "/checkout/confirmation" || !strings.Contains(normalized, "evidence-1") {
		t.Fatalf("unexpected normalized finding %s", normalized)
	}
}

func TestSensitiveTrafficPromptFitsInputTokenEnvelope(t *testing.T) {
	entry := sensitiveRelayEntry{}
	for index := 0; index < 5; index++ {
		entry.fragments = append(entry.fragments, &evidencev1.EvidenceFragment{
			EvidenceHandle: "evidence-bounded", Field: "body", Content: bytes.Repeat([]byte{'x'}, kernel.TrafficReviewModelEvidenceBytes/5),
		})
	}
	var total int
	for _, message := range sensitiveTrafficMessages(entry) {
		total += len(message.Content)
	}
	if total > kernel.TrafficReviewModelInputBytes {
		t.Fatalf("sensitive prompt bytes=%d limit=%d", total, kernel.TrafficReviewModelInputBytes)
	}
}

func TestValidateTrafficFindingRejectsPartialSensitiveEcho(t *testing.T) {
	entry := sensitiveRelayEntry{fragments: []*evidencev1.EvidenceFragment{{
		EvidenceHandle: "evidence-1", Field: "body", Content: []byte("prefix-private-card-token-123456789-suffix"),
	}}}
	raw := marshalTrafficFinding(t, &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MALICIOUS,
		Confidence:  0.8, EvidenceRefs: []string{"evidence-1"}, Rationale: "观察到 private-card-token-1234，建议人工核对",
	})
	if _, _, err := validateTrafficFinding(raw, entry); err == nil {
		t.Fatal("partial sensitive echo must be discarded")
	}
}

func TestValidateTrafficFindingRejectsShortSensitiveEcho(t *testing.T) {
	entry := sensitiveRelayEntry{fragments: []*evidencev1.EvidenceFragment{{
		EvidenceHandle: "evidence-1", Field: "query", Content: []byte("pin=4829"),
	}}}
	raw := marshalTrafficFinding(t, &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MALICIOUS,
		Confidence:  0.8, EvidenceRefs: []string{"evidence-1"}, Rationale: "请求包含 pin=4829",
	})
	if _, _, err := validateTrafficFinding(raw, entry); err == nil {
		t.Fatal("short sensitive echo must be discarded")
	}
}

func TestValidateTrafficFindingRejectsReferenceOutsideApproval(t *testing.T) {
	entry := sensitiveRelayEntry{fragments: []*evidencev1.EvidenceFragment{{EvidenceHandle: "evidence-1", Field: "body", Content: []byte("opaque request content")}}}
	raw := marshalTrafficFinding(t, &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_BENIGN,
		Confidence:  0.7, EvidenceRefs: []string{"evidence-other"}, Rationale: "未发现异常",
	})
	if _, _, err := validateTrafficFinding(raw, entry); err == nil {
		t.Fatal("reference outside approved evidence must be rejected")
	}
}

func TestValidateTrafficFindingRejectsUnknownNumericDisposition(t *testing.T) {
	entry := sensitiveRelayEntry{fragments: []*evidencev1.EvidenceFragment{{
		EvidenceHandle: "evidence-1", Field: "body", Content: []byte("opaque request content"),
	}}}
	raw := marshalTrafficFinding(t, &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition(999),
		Confidence:  0.7, EvidenceRefs: []string{"evidence-1"}, Rationale: "未知枚举不得穿透类型门禁",
	})
	if _, _, err := validateTrafficFinding(raw, entry); err == nil {
		t.Fatal("unknown numeric disposition must be rejected")
	}
}

func TestValidateTrafficFindingRejectsShapeDraftOutsideSuspectedMiss(t *testing.T) {
	entry := sensitiveRelayEntry{fragments: []*evidencev1.EvidenceFragment{{
		EvidenceHandle: "evidence-1", Field: "body", Content: []byte("opaque request content"),
	}}}
	raw := marshalTrafficFinding(t, &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_BENIGN,
		Confidence:  0.7, EvidenceRefs: []string{"evidence-1"}, Rationale: "未发现异常",
		OptionalShapeDraft: &artifactv1.ShapeSource{Methods: []string{"POST"}, RouteTemplate: "/login"},
	})
	if _, _, err := validateTrafficFinding(raw, entry); err == nil {
		t.Fatal("shape draft outside suspected miss must be rejected")
	}
}

func TestValidateTrafficFindingEnforcesFieldAndMessageLimits(t *testing.T) {
	entry := sensitiveRelayEntry{fragments: []*evidencev1.EvidenceFragment{{
		EvidenceHandle: "evidence-1", Field: "body", Content: []byte("opaque request content"),
	}}}
	raw := marshalTrafficFinding(t, &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MALICIOUS,
		Confidence:  0.7, EvidenceRefs: []string{"evidence-1"}, AttackClass: strings.Repeat("x", 129),
	})
	if _, _, err := validateTrafficFinding(raw, entry); err == nil {
		t.Fatal("oversized typed field must be rejected")
	}
}

func marshalTrafficFinding(t *testing.T, finding *modelv1.TrafficFinding) string {
	t.Helper()
	raw, err := protojson.Marshal(finding)
	if err != nil {
		t.Fatalf("marshal traffic finding: %v", err)
	}
	return string(raw)
}
