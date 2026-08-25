package scripts

import (
	"strings"
	"testing"
)

func TestTrafficReviewUsesTheCurrentSignedGenerationAfterCompletedOnboarding(t *testing.T) {
	body := readScript(t, "traffic-review-live.sh")
	for _, want := range []string{
		"/yufeng.asset.v1.AssetService/GetEdgeEnrollment",
		`enrollment.get("lastHeartbeatAt", "")`,
		`enrollment.get("currentGenerationId", "")`,
		`enrollment.get("currentGenerationSeq")`,
		`enrollment.get("expectedGenerationSeq")`,
		`enrollment.get("status") == "EDGE_ENROLLMENT_STATUS_ONLINE"`,
		`local.get("generation_id", "")`,
		`local.get("listen_plan_version")`,
		"YUFENG_EDGE_ADMIN_PORT",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("completed traffic review readiness missing %q", want)
		}
	}
	for _, forbidden := range []string{`onboarding.get("edgeReady")`, `onboarding.get("localAssetId")`, `onboarding.get("localUnitId")`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("completed traffic review must not read retired onboarding field %q", forbidden)
		}
	}
}
