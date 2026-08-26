package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func onboardingLiveScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller information is unavailable")
	}
	return filepath.Join(filepath.Dir(file), "onboarding-live.sh")
}

func onboardingStaticCommand(t *testing.T) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell checks run in the canonical Linux continuous integration job")
	}
	cmd := exec.Command("sh", onboardingLiveScript(t), "static")
	cmd.Dir = filepath.Dir(filepath.Dir(onboardingLiveScript(t)))
	// 外层 go test 已单独执行 deploy 包，这里只验证脚本扫描，避免嵌套重复编译。
	cmd.Env = append(os.Environ(), "YUFENG_SKIP_DEPLOY_GO_TESTS=1")
	return cmd
}

func TestOnboardingLiveStaticScansSeparateControlAndDataPlaneComposeFiles(t *testing.T) {
	cmd := onboardingStaticCommand(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("static against shipped compose files: %v\n%s", err, out)
	}
	for _, want := range []string{"compose static ok", "onboarding static ok"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestOnboardingLiveStaticRejectsEdgeInControlPlaneCompose(t *testing.T) {
	root := filepath.Dir(filepath.Dir(onboardingLiveScript(t)))
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(raw), "services:\n", "services:\n  edge:\n    image: yufeng-edge:test\n", 1)
	if mutated == string(raw) {
		t.Fatal("failed to insert Edge service")
	}
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := onboardingStaticCommand(t)
	cmd.Env = append(cmd.Env, "COMPOSE_FILE="+path)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("control plane Edge service must fail static scan: %s", out)
	}
}

func TestOnboardingLiveStaticRejectsDataPlaneWithoutUnixSocket(t *testing.T) {
	root := filepath.Dir(filepath.Dir(onboardingLiveScript(t)))
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "compose.edge-modelside.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.ReplaceAll(string(raw), "unix:///run/yufeng/modelside.sock", "https://modelside.example:9443")
	if mutated == string(raw) {
		t.Fatal("failed to remove Unix domain socket")
	}
	path := filepath.Join(t.TempDir(), "compose.edge-modelside.yaml")
	if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := onboardingStaticCommand(t)
	cmd.Env = append(cmd.Env, "YUFENG_EDGE_COMPOSE_FILE="+path)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("same-host data plane without Unix socket must fail static scan: %s", out)
	}
}

func TestOnboardingLiveKeycheckAllowsMissingSecret(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell checks run in the canonical Linux continuous integration job")
	}
	cmd := exec.Command("sh", onboardingLiveScript(t), "keycheck")
	cmd.Dir = filepath.Dir(filepath.Dir(onboardingLiveScript(t)))
	filtered := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "YUFENG_MODEL_API_KEY=") {
			filtered = append(filtered, value)
		}
	}
	cmd.Env = append(filtered, "YUFENG_SKIP_DOTENV=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("missing optional key must be allowed: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "will not send a provider authentication header") {
		t.Fatalf("output=%s", out)
	}
}

func TestOnboardingLiveLeavesEdgeLifecycleWithOperator(t *testing.T) {
	body := readScript(t, "onboarding-live.sh")
	for _, required := range []string{
		"/yufeng.asset.v1.AssetService/PutEdgeEnrollment",
		"/yufeng.asset.v1.AssetService/CreateAsset",
		"/yufeng.asset.v1.AssetService/GetEdgeEnrollment",
		"deploy/compose.edge-modelside.yaml",
		"operator action: start the separately delivered Edge and ModelSide services",
		"up -d --build modelside edge",
		"YUFENG_MODELSIDE_WEIGHTS_DIR",
		"edge_admin_port=${YUFENG_EDGE_ADMIN_PORT:-19092}",
		"${unit_id}-modelside",
		`"EDGE_ENROLLMENT_STATUS_ONLINE"`,
		"http://127.0.0.1:19092/ready",
		`http://127.0.0.1:${edge_admin_port}/ready`,
		`"modelProfile"`,
		`"reviewWindowSeconds": 300`,
		`"maxReviewPerUnit": 4`,
		`"maxReviewPerRoute": 1`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("manual onboarding script missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"PutDeploymentSpecification", "DeployDataplane", "ONBOARDING_DEPLOY", "unit.ensure_local", "generation.publish_baseline", "edge.probe",
		"docker rm -f", `"edgeReady"`, "dataplaneReady",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("manual onboarding script retains forbidden lifecycle path %q", forbidden)
		}
	}
}

func TestOnboardingLiveVerifiesEnrollmentHeartbeatAndCurrentSignedGeneration(t *testing.T) {
	body := readScript(t, "onboarding-live.sh")
	for _, required := range []string{
		"/yufeng.asset.v1.AssetService/GetEdgeEnrollment",
		`last.get("status") == "EDGE_ENROLLMENT_STATUS_ONLINE"`,
		`last.get("currentGenerationId", "")`,
		`last.get("currentGenerationSeq")`,
		`last.get("expectedGenerationId", "")`,
		`last.get("expectedGenerationSeq")`,
		`local.get("generation_id", "")`,
		`local.get("generation_seq")`,
		`local.get("listen_plan_version")`,
		`last.get("lastHeartbeatAt", "")`,
		"current_sequence == expected_sequence",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("completed onboarding verification missing %q", required)
		}
	}
}

func TestOnboardingLiveMutationsCarryIdempotencyKeys(t *testing.T) {
	body := readScript(t, "onboarding-live.sh")
	for _, call := range []string{
		`PutModelConfig" "$model_body" "$token" 1`,
		`TestModelConnectivity" "{}" "$token" 1`,
		`CompleteOnboarding" "{}" "$token" 1`,
		`CreateAsset" "$create_body" "$token" 1`,
		`PutEdgeEnrollment" "$enrollment" "$token" 1`,
	} {
		if !strings.Contains(body, call) {
			t.Errorf("state-changing onboarding call missing idempotency key: %s", call)
		}
	}
}

func TestOnboardingLiveResetIsExplicitAndCoversBothDeliveries(t *testing.T) {
	body := readScript(t, "onboarding-live.sh")
	for _, required := range []string{
		`if [ "${YUFENG_LIVE_RESET:-}" = "1" ]`,
		`-f "$control_compose" -f "$data_compose" -f "$test_compose" down -v --remove-orphans`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("explicit reset missing %q", required)
		}
	}
}
