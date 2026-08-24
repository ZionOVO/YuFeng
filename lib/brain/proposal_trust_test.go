package brain

import (
	"testing"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	governv1 "yufeng/proto/gen/governv1"
)

func TestDeriveTrustedProposalRejectsFactsAbsentFromCluster(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	assetID := "asset-trust-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	trusted := &commonv1.DetectionKey{
		DetectorId: "crs", DetectorVersion: "4.25.0", DetectorManifestDigest: "manifest",
		RuleId: "942100", Phase: "request", TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
		TargetSelector: "query.id", NormalizationProfileDigest: "profile",
	}
	clusterID := seedProposalCluster(t, ctx, st.Pool(), assetID, "/api/items", "GET",
		commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED, []*commonv1.DetectionKey{trusted})
	base := &governv1.ProposeArtifactRequest{
		Intent: &governv1.ProposalIntent{Kind: commonv1.ProposalKind_PROPOSAL_KIND_POLICY, ClusterId: clusterID},
		Scope:  &artifactv1.Scope{AssetIds: []string{assetID}},
	}
	base.Intent.DetectionKeys = []*commonv1.DetectionKey{{RuleId: "941100"}}
	if _, err := deriveTrustedProposal(ctx, st.Pool(), base); err == nil {
		t.Fatal("detection absent from pinned event must fail")
	}
	base.Intent.DetectionKeys = []*commonv1.DetectionKey{{RuleId: trusted.RuleId}}
	derived, err := deriveTrustedProposal(ctx, st.Pool(), base)
	if err != nil {
		t.Fatal(err)
	}
	got := derived.intent.GetDetectionKeys()[0]
	if got.GetTargetSelector() != trusted.GetTargetSelector() || got.GetDetectorManifestDigest() != trusted.GetDetectorManifestDigest() ||
		len(derived.evidenceRefs) != 1 || derived.intent.GetRouteTemplate() != "/api/items" {
		t.Fatalf("trusted projection is incomplete: %+v refs=%v", got, derived.evidenceRefs)
	}
}

func TestTrustedPolicyKeysUsePinnedEventFacts(t *testing.T) {
	want := &commonv1.DetectionKey{
		DetectorId: "crs", DetectorVersion: "4.14.0", DetectorManifestDigest: "sha256:manifest",
		RuleId: "942100", Phase: "request", TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
		TargetSelector: "query.id", NormalizationProfileDigest: "sha256:profile",
	}
	keys, err := trustedPolicyKeys([]*commonv1.DetectionKey{{RuleId: "942100"}}, []*eventv1.Event{{
		Detections: []*eventv1.Detection{{Key: want}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].GetDetectorManifestDigest() != want.GetDetectorManifestDigest() ||
		keys[0].GetNormalizationProfileDigest() != want.GetNormalizationProfileDigest() ||
		keys[0].GetTargetSelector() != want.GetTargetSelector() {
		t.Fatalf("trusted key was not projected from pinned event: %+v", keys)
	}
	keys[0].RuleId = "changed"
	if want.GetRuleId() != "942100" {
		t.Fatal("trusted key must be cloned")
	}
}

func TestTrustedPolicyKeysRejectAbsentAndAmbiguousFacts(t *testing.T) {
	events := []*eventv1.Event{{Detections: []*eventv1.Detection{
		{Key: &commonv1.DetectionKey{DetectorId: "a", RuleId: "942100"}},
		{Key: &commonv1.DetectionKey{DetectorId: "b", RuleId: "942100"}},
	}}}
	if _, err := trustedPolicyKeys([]*commonv1.DetectionKey{{RuleId: "941100"}}, events); err == nil {
		t.Fatal("fact absent from pinned events must fail")
	}
	if _, err := trustedPolicyKeys([]*commonv1.DetectionKey{{RuleId: "942100"}}, events); err == nil {
		t.Fatal("ambiguous partial assertion must fail")
	}
	keys, err := trustedPolicyKeys([]*commonv1.DetectionKey{{DetectorId: "a", RuleId: "942100"}}, events)
	if err != nil || len(keys) != 1 || keys[0].GetDetectorId() != "a" {
		t.Fatalf("fully disambiguated assertion must pass: keys=%+v err=%v", keys, err)
	}
}

func TestValidateShapeAgainstClusterRejectsInventedSelector(t *testing.T) {
	cluster := proposalCluster{
		route: "/api/items", method: "POST",
		events: []*eventv1.Event{{
			Traffic:    &eventv1.Event_Http{Http: &eventv1.Http{QueryRedacted: "id=redacted"}},
			Detections: []*eventv1.Detection{{Key: &commonv1.DetectionKey{TargetSelector: "json.name"}}},
		}},
	}
	valid := &artifactv1.ShapeSource{
		Methods: []string{"POST"}, PathPrefix: "/api", RouteTemplate: "/api/items",
		Constraints: []*artifactv1.ShapeConstraint{{Selector: "query.id"}, {Selector: "json.name"}},
	}
	if err := validateShapeAgainstCluster(valid, cluster); err != nil {
		t.Fatal(err)
	}
	invalid := &artifactv1.ShapeSource{
		Methods: []string{"POST"}, PathPrefix: "/api",
		Constraints: []*artifactv1.ShapeConstraint{{Selector: "header.x-admin"}},
	}
	if err := validateShapeAgainstCluster(invalid, cluster); err == nil {
		t.Fatal("selector absent from pinned events must fail")
	}
}
