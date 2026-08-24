package brain

import (
	"testing"

	"yufeng/lib/edgecore"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestCompilePolicyIntentWritesCandidate(t *testing.T) {
	raw, err := compileProposalPayload(&governv1.ProposalIntent{
		Kind:          commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
		DetectionKeys: []*commonv1.DetectionKey{compileDetectionKey("other-eye", "942100")},
	}, &artifactv1.Scope{AssetIds: []string{"asset-1"}}, edgecore.DefaultTaxonomyMapper())
	if err != nil {
		t.Fatal(err)
	}
	var cand artifactv1.PolicyCandidate
	if err := protojson.Unmarshal(raw, &cand); err != nil {
		t.Fatal(err)
	}
	if cand.Action != "block" || cand.GetPredicate() == nil || len(cand.Predicate.DetectionKeys) != 1 {
		t.Fatalf("compiled candidate: %+v", &cand)
	}
	if cand.Predicate.DetectionKeys[0].RuleId != "942100" || cand.GetScope().GetAssetId() != "asset-1" {
		t.Fatalf("keys/scope not copied: %+v", &cand)
	}
}

func TestCompileShapeIntentWritesSource(t *testing.T) {
	raw, err := compileProposalPayload(&governv1.ProposalIntent{
		Kind: commonv1.ProposalKind_PROPOSAL_KIND_SHAPE,
		ShapeSource: &artifactv1.ShapeSource{
			Methods:    []string{"GET"},
			PathPrefix: "/api/items",
			Constraints: []*artifactv1.ShapeConstraint{{
				Selector: "query.id", MinLen: 1, MaxLen: 8, Charset: "digit",
			}},
		},
	}, &artifactv1.Scope{AssetIds: []string{"asset-1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var src artifactv1.ShapeSource
	if err := protojson.Unmarshal(raw, &src); err != nil {
		t.Fatal(err)
	}
	if src.PathPrefix != "/api/items" || len(src.Methods) != 1 {
		t.Fatalf("compiled shape: %+v", &src)
	}
	if _, err := compileProposalPayload(&governv1.ProposalIntent{
		Kind:        commonv1.ProposalKind_PROPOSAL_KIND_SHAPE,
		ShapeSource: &artifactv1.ShapeSource{Methods: []string{"GET"}, PathPrefix: "/"},
	}, nil, nil); err == nil {
		t.Fatal("root prefix must fail")
	}
}

func TestShapeIntentIsNotCRSMapped(t *testing.T) {
	risk, ev := intentRiskClass(&governv1.ProposalIntent{Kind: commonv1.ProposalKind_PROPOSAL_KIND_SHAPE})
	if ev == commonv1.EvidenceClass_EVIDENCE_CLASS_CRS_MAPPED {
		t.Fatal("shape must not be tagged crs_mapped")
	}
	if !autoPromoteBlocked(scopeRiskDB(risk), evidenceClassDB(ev)) {
		t.Fatal("shape tags must block auto promote")
	}
}

func TestCompileWithoutMapperRejectsKeys(t *testing.T) {
	_, err := compileProposalPayload(&governv1.ProposalIntent{
		Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
		DetectionKeys: []*commonv1.DetectionKey{{
			DetectorId: "crs", RuleId: "942100",
		}},
	}, &artifactv1.Scope{AssetIds: []string{"asset-1"}}, nil)
	if err == nil {
		t.Fatal("mapper absence must not auto-govern")
	}
}

func TestCompilePolicyIntentRequiresKeys(t *testing.T) {
	_, err := compileProposalPayload(&governv1.ProposalIntent{Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY}, nil, edgecore.DefaultTaxonomyMapper())
	if err == nil {
		t.Fatal("empty detection_keys must fail")
	}
}

func TestCompilePolicyIntentRejectsProtocolRules(t *testing.T) {
	_, err := compileProposalPayload(&governv1.ProposalIntent{
		Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
		DetectionKeys: []*commonv1.DetectionKey{{
			DetectorId: "crs", RuleId: "920100",
			TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
		}},
	}, &artifactv1.Scope{AssetIds: []string{"asset-1"}}, edgecore.DefaultTaxonomyMapper())
	if err == nil {
		t.Fatal("920 protocol keys must not compile")
	}
	raw, err := compileProposalPayload(&governv1.ProposalIntent{
		Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
		DetectionKeys: []*commonv1.DetectionKey{
			{DetectorId: "crs", RuleId: "920100"},
			compileDetectionKey("other-eye", "942100"),
		},
	}, &artifactv1.Scope{AssetIds: []string{"asset-1"}}, edgecore.DefaultTaxonomyMapper())
	if err != nil {
		t.Fatal(err)
	}
	var cand artifactv1.PolicyCandidate
	if err := protojson.Unmarshal(raw, &cand); err != nil {
		t.Fatal(err)
	}
	if len(cand.Predicate.DetectionKeys) != 1 || cand.Predicate.DetectionKeys[0].RuleId != "942100" {
		t.Fatalf("only attack-class key must remain: %+v", cand.Predicate.DetectionKeys)
	}
}

func TestCompilePolicyIntentRejectsMixedDependencies(t *testing.T) {
	first := compileDetectionKey("crs", "942100")
	second := compileDetectionKey("crs", "941100")
	second.DetectorManifestDigest = "other-manifest"
	if _, err := compileProposalPayload(&governv1.ProposalIntent{
		Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY, DetectionKeys: []*commonv1.DetectionKey{first, second},
	}, &artifactv1.Scope{AssetIds: []string{"asset-1"}}, edgecore.DefaultTaxonomyMapper()); err == nil {
		t.Fatal("mixed dependency digests must fail before publication")
	}
}

func compileDetectionKey(detectorID, ruleID string) *commonv1.DetectionKey {
	return &commonv1.DetectionKey{
		DetectorId: detectorID, RuleId: ruleID,
		DetectorVersion: "detector-version", DetectorManifestDigest: "manifest",
		NormalizationProfileDigest: "normalizer",
		TargetLocation:             commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
	}
}
