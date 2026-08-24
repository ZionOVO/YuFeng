package scripts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEndToEndScriptsAreFilteredGoTestsNotLiveStacks(t *testing.T) {
	production := readScript(t, "production-end-to-end.sh")
	fault := readScript(t, "fault-injection-end-to-end.sh")
	for name, body := range map[string]string{"production-end-to-end.sh": production, "fault-injection-end-to-end.sh": fault} {
		if !strings.Contains(body, "go test") {
			t.Errorf("%s must be filtered go test", name)
		}
		if !strings.Contains(body, "onboarding-live") && !strings.Contains(body, "compose-live") {
			t.Errorf("%s must defer live-stack proof to onboarding-live / compose-live", name)
		}
		if strings.Contains(body, "当作 compose 全栈") || strings.Contains(body, "作为 compose 全栈") {
			t.Errorf("%s must not treat itself as compose full stack proof", name)
		}
		if strings.Contains(body, "make compose-up") {
			t.Errorf("%s must not treat make compose-up as its proof", name)
		}
	}
	if strings.Contains(production, "yufeng-jarvis") && !strings.Contains(production, "不得") {
		t.Fatal("production-end-to-end.sh must not present jarvis live stack as its proof")
	}
	for _, required := range []string{"TestValidateResultAgainstSignedProfile", "TestModelResultIngestionIsAtomicIdempotentAndBounded"} {
		if !strings.Contains(production, required) {
			t.Errorf("production end-to-end gate must execute %q", required)
		}
	}
	if !strings.Contains(fault, "TestModelResultIngestionIsAtomicIdempotentAndBounded") {
		t.Error("fault injection gate must execute typed model result idempotency")
	}
	for _, retired := range []string{"TestModelScore", "TestApplyModelInference", "TestStartModelLoop"} {
		if strings.Contains(production, retired) || strings.Contains(fault, retired) {
			t.Errorf("end-to-end scripts must not silently select retired test %q", retired)
		}
	}
}

func TestResilienceLiveUsesExistingRealDeployment(t *testing.T) {
	body := readScript(t, "resilience-live.sh")
	for _, want := range []string{
		`control stop brain`,
		`data restart edge`,
		`operator action: restart Edge while Brain is offline`,
		`spool_lines`,
		`distinct_event_count`,
		`resilience-duplicate.ndjson`,
		`direct-origin-rollback`,
		`generation_id`,
		`edge_admin_port=${YUFENG_EDGE_ADMIN_PORT:-19092}`,
		`http://127.0.0.1:${edge_admin_port}/ready`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("resilience live script missing %q", want)
		}
	}
	for _, forbidden := range []string{"down -v", "docker rm -f", "make compose-up", "onboarding-live.sh live\n", "supervisor_name", "yufeng-dataplane"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("resilience drill must preserve the prepared deployment, found %q", forbidden)
		}
	}
}

func TestSecurityLiveCoversIdentitySecretsAndLeakage(t *testing.T) {
	body := readScript(t, "security-live.sh")
	for _, want := range []string{
		`PromoteEnforce`,
		`RegistryService/Register`,
		`TestDualTokenGatewayTable`,
		`TestInvokeToolIdempotencyNoDoubleBudget`,
		`socket_holders == []`,
		`modelside_result_token`,
		`unit_bootstrap_token`,
		`--brain-token-file`,
		`--weights`,
		`-signing-key`,
		`YUFENG_MODEL_API_KEY`,
		`pg_dump`,
		`request secrets stay out`,
		`TestTrafficPoolUsesRestrictedRole`,
		`traffic.traffic_window_receipts`,
		`INSERT INTO releases`,
		`UPDATE audit_entries`,
		`42501`,
		`security_probe_dir=$(mktemp -d`,
		`-bootstrap-admin-pass-file`,
		`-agent-bootstrap-token-file`,
		`-unit-bootstrap-token-file`,
		`-modelside-token-file`,
		`-dsn=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("security live script missing %q", want)
		}
	}
	for _, forbidden := range []string{"down -v", "docker rm -f", "YUFENG_MODEL_API_KEY=", "yufeng-dataplane", "/v1/probe"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("security drill must not reset data or inject a model key, found %q", forbidden)
		}
	}
}

func TestTrafficReviewLiveReachesShadowAndCleansTemporaryCapability(t *testing.T) {
	body := readScript(t, "traffic-review-live.sh")
	for _, want := range []string{
		`TRAFFIC_REVIEW_MODE_STATISTICS_ONLY`,
		`TRAFFIC_REVIEW_MODE_REDACTED_CASES`,
		`TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL`,
		`TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES`,
		`case.get`,
		`case.request_evidence`,
		`run.create`,
		`AgentInteractionService/DecideApproval`,
		`INVESTIGATION_CASE_STATE_SHADOW_OBSERVING`,
		`lastWorkerId`,
		`RELEASE_STATE_SHADOW`,
		`GovernService/GetReleaseTimeline`,
		`GovernService/RetireRelease`,
		`AGENT_PROFILE_STATE_DISABLED`,
		`TRAFFIC_REVIEW_MODE_OFF`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("traffic review live script missing %q", want)
		}
	}
	for _, forbidden := range []string{"down -v", "docker rm -f", "YUFENG_LIVE_RESET", "FakeProvider", "YUFENG_MODEL_API_KEY="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("traffic review drill must preserve pilot data and use the real model, found %q", forbidden)
		}
	}
}

func TestPerformanceLiveMeasuresFrozenDeploymentBudgets(t *testing.T) {
	body := readScript(t, "performance-live.sh")
	for _, want := range []string{
		`TestModelIngressWindowCapacityMatrix`,
		`YUFENG_RUN_MODEL_BYPASS_PERFORMANCE=1`,
		`model-ingress-window-capacity/v2`,
		`model_bypass_p99_micros`,
		`model_bypass_cpu_percent`,
		`target != 2000`,
		`p99_budget != 1000`,
		`bypass_disabled`,
		`modelside_idle`,
		`modelside_stable`,
		`modelside_full`,
		`modelside_unreachable`,
		`near_inspection_limit`,
		`local_hard_limit`,
		`modelside_rejected`,
		`transport_failed`,
		`load_generator_dropped`,
		`throughput_budget_met`,
		`p99_budget_met`,
		`cpu_budget_met`,
		`resident_memory_budget_met`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("performance live script missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"down -v",
		"docker rm -f",
		"yufeng-dataplane",
		"TRAFFIC_REVIEW_MODE_STATISTICS_ONLY",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("performance drill must isolate the bounded model bypass without mutating deployment state, found %q", forbidden)
		}
	}
}

func readScript(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
