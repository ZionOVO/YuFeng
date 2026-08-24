package edgecore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestTrafficReviewPolicyRolloutModes(t *testing.T) {
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	decision := Decision{Action: ActionAllow, GenerationID: "g1", GenerationSeq: 1}

	off := &artifactv1.TrafficReviewPolicy{Mode: artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF}
	offCollector, err := NewReviewCollector(off, "sha256:off", nil)
	if err != nil {
		t.Fatalf("create disabled collector: %v", err)
	}
	offCollector.Observe(start, "unit-1", "asset-1", "request-1", Request{Method: "GET", Path: "/"}, decision)
	if windows, candidates := offCollector.Drain(start.Add(6 * time.Minute)); len(windows) != 0 || len(candidates) != 0 {
		t.Fatalf("off mode emitted windows=%d candidates=%d", len(windows), len(candidates))
	}

	statistics := DefaultTrafficReviewPolicy()
	statistics.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY
	statisticsCollector, err := NewReviewCollector(statistics, "sha256:statistics", nil)
	if err != nil {
		t.Fatalf("create statistics collector: %v", err)
	}
	statisticsCollector.Observe(start, "unit-1", "asset-1", "request-1", Request{Method: "GET", Path: "/"}, decision)
	windows, candidates := statisticsCollector.Drain(start.Add(6 * time.Minute))
	if len(windows) != 1 || len(candidates) != 0 {
		t.Fatalf("statistics mode emitted windows=%d candidates=%d", len(windows), len(candidates))
	}

	redacted := DefaultTrafficReviewPolicy()
	redacted.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES
	redactedCollector, err := NewReviewCollector(redacted, "sha256:redacted", nil)
	if err != nil {
		t.Fatalf("create redacted collector: %v", err)
	}
	redactedCollector.Observe(start, "unit-1", "asset-1", "request-1", Request{Method: "GET", Path: "/"}, decision)
	_, candidates = redactedCollector.Drain(start.Add(6 * time.Minute))
	if len(candidates) != 1 {
		t.Fatalf("redacted mode candidates=%d", len(candidates))
	}
	if candidates[0].GetEvidenceHandle() != "" {
		t.Fatalf("redacted mode exposed evidence handle %q", candidates[0].GetEvidenceHandle())
	}
}

func TestValidateTrafficReviewPolicyRejectsInvalidBounds(t *testing.T) {
	valid := DefaultTrafficReviewPolicy()
	valid.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES
	clone := func() *artifactv1.TrafficReviewPolicy {
		return proto.Clone(valid).(*artifactv1.TrafficReviewPolicy)
	}
	tests := []struct {
		name   string
		policy func() *artifactv1.TrafficReviewPolicy
	}{
		{name: "missing policy", policy: func() *artifactv1.TrafficReviewPolicy { return nil }},
		{name: "unspecified mode", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_UNSPECIFIED
			return value
		}},
		{name: "unknown mode", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.Mode = artifactv1.TrafficReviewMode(99)
			return value
		}},
		{name: "wrong window", policy: func() *artifactv1.TrafficReviewPolicy { value := clone(); value.WindowSeconds--; return value }},
		{name: "no route cells", policy: func() *artifactv1.TrafficReviewPolicy { value := clone(); value.TopRouteCells = 0; return value }},
		{name: "too many route cells", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.TopRouteCells = kernel.TrafficReviewTopRoutes + 1
			return value
		}},
		{name: "no candidates", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.MaxCandidatesPerWindow = 0
			return value
		}},
		{name: "too many candidates", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.MaxCandidatesPerWindow = kernel.TrafficReviewCandidatesPerWindow + 1
			return value
		}},
		{name: "no evidence budget", policy: func() *artifactv1.TrafficReviewPolicy { value := clone(); value.MaxEvidenceBytes = 0; return value }},
		{name: "evidence budget too large", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.MaxEvidenceBytes = kernel.TrafficReviewEvidenceBytes + 1
			return value
		}},
		{name: "vault smaller than entry", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.VaultMaxBytes = int64(value.MaxEvidenceBytes - 1)
			return value
		}},
		{name: "vault too large", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.VaultMaxBytes = kernel.TrafficReviewVaultBytes + 1
			return value
		}},
		{name: "evidence lifetime too short", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.EvidenceTtlSeconds = int64(time.Hour/time.Second) - 1
			return value
		}},
		{name: "evidence lifetime too long", policy: func() *artifactv1.TrafficReviewPolicy {
			value := clone()
			value.EvidenceTtlSeconds = int64(kernel.TrafficReviewEvidenceTTL/time.Second) + 1
			return value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateTrafficReviewPolicy(test.policy()); err == nil {
				t.Fatal("invalid traffic review policy must be rejected")
			}
		})
	}
	if err := ValidateTrafficReviewPolicy(&artifactv1.TrafficReviewPolicy{Mode: artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF}); err != nil {
		t.Fatalf("disabled policy must not require active limits: %v", err)
	}
}

func TestReviewCollectorBoundsCandidatesAndAggregatesRoutes(t *testing.T) {
	vault, err := NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	collector, err := NewReviewCollector(policy, "sha256:policy", vault)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		dec := Decision{Action: ActionAllow, GenerationID: "g1", GenerationSeq: 1,
			Inspection: Inspection{Coverage: []Coverage{{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_BODY, Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL}}}}
		if i < 10 {
			dec.Action = ActionBlock
		}
		collector.Observe(start.Add(time.Duration(i)*time.Second), "unit-1", "asset-1", newEventID(), Request{
			Method: "GET", Path: fmt.Sprintf("/route/r%d", i%40), Query: "q=secret", Body: []byte("body"),
		}, dec)
	}
	windows, candidates := collector.Drain(start.Add(6 * time.Minute))
	if len(windows) != 1 {
		t.Fatalf("windows=%d", len(windows))
	}
	if windows[0].GetRequestCount() != 100 || len(windows[0].GetRouteCells()) != 32 || windows[0].GetOther().GetRequestCount() == 0 {
		t.Fatalf("window=%+v", windows[0])
	}
	if len(candidates) > 4 {
		t.Fatalf("candidates=%d", len(candidates))
	}
}

func TestReviewCollectorSeparatesAssetGenerationWithinOneClockWindow(t *testing.T) {
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY
	collector, err := NewReviewCollector(policy, "sha256:policy", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	collector.Observe(start, "unit-1", "asset-1", "request-1", Request{Method: "GET", Path: "/"},
		Decision{Action: ActionAllow, GenerationID: "generation-1", GenerationSeq: 1})
	collector.Observe(start.Add(time.Second), "unit-1", "asset-1", "request-2", Request{Method: "GET", Path: "/"},
		Decision{Action: ActionAllow, GenerationID: "generation-2", GenerationSeq: 2})
	windows, _ := collector.Drain(start.Add(6 * time.Minute))
	if len(windows) != 2 || windows[0].GetWindowId() == windows[1].GetWindowId() || windows[0].GetGenerationId() == windows[1].GetGenerationId() {
		t.Fatalf("generation windows=%v", windows)
	}
}

func TestTrafficRouteTemplateRemovesIdentifiersAndQuery(t *testing.T) {
	got := TrafficRouteTemplate("/users/12345/orders/550e8400-e29b-41d4-a716-446655440000?access_token=secret")
	if got != "/users/:number/orders/:id" {
		t.Fatalf("route template=%q", got)
	}
	if got := TrafficRouteTemplate("/users/alice@example.com"); got != "/users/:value" {
		t.Fatalf("personal path template=%q", got)
	}
}

func TestReviewCandidateIncludesDeterministicRiskReasons(t *testing.T) {
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES
	collector, err := NewReviewCollector(policy, "sha256:risk", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	collector.Observe(start, "unit-1", "asset-1", "request-1", Request{Method: "POST", Path: "/users/12345"},
		Decision{Action: ActionAllow, GenerationID: "g1", GenerationSeq: 1, Detections: []Detection{{
			InspectorID: "test", RuleID: "rule", Class: commonv1.AttackClass_ATTACK_CLASS_UNMAPPED, Score: 0.7,
		}}})
	_, candidates := collector.Drain(start.Add(6 * time.Minute))
	if len(candidates) != 1 || candidates[0].GetRouteTemplate() != "/users/:number" {
		t.Fatalf("candidates=%v", candidates)
	}
	for _, reason := range []string{"sync_detection", "suspected_miss", "unmapped_detection", "anomaly_score"} {
		found := false
		for _, got := range candidates[0].GetRiskReasons() {
			found = found || got == reason
		}
		if !found {
			t.Fatalf("missing risk reason %s: %v", reason, candidates[0].GetRiskReasons())
		}
	}
}

func TestReviewCollectorRetainsFrozenSnapshotUntilSpoolCommit(t *testing.T) {
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY
	collector, err := NewReviewCollector(policy, "sha256:two-phase", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(5 * time.Minute)
	collector.Observe(start, "edge-1", "asset-1", "request-1", Request{Method: "GET", Path: "/health"},
		Decision{Action: ActionAllow, GenerationID: "generation-1", GenerationSeq: 1})
	first, _, version := collector.PrepareDrain(start.Add(6 * time.Minute))
	if len(first) != 1 {
		t.Fatalf("first snapshot windows=%d", len(first))
	}
	retry, _, retryVersion := collector.PrepareDrain(start.Add(7 * time.Minute))
	if len(retry) != 1 || retry[0].GetWindowId() != first[0].GetWindowId() || retryVersion != version {
		t.Fatalf("uncommitted snapshot changed: retry=%v version=%d want=%d", retry, retryVersion, version)
	}
	collector.CommitDrain(version)
	committed, _, _ := collector.PrepareDrain(start.Add(8 * time.Minute))
	if len(committed) != 0 {
		t.Fatalf("committed snapshot remained in memory: %v", committed)
	}
}

func TestReviewCollectorFlushRetainsSnapshotUntilMatchingCommit(t *testing.T) {
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY
	collector, err := NewReviewCollector(policy, "sha256:flush", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 23, 10, 1, 0, 0, time.UTC)
	collector.Observe(start, "edge-1", "asset-1", "request-1", Request{Method: "GET", Path: "/health"},
		Decision{Action: ActionAllow, GenerationID: "generation-1", GenerationSeq: 1})
	windows, candidates, version := collector.PrepareFlush()
	if len(windows) != 1 || len(candidates) != 0 {
		t.Fatalf("flush windows=%d candidates=%d", len(windows), len(candidates))
	}
	collector.CommitDrain(version + 1)
	retry, _, retryVersion := collector.PrepareFlush()
	if len(retry) != 1 || retryVersion != version || retry[0].GetWindowId() != windows[0].GetWindowId() {
		t.Fatalf("mismatched commit lost snapshot: retry=%v version=%d want=%d", retry, retryVersion, version)
	}
	collector.CommitDrain(version)
	if committed, _, _ := collector.PrepareFlush(); len(committed) != 0 {
		t.Fatalf("matching commit retained snapshot: %v", committed)
	}

	collector.Observe(start.Add(time.Second), "edge-1", "asset-1", "request-2", Request{Method: "GET", Path: "/ready"},
		Decision{Action: ActionAllow, GenerationID: "generation-1", GenerationSeq: 1})
	if flushed, candidates := collector.Flush(); len(flushed) != 1 || len(candidates) != 0 {
		t.Fatalf("flush result windows=%d candidates=%d", len(flushed), len(candidates))
	}
}

func TestControlledSelectorValueSupportsQueryAndStructuredBodies(t *testing.T) {
	tests := []struct {
		name        string
		selector    string
		request     Request
		contentType string
		wantValue   string
		wantSurface string
	}{
		{name: "query", selector: "query.id", request: Request{Query: "id=42&id=43"}, wantValue: "42", wantSurface: "query"},
		{name: "json", selector: "json.name", request: Request{Body: []byte(`{"name":"alice"}`)}, contentType: "application/problem+json", wantValue: "alice", wantSurface: "body"},
		{name: "generic json body", selector: "body.name", request: Request{Body: []byte(`{"name":"alice"}`)}, contentType: "application/json", wantValue: "alice", wantSurface: "body"},
		{name: "form body", selector: "arg.name", request: Request{Body: []byte("name=alice")}, contentType: "application/x-www-form-urlencoded", wantValue: "alice", wantSurface: "body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, surface, ok := controlledSelectorValue(test.selector, test.request, test.contentType)
			if !ok || value != test.wantValue || surface != test.wantSurface {
				t.Fatalf("value=%q surface=%q ok=%v", value, surface, ok)
			}
		})
	}
	for _, selector := range []string{"missing-dot", "query.missing", "header.authorization", "json.name"} {
		if _, _, ok := controlledSelectorValue(selector, Request{Query: "%zz", Body: []byte("not-json")}, "application/octet-stream"); ok {
			t.Fatalf("unsupported selector %q returned a value", selector)
		}
	}
}

func TestMarshalBoundedEvidenceShrinksOnlyOptionalStrings(t *testing.T) {
	document := trafficEvidenceDocument{Method: strings.Repeat("M", 80), RouteTemplate: "/" + strings.Repeat("route", 80)}
	raw, err := marshalBoundedEvidence(document, 96)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 96 || !json.Valid(raw) {
		t.Fatalf("bounded evidence bytes=%d valid=%v", len(raw), json.Valid(raw))
	}
	if _, err := marshalBoundedEvidence(document, 1); err == nil {
		t.Fatal("structural envelope must fail when the byte budget cannot fit it")
	}
	if _, err := marshalBoundedEvidence(document, 0); err == nil {
		t.Fatal("non-positive evidence budget must be rejected")
	}
}

func TestReviewCollectorKeepsOversizedEvidenceStructurallyValid(t *testing.T) {
	vault, err := NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{13}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	collector, err := NewReviewCollector(policy, "sha256:policy", vault)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	collector.Observe(start, "unit-1", "asset-1", "request-oversized", Request{
		Method: "POST", Path: "/large", Query: "payload=" + strings.Repeat("q", 10<<10),
	}, Decision{Action: ActionBlock, GenerationID: "g1", GenerationSeq: 1, Detections: []Detection{{Selector: "query.payload"}}})
	_, candidates := collector.Drain(start.Add(6 * time.Minute))
	if len(candidates) != 1 || candidates[0].GetEvidenceHandle() == "" {
		t.Fatalf("candidates=%v", candidates)
	}
	raw, _, ok, err := vault.Get(candidates[0].GetEvidenceHandle(), start.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("read bounded evidence ok=%v err=%v", ok, err)
	}
	if len(raw) > kernel.TrafficReviewEvidenceBytes || !json.Valid(raw) {
		t.Fatalf("bounded evidence bytes=%d valid=%v", len(raw), json.Valid(raw))
	}
}

func TestReviewCollectorKeepsStatisticsWhenVaultIsUnavailable(t *testing.T) {
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	collector, err := NewReviewCollector(policy, "sha256:no-vault", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	collector.Observe(start, "unit-1", "asset-1", "request-1", Request{Method: "POST", Path: "/login", Body: []byte("payload")},
		Decision{Action: ActionBlock, GenerationID: "g1", GenerationSeq: 1})
	windows, candidates := collector.Drain(start.Add(6 * time.Minute))
	if len(windows) != 1 || len(candidates) != 0 {
		t.Fatalf("windows=%d candidates=%d", len(windows), len(candidates))
	}
	if windows[0].GetRequestCount() != 1 || windows[0].GetEvidenceDroppedCount() != 1 || windows[0].GetEvidenceDropReasons()["vault_unavailable"] != 1 {
		t.Fatalf("window=%+v", windows[0])
	}
}

func TestControlledTrafficEvidenceStoresOnlyDetectedSafeFields(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEifQ.signature-part"
	certificate := "-----BEGIN CERTIFICATE-----\nclient-certificate-secret\n-----END CERTIFICATE-----"
	req := Request{
		Method: "POST", Path: "/login", Query: "access_token=query-secret&safe=42&ignored=never-store",
		Headers: map[string]string{"Content-Type": "application/json", "Authorization": "Bearer header-secret"},
		Body:    []byte(`{"cookie":"session-secret","client_certificate":"` + certificate + `","payload":"` + jwt + `"}`),
	}
	decision := Decision{Detections: []Detection{
		{Selector: "query.access_token"}, {Selector: "query.safe"}, {Selector: "json.cookie"},
		{Selector: "json.client_certificate"}, {Selector: "json.payload"}, {Selector: "header.authorization"},
	}}
	document := controlledTrafficEvidence(req, decision, "/login")
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"query-secret", "session-secret", "client-certificate-secret", jwt, "header-secret", "never-store"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("credential or unselected field %q survived controlled evidence: %s", secret, raw)
		}
	}
	if !bytes.Contains(raw, []byte(`"value":"42"`)) {
		t.Fatalf("safe detected selector was not retained: %s", raw)
	}
	for _, field := range document.Fields {
		if field.Selector == "header.authorization" {
			t.Fatal("headers must never enter the evidence vault")
		}
	}
}

func TestControlledTrafficEvidenceUnknownContentTypeStoresMetadataOnly(t *testing.T) {
	document := controlledTrafficEvidence(Request{
		Method: "POST", Path: "/upload", Headers: map[string]string{"Content-Type": "application/octet-stream"},
		Body: []byte("binary-secret-that-must-never-be-stored"),
	}, Decision{Detections: []Detection{{Selector: "body.payload"}}}, "/upload")
	if len(document.Fields) != 0 || document.ContentLength == 0 || document.ContentType != "application/octet-stream" {
		t.Fatalf("document=%+v", document)
	}
}

func TestReviewCollectorWritesOnlyFinalWindowRepresentatives(t *testing.T) {
	directory := t.TempDir()
	vault, err := NewEvidenceVault(directory, bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	collector, err := NewReviewCollector(policy, "sha256:policy", vault)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 20; index++ {
		collector.Observe(start.Add(time.Duration(index)*time.Second), "unit-1", "asset-1", newEventID(), Request{
			Method: "GET", Path: "/risk/" + string(rune('a'+index)), Query: "value=kept",
		}, Decision{Action: ActionAllow, GenerationID: "g1", GenerationSeq: 1,
			Detections: []Detection{{InspectorID: "test", RuleID: "rule", Score: float64(index) / 20}}})
	}
	_, candidates := collector.Drain(start.Add(6 * time.Minute))
	if len(candidates) != kernel.TrafficReviewCandidatesPerWindow {
		t.Fatalf("candidates=%d", len(candidates))
	}
	files, err := filepath.Glob(filepath.Join(directory, "evidence-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	records := 0
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		records += bytes.Count(raw, []byte{'\n'})
	}
	if records != len(candidates) {
		t.Fatalf("vault records=%d candidates=%d", records, len(candidates))
	}
}

func TestTrafficReviewProjectionStaysBoundedAcrossTwentyEdges(t *testing.T) {
	project := func(requestsPerEdge int) (int, int) {
		windows, candidates := 0, 0
		for edge := 0; edge < 20; edge++ {
			policy := DefaultTrafficReviewPolicy()
			policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES
			collector, err := NewReviewCollector(policy, "sha256:bounded", nil)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
			for request := 0; request < requestsPerEdge; request++ {
				collector.Observe(start, "edge-"+string(rune('a'+edge)), "asset-1", newEventID(), Request{
					Method: "GET", Path: "/route/" + string(rune(0x1000+request)),
				}, Decision{Action: ActionAllow, GenerationID: "g1", GenerationSeq: 1,
					Detections: []Detection{{InspectorID: "test", RuleID: "rule", Score: float64(request%100) / 100}}})
			}
			if len(collector.state.routes) > kernel.TrafficReviewTopRoutes || len(collector.state.candidates) > kernel.TrafficReviewCandidatesPerWindow || len(collector.state.evidence) > kernel.TrafficReviewCandidatesPerWindow {
				t.Fatalf("edge memory projection routes=%d candidates=%d evidence=%d",
					len(collector.state.routes), len(collector.state.candidates), len(collector.state.evidence))
			}
			projectedWindows, projectedCandidates := collector.Drain(start.Add(6 * time.Minute))
			windows += len(projectedWindows)
			candidates += len(projectedCandidates)
		}
		return windows, candidates
	}
	lowWindows, lowCandidates := project(100)
	highWindows, highCandidates := project(2000)
	if lowWindows != 20 || highWindows != 20 || lowCandidates > 80 || highCandidates > 80 {
		t.Fatalf("projected writes low=%d/%d high=%d/%d", lowWindows, lowCandidates, highWindows, highCandidates)
	}
}
