package brain

import (
	"strings"
	"testing"
	"time"

	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"

	"yufeng/lib/kernel"
)

func TestClusterIdentityIgnoresCoverageAndTime(t *testing.T) {
	a := ClusterIdentity("asset-1", "GET", "/api/items", "key:942100:query.id")
	b := ClusterIdentity("asset-1", "GET", "/api/items", "key:942100:query.id")
	if a != b || a == "" {
		t.Fatalf("same identity must match: %s %s", a, b)
	}
	// 覆盖度不在入参里：PARTIAL 与 FULL 只要键相同就必须同一身份。
	e1 := &eventv1.Event{
		AssetId: "asset-1",
		Traffic: &eventv1.Event_Http{Http: &eventv1.Http{Method: "GET", Path: "/api/items"}},
		Detections: []*eventv1.Detection{{
			RuleId: "942100",
			Key:    &commonv1.DetectionKey{RuleId: "942100", TargetSelector: "query.id"},
		}},
		Coverage: []*commonv1.InspectionCoverage{{
			Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			Status: commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL,
		}},
	}
	e2 := &eventv1.Event{
		AssetId: "asset-1",
		Traffic: &eventv1.Event_Http{Http: &eventv1.Http{Method: "GET", Path: "/api/items"}},
		Detections: []*eventv1.Detection{{
			RuleId: "942100",
			Key:    &commonv1.DetectionKey{RuleId: "942100", TargetSelector: "query.id"},
		}},
		Coverage: []*commonv1.InspectionCoverage{{
			Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL,
		}},
	}
	if EventClusterKey(e1) != EventClusterKey(e2) {
		t.Fatal("coverage must not enter cluster key")
	}
	if ClusterIdentity(e1.AssetId, "GET", RouteTemplate(e1.GetHttp().Path), EventClusterKey(e1)) !=
		ClusterIdentity(e2.AssetId, "GET", RouteTemplate(e2.GetHttp().Path), EventClusterKey(e2)) {
		t.Fatal("coverage must not split cluster identity")
	}
}

func TestAppendRepresentativesCapsAtFive(t *testing.T) {
	var ids []string
	for i := 0; i < 8; i++ {
		ids = AppendRepresentatives(ids, "e"+string(rune('0'+i)))
	}
	if len(ids) != kernel.ClusterRepresentatives {
		t.Fatalf("len=%d want %d", len(ids), kernel.ClusterRepresentatives)
	}
	again := AppendRepresentatives(ids, "e0")
	if len(again) != kernel.ClusterRepresentatives {
		t.Fatal("duplicate must not grow")
	}
}

func TestClusterOpenIdle(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if ClusterOpen(false, time.Time{}, now) != true {
		t.Fatal("missing cluster must open")
	}
	if ClusterOpen(true, now.Add(-time.Minute), now) {
		t.Fatal("fresh cluster must stay open")
	}
	if !ClusterOpen(true, now.Add(-kernel.ClusterIdle), now) {
		t.Fatal("idle cluster must close")
	}
}

func TestRouteTemplateCollapsesIDs(t *testing.T) {
	if got := RouteTemplate("/api/items/42"); got != "/api/items/{id}" {
		t.Fatalf("got %s", got)
	}
	if strings.Contains(ClusterIdentity("a", "GET", RouteTemplate("/api/items/1"), "key:1"), "time") {
		t.Fatal("identity must not mention time")
	}
}
