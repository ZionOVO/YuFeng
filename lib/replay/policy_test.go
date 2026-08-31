package replay

import (
	"context"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/edgecore"
)

func TestPolicyReplayBlocksDetectionKeyOnR5Corpus(t *testing.T) {
	det, err := edgecore.NewCorazaDetector()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := det.Close(); err != nil {
			t.Errorf("close Coraza detector: %v", err)
		}
	})
	sample := edgecore.Request{Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+password"}
	dets, err := det.Detect(sample)
	if err != nil || len(dets) == 0 {
		t.Fatalf("need crs key: %v %#v", err, dets)
	}
	payload, err := protojson.Marshal(&artifactv1.PolicyCandidate{
		Action: "block",
		Predicate: &artifactv1.PolicyPredicate{
			DetectionKeys: []*commonv1.DetectionKey{{
				DetectorId: "crs", RuleId: dets[0].RuleID,
				TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	corpus := []Case{
		{ID: "m-sql", Label: LabelMalicious, Request: sample},
		{ID: "b-page", Label: LabelBenign, Request: edgecore.Request{Method: "GET", Path: "/api/items", Query: "page=2"}},
	}
	report, err := Run(context.Background(), &artifactv1.Artifact{
		PayloadSchema: "policy/v1", Payload: payload,
	}, corpus)
	if err != nil {
		t.Fatal(err)
	}
	if report.MaliciousBlocked != report.MaliciousTotal || report.MaliciousTotal == 0 {
		t.Fatalf("malicious must all block: %+v", report)
	}
	if report.BenignBlocked != 0 {
		t.Fatalf("benign must be zero: %+v", report)
	}
}

func TestPolicyReplayOnlyAccountsCandidateScope(t *testing.T) {
	cand := &artifactv1.PolicyCandidate{
		Action: "block",
		Predicate: &artifactv1.PolicyPredicate{DetectionKeys: []*commonv1.DetectionKey{{
			DetectorId: "crs", RuleId: "942100", TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
		}}},
		Scope: &artifactv1.PolicyScope{AssetId: "asset-1", RouteTemplate: "/api/items", Methods: []string{"GET"}},
	}
	payload, err := protojson.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	report, err := RunPolicy(context.Background(), &artifactv1.Artifact{PayloadSchema: policySchema, Payload: payload}, BuiltinCorpus("asset-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.GetPassed() || report.GetMaliciousTotal() != 1 || report.GetMaliciousBlocked() != 1 {
		t.Fatalf("scope-aware replay must only account matching corpus cases: %+v", report)
	}
}
