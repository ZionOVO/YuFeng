package scripts

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDeploymentEvidenceRemainsDiagnostic(t *testing.T) {
	body := readScript(t, "delivery-evidence.sh")
	for _, want := range []string{
		"go1.27.0",
		"go test -race ./...",
		"go test -tags yufeng_dev",
		"gofmt -l",
		"govulncheck@v1.7.0",
		"golangci-lint@v2.13.1",
		"buf breaking",
		"npm run build",
		"onboarding-live.sh live",
		"security-live.sh live",
		"traffic-review-live.sh live",
		"resilience-live.sh live",
		"performance-live.sh live",
		"envoy/run-integration.sh",
		"backup-restore-live.sh live",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("deployment evidence script missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"gh release",
		"git tag",
		"release-artifacts.py",
		"preflight-release-evidence",
		"archive-live-evidence",
		"compose down",
		"down --volumes",
		"compose-live-reset",
		"docker volume rm",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("deployment evidence must not mutate software release or pilot data, found %q", forbidden)
		}
	}
}

func TestContinuousIntegrationUsesOneFiniteRequiredResult(t *testing.T) {
	body, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, want := range []string{
		"name: continuous-integration",
		"pull_request:\n    branches: [main]",
		"push:\n    branches: [main]",
		`go-version: "1.27.0"`,
		"make build test vet",
		"golangci-lint@v2.13.1",
		"govulncheck@v1.7.0",
		"protoc-gen-go@v1.36.12",
		"protoc-gen-connect-go@v1.20.0",
		"buf breaking",
		"npm run build",
		"GOARCH=mips",
		"name: required",
		"needs: [go, quality, proto, console, cross-compile]",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("continuous integration workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"branches: [develop]", "release-gate.yml", "pull-request.yml"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("continuous integration retains retired topology %q", forbidden)
		}
	}
}

func TestReleaseVersionSourcesAgree(t *testing.T) {
	versionRaw, err := os.ReadFile("../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if string(versionRaw) != "v0.1.0\n" {
		t.Fatalf("VERSION=%q", versionRaw)
	}

	for _, path := range []string{"../console/package.json", "../console/package-lock.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		if document.Version != "0.1.0" {
			t.Errorf("%s version=%q", path, document.Version)
		}
		if path == "../console/package-lock.json" {
			var lock struct {
				Packages map[string]struct {
					Version string `json:"version"`
				} `json:"packages"`
			}
			if err := json.Unmarshal(raw, &lock); err != nil {
				t.Fatal(err)
			}
			if lock.Packages[""].Version != "0.1.0" {
				t.Errorf("%s root package version=%q", path, lock.Packages[""].Version)
			}
		}
	}
	for path, want := range map[string]string{
		"../components/modelside/pyproject.toml":               `version = "0.1.0"`,
		"../components/modelside/yufeng_modelside/__init__.py": `__version__ = "0.1.0"`,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s missing %q", path, want)
		}
	}
}
