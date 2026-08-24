package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestDeliveryEvidenceCoversReleaseGates(t *testing.T) {
	body := readScript(t, "delivery-evidence.sh")
	wants := []string{
		"go1.27.0",
		"golangci-lint@v2.13.1",
		"go test -race ./...",
		"go test -tags yufeng_dev",
		"gofmt -l",
		"govulncheck@v1.7.0",
		"govulncheck",
		"golangci-lint",
		"buf breaking",
		"npm run build",
		"onboarding-live.sh live",
		"security-live.sh live",
		"traffic-review-live.sh live",
		"resilience-live.sh live",
		"performance-live.sh live",
		"envoy/run-integration.sh",
		"backup-restore-live.sh live",
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("delivery evidence script missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"make build test",
		"make build test vet",
		"staticcheck@v0.8.0",
		"production-end-to-end.sh",
		"fault-injection-end-to-end.sh",
		"onboarding-live.sh static",
		"resilience-live.sh static",
		"security-live.sh static",
		"performance-live.sh static",
		"backup-restore-live.sh static",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("delivery evidence repeats a covered check %q", forbidden)
		}
	}
}

func TestDeliveryToolchainMatchesRepository(t *testing.T) {
	checks := map[string][]string{
		"../go.mod":                             {"go 1.27.0"},
		"../.github/workflows/pull-request.yml": {`go-version: "1.27.0"`, "protoc-gen-go@v1.36.12", "protoc-gen-connect-go@v1.20.0", "golangci-lint@v2.13.1", "github.event.pull_request.base.sha", "branch=buf-breaking-baseline,subdir=proto"},
		"../.github/workflows/ci.yml":           {"actions/workflows/pull-request.yml/runs", `jq -r .before "$GITHUB_EVENT_PATH"`},
		"delivery-evidence.sh":                  {"go1.27.0", "govulncheck@v1.7.0", "golangci-lint@v2.13.1", "branch=main,subdir=proto"},
	}
	for path, wants := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		if strings.Contains(string(raw), "tag=v0.0.1") {
			t.Errorf("%s must not depend on a missing release tag", path)
		}
	}
}

func TestDeliveryEvidenceDoesNotResetPilotData(t *testing.T) {
	body := readScript(t, "delivery-evidence.sh")
	for _, forbidden := range []string{"compose down", "down --volumes", "compose-live-reset", "docker volume rm"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("delivery evidence must preserve pilot data, found %q", forbidden)
		}
	}
}

func TestReleasePreflightRunsTheLocalStaticListOnExpectedMergeTree(t *testing.T) {
	body := readScript(t, "preflight-release-evidence.sh")
	for _, want := range []string{
		`git status --porcelain=v1 --untracked-files=all`,
		`git rev-parse origin/develop`,
		`release/${version}`,
		`git merge-tree --write-tree`,
		`git commit-tree`,
		`git worktree add --detach`,
		`promotion-probe`,
		`release environment fingerprint depends on the checkout path`,
		`delivery-evidence.sh static`,
		`hot_path_prototype_benchmark_test.go`,
		`-benchmem`,
		`-benchtime=250ms`,
		`-count=5`,
		`COPYFILE_DISABLE=1 tar`,
		`release-environment.py`,
		`YUFENG_EDGE_UNIT`,
		`YUFENG_MODELSIDE_ID`,
		`YUFENG_MODELSIDE_WEIGHTS_DIR`,
		`release-evidence.py scan`,
		`verify-preflight`,
		`preflight-evidence.tar.gz.sha256`,
		`yufeng.release-preflight/v1`,
		`preflight-result`,
		`expires-at`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release preflight missing %q", want)
		}
	}
	for _, forbidden := range []string{"YUFENG_LIVE_RESET", "down -v", "docker volume rm", "docker volume prune"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("release preflight must preserve pilot data, found %q", forbidden)
		}
	}
}

func TestReleaseEnvironmentFingerprintBindsDeploymentIdentities(t *testing.T) {
	body := readScript(t, "release-environment.py")
	for _, want := range []string{
		`"deployment-identities"`,
		`"edge-unit": edge_unit`,
		`edge_asset = os.environ.get("YUFENG_EDGE_ASSET"`,
		`"edge-asset": edge_asset`,
		`"modelside-id": modelside_id`,
		`"configured-service-hashes"`,
		`"--no-path-resolution"`,
		`"--hash"`,
		`"configured-services": sorted(`,
		`"configured-images": sorted(`,
		`"configured-service-hashes": sorted(`,
		`release environment requires Node.js`,
		`--require-node-major`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release environment fingerprint missing %q", want)
		}
	}
}

func TestEvidencePromotionOnlyBindsFinalDevelopCommit(t *testing.T) {
	body := readScript(t, "archive-live-evidence.sh")
	for _, want := range []string{
		`git rev-parse origin/develop`,
		`git rev-list --parents -n 1`,
		`release-environment.py capture`,
		`release-evidence.py verify-preflight`,
		`actions/runs/`,
		`git rev-parse HEAD^{tree}`,
		`pg_dump`,
		`delivery-evidence.sh live`,
		`yufeng.release-evidence/v2`,
		`merge-parents`,
		`release-metadata.py`,
		`release-evidence.py verify`,
		`live-evidence.tar.gz.sha256`,
		`evidence-result=passed`,
		`COPYFILE_DISABLE=1 tar`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("evidence promotion missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"delivery-evidence.sh static",
		"go test -race",
		"go test -tags",
		"-benchmem",
		"npm test",
		"buf breaking",
		"govulncheck",
		"golangci-lint",
		"YUFENG_LIVE_RESET",
		"down -v",
		"docker volume rm",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("evidence promotion must not rerun or reset %q", forbidden)
		}
	}
}

func TestReleaseVersionHasOneRepositorySource(t *testing.T) {
	raw, err := os.ReadFile("../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "v0.2.0\n" {
		t.Fatalf("VERSION=%q", raw)
	}
}
