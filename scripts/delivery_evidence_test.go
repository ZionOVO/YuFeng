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
		"runs-on: ubuntu-24.04",
		"make build test vet",
		"golangci-lint@v2.13.1",
		"govulncheck@v1.7.0",
		"protoc-gen-go@v1.36.12",
		"protoc-gen-connect-go@v1.20.0",
		`version: "1.72.0"`,
		"buf breaking",
		"npm run build",
		"GOARCH=mips",
		"Compile tests for Windows and both macOS architectures",
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

func TestDevelopmentPlatformCompatibilityRemainsAdvisory(t *testing.T) {
	body, err := os.ReadFile("../.github/workflows/compatibility.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, want := range []string{
		"name: development-platform-compatibility",
		"workflow_dispatch:",
		"schedule:",
		"ubuntu-22.04-low-resource",
		"windows-2022-low-resource",
		"macos-15-intel-low-resource",
		"GO_TEST_FLAGS='-p=1'",
		"development-check.ps1 -Parallelism 1",
		"npm test -- --maxWorkers=1",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("development compatibility workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"pull_request:", "push:", "needs:"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("advisory compatibility workflow must not become a merge gate through %q", forbidden)
		}
	}
}

func TestDevelopmentChecksAreStableAcrossLocalWorkspaces(t *testing.T) {
	makefile, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makeText := string(makefile)
	for _, want := range []string{
		"GO_PACKAGES := ./agents/...",
		"./console ./deploy ./docs",
		"GO_TEST_FLAGS ?=",
		"go test $(GO_TEST_FLAGS) $(GO_PACKAGES)",
	} {
		if !strings.Contains(makeText, want) {
			t.Errorf("Makefile cross-device check missing %q", want)
		}
	}
	if strings.Contains(makeText, "go test ./...") {
		t.Fatal("development checks must not scan ignored node_modules packages")
	}

	powershell, err := os.ReadFile("development-check.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[int]$Parallelism = 1", "'./console'", "go test -p $Parallelism @packagePatterns"} {
		if !strings.Contains(string(powershell), want) {
			t.Errorf("Windows development check missing %q", want)
		}
	}

	attributes, err := os.ReadFile("../.gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(attributes), "* text=auto eol=lf") || !strings.Contains(string(attributes), "*.ps1 text eol=crlf") {
		t.Fatal("cross-device line endings are not fixed")
	}
}

func TestReleaseVersionSourcesAgree(t *testing.T) {
	versionRaw, err := os.ReadFile("../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(versionRaw))
	plainVersion := strings.TrimPrefix(version, "v")
	if plainVersion == "" || plainVersion == version {
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
		if document.Version != plainVersion {
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
			if lock.Packages[""].Version != plainVersion {
				t.Errorf("%s root package version=%q", path, lock.Packages[""].Version)
			}
		}
	}
	for path, want := range map[string]string{
		"../components/modelside/pyproject.toml":               `version = "` + plainVersion + `"`,
		"../components/modelside/yufeng_modelside/__init__.py": `__version__ = "` + plainVersion + `"`,
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
