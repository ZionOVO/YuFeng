package scripts

import (
	"strings"
	"testing"
)

func TestTrafficReviewUsesTheCurrentSignedGenerationAfterCompletedOnboarding(t *testing.T) {
	body := readScript(t, "traffic-review-live.sh")
	for _, want := range []string{
		"/yufeng.asset.v1.AssetService/GetAsset",
		`unit.get("lastHeartbeatAt", "")`,
		`unit.get("currentGenerationId", "")`,
		`unit.get("currentGenerationSeq")`,
		`onboarding.get("expectedGenerationSeq")`,
		`local.get("generation_id", "")`,
		`local.get("listen_plan_version")`,
		"YUFENG_EDGE_ADMIN_PORT",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("completed traffic review readiness missing %q", want)
		}
	}
	if strings.Contains(body, `onboarding.get("edgeReady") is not True`) {
		t.Error("completed traffic review must not require the stale initial generation readiness bit")
	}
}
