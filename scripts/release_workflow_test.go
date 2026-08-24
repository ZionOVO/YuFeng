package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopAndMainUseDifferentVerificationWorkflows(t *testing.T) {
	continuousIntegration := readRepositoryFile(t, ".github/workflows/ci.yml")
	for _, want := range []string{"push:\n    branches: [develop]", `git rev-list --parents -n 1 "$GITHUB_SHA"`, `git diff --quiet "$source_parent" "$GITHUB_SHA"`, "actions/workflows/pull-request.yml/runs", `conclusion" != "success"`} {
		if !strings.Contains(continuousIntegration, want) {
			t.Errorf("develop continuous integration missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"branches: [main]",
		"pull_request:",
		"proto:",
		"console:",
		"cross-compile:",
		"actions/setup-go",
		"image: postgres",
		"go build ./...",
		"go test",
		"go vet ./...",
		"gofmt -l",
		"staticcheck@v0.8.0",
		"golangci-lint",
		"govulncheck",
		"production-end-to-end.sh",
		"fault-injection-end-to-end.sh",
		"onboarding-live.sh static",
	} {
		if strings.Contains(continuousIntegration, forbidden) {
			t.Errorf("develop continuous integration repeats covered check %q", forbidden)
		}
	}

	pullRequest := readRepositoryFile(t, ".github/workflows/pull-request.yml")
	for _, want := range []string{"pull_request:\n    branches: [develop]", "github.event.pull_request.base.sha", "make build test", "gofmt -l", "golangci-lint@v2.13.1", "buf breaking", "npm run build", "GOARCH=mips", "YUFENG_TEST_DSN", "image: postgres", "Create restricted traffic writer role"} {
		if !strings.Contains(pullRequest, want) {
			t.Errorf("develop pull request gate missing %q", want)
		}
	}
	for _, forbidden := range []string{"make build test vet", "go test -race", "govulncheck", "production-end-to-end.sh"} {
		if strings.Contains(pullRequest, forbidden) {
			t.Errorf("develop pull request gate must not run expensive post-merge check %q", forbidden)
		}
	}

	releaseGate := readRepositoryFile(t, ".github/workflows/release-gate.yml")
	for _, want := range []string{"pull_request:\n    branches: [main]", "main only accepts the repository develop branch", `merge_base="$(git merge-base`, `git diff --quiet "$merge_base" "$BASE_SHA"`, "actions/workflows/ci.yml/runs", `conclusion" != "success"`, `release-metadata.py get --file "$RUNNER_TEMP/release-body.txt" --field evidence-tree`, `test "$evidence_tree" = "$head_tree"`} {
		if !strings.Contains(releaseGate, want) {
			t.Errorf("main release gate missing %q", want)
		}
	}
	for _, forbidden := range []string{"go test -race", "production-end-to-end.sh", "npm test"} {
		if strings.Contains(releaseGate, forbidden) {
			t.Errorf("main release gate must not repeat full verification step %q", forbidden)
		}
	}
}

func TestReleaseArtifactsRequireVersionTagFromMain(t *testing.T) {
	release := readRepositoryFile(t, ".github/workflows/release.yml")
	for _, want := range []string{
		"workflow_dispatch:",
		"release_version:",
		`"v[0-9]*.[0-9]*.[0-9]*"`,
		`[[ "$RELEASE_TAG" =~ ^v(0|[1-9][0-9]*)`,
		`test "$RELEASE_TAG" = "$(tr -d '[:space:]' < VERSION)"`,
		`git ls-remote --tags origin`,
		`test "$remote_tag_commit" = "$GITHUB_SHA"`,
		"sort -V",
		`test "$GITHUB_SHA" = "$(git rev-parse origin/main)"`,
		`test "${#parents[@]}" -eq 3`,
		`git merge-base --is-ancestor "$develop_parent" origin/develop`,
		"actions/workflows/ci.yml/runs",
		`conclusion" != "success"`,
		"release/linux-amd64/bin",
		"release/linux-arm64/bin",
		"release/linux-mips/bin",
		"release/windows-amd64/bin",
		"release/darwin-amd64/bin",
		"release/darwin-arm64/bin",
		"deploy/agentd/Install-Windows.ps1",
		"deploy/agentd/install-linux.sh",
		"deploy/agentd/install-macos.sh",
		"cp deploy/agentd/README.md",
		"deploy/edge/install-linux.sh",
		"deploy/edge/yufeng-edge.service",
		"python3 -m pip wheel --no-deps",
		"deploy/modelside/yufeng-modelside.service",
		"deploy/compose.edge-modelside.yaml",
		"docker build --platform linux/amd64",
		"deploy/edge.Dockerfile",
		"components/modelside/Dockerfile",
		`docker save "yufeng-edge:${RELEASE_TAG}"`,
		`docker save "yufeng-modelside:${RELEASE_TAG}"`,
		"yufeng-${RELEASE_TAG}-modelside-python.tar.gz",
		"yufeng-${RELEASE_TAG}-deployment.tar.gz",
		"yufeng-${RELEASE_TAG}-edge-image-linux-amd64.tar.gz",
		"yufeng-${RELEASE_TAG}-modelside-image-linux-amd64.tar.gz",
		`GOARCH="$architecture" go build -trimpath -ldflags "$linker_flags"`,
		"sha256sum",
		"gh release create",
		"--draft",
		"scripts/release-metadata.py verify",
		"scripts/release-evidence.py verify",
		`--expected-base-commit "$EVIDENCE_BASE_PARENT"`,
		`--expected-source-commit "$EVIDENCE_SOURCE_PARENT"`,
		"yufeng-${RELEASE_TAG}-live-evidence.tar.gz",
		"yufeng-${RELEASE_TAG}-live-evidence.tar.gz.sha256",
		"yufeng-${RELEASE_TAG}-live-evidence.json",
		`gh release edit "$RELEASE_TAG" --draft=false --latest`,
	} {
		if !strings.Contains(release, want) {
			t.Errorf("tagged release workflow missing %q", want)
		}
	}
	if strings.Contains(release, `git cat-file -t "$GITHUB_REF"`) {
		t.Error("tagged release workflow must not trust the locally peeled tag reference")
	}
	if strings.Contains(release, `gh release create "$RELEASE_TAG" release/*.tar.gz release/*-checksums.txt --verify-tag --generate-notes --title "$RELEASE_TAG"`) {
		t.Error("tag push must create a draft release rather than publishing immediately")
	}
}

func TestReleaseGateRequiresExactEvidenceMetadata(t *testing.T) {
	releaseGate := readRepositoryFile(t, ".github/workflows/release-gate.yml")
	for _, want := range []string{
		"github.event.pull_request.body",
		"scripts/release-metadata.py verify",
		`--expected-version "$(tr -d '[:space:]' < VERSION)"`,
		`--expected-commit "$HEAD_SHA"`,
		`--expected-tree "$head_tree"`,
	} {
		if !strings.Contains(releaseGate, want) {
			t.Errorf("main release gate missing evidence check %q", want)
		}
	}
}

func readRepositoryFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
