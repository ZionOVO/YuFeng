package edgecore

import (
	"testing"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestGateOrderPolicyFirst(t *testing.T) {
	if GateOrder(true, true, true) != ActionBlock {
		t.Fatal("policy should short-circuit")
	}
	if GateOrder(false, false, false) != ActionAllow {
		t.Fatal("none should allow")
	}
}

func TestCRSHitWithoutPolicyIsAllow(t *testing.T) {
	// 无策略时引擎命中本身不 403。
	if GateOrder(false, false, false) != ActionAllow {
		t.Fatal("engine hit without policy must allow")
	}
}

func TestPolicyMatchRequiresKey(t *testing.T) {
	found := []Detection{{RuleID: "942100", Location: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY}}
	cov := []Coverage{{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL}}
	if !PolicyMatchesKey(found, "942100", commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, false, cov) {
		t.Fatal("key should match")
	}
	if PolicyMatchesKey(found, "941100", commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, false, cov) {
		t.Fatal("other key must not match")
	}
}

func TestPolicyMatchesFullDetectionKey(t *testing.T) {
	found := []Detection{{
		InspectorID: "crs", RuleID: "942100", Version: "4.25.0",
		Location: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, Selector: "query.id",
	}}
	cov := []Coverage{{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL}}
	key := &commonv1.DetectionKey{
		DetectorId: "crs", DetectorVersion: "4.25.0", RuleId: "942100",
		TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, TargetSelector: "query.id",
	}
	if !PolicyMatchesDetectionKey(found, key, true, cov) {
		t.Fatal("full key should match")
	}
	key.DetectorVersion = "9.0.0"
	if PolicyMatchesDetectionKey(found, key, true, cov) {
		t.Fatal("other detector version must not match")
	}
}

func TestPolicyScopeRequiresHostAndMethod(t *testing.T) {
	scope := &artifactv1.PolicyScope{
		AssetId: "asset-1", Hosts: []string{"app.example"}, Methods: []string{"GET"},
		PathPrefix: "/api/", ContentTypes: []string{"application/json"},
	}
	view := Canonicalize("GET", "/api/items", "", map[string]string{"Host": "app.example", "Content-Type": "application/json"}, nil, DefaultInspectionProfile())
	if !PolicyScopeMatches(scope, Request{AssetID: "asset-1", Method: "GET", Path: "/api/items"}, view) {
		t.Fatal("in-scope request must match")
	}
	if PolicyScopeMatches(scope, Request{AssetID: "asset-2", Method: "GET", Path: "/api/items"}, view) {
		t.Fatal("other asset must not match")
	}
	post := Canonicalize("POST", "/api/items", "", map[string]string{"Host": "app.example", "Content-Type": "application/json"}, nil, DefaultInspectionProfile())
	if PolicyScopeMatches(scope, Request{AssetID: "asset-1", Method: "POST", Path: "/api/items"}, post) {
		t.Fatal("other method must not match")
	}
}

func TestCoverageErrorIsNotNoDetection(t *testing.T) {
	cov := []Coverage{CoverageError(commonv1.InspectionSurface_INSPECTION_SURFACE_BODY)}
	if IsNoDetection(cov, nil) {
		t.Fatal("ERROR must not count as no detection")
	}
}

func TestAutoCanarySingleUnitForbidden(t *testing.T) {
	if AutoCanaryAllowed(1, 5) {
		t.Fatal("single unit cannot auto canary at 5%")
	}
	if !AutoCanaryAllowed(20, 5) {
		t.Fatal("20 units can form 5% buckets")
	}
}
