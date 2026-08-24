package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseWorkflowPromotesOneImmutableBundle(t *testing.T) {
	release := readRepositoryContractFile(t, ".github/workflows/release.yml")
	for _, want := range []string{
		"name: software-release",
		"workflow_dispatch:",
		"bundle_run_id:",
		"github.ref == 'refs/heads/main'",
		"actions/workflows/ci.yml/runs?branch=main&event=push",
		"./scripts/build-release-assets.sh",
		"./scripts/verify-release-assets.sh",
		"actions/upload-artifact@v4",
		"overwrite: false",
		"actions/download-artifact@v4",
		"gh run download \"$BUNDLE_RUN_ID\"",
		"cmp \"$BUNDLE_DIR/$asset\"",
		"git tag -a \"$RELEASE_TAG\"",
		"--verify-tag --draft",
		"gh release upload \"$RELEASE_TAG\" \"$BUNDLE_DIR/$asset\"",
		"gh release download \"$RELEASE_TAG\" --dir \"$downloaded\"",
		"gh release edit \"$RELEASE_TAG\" --draft=false --latest",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("software release workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"push:\n    tags:",
		"--clobber",
		"origin/develop",
		"release-evidence.py",
		"release-metadata.py",
		"live-evidence",
	} {
		if strings.Contains(release, forbidden) {
			t.Errorf("software release workflow retains non-convergent behavior %q", forbidden)
		}
	}
	upload := strings.Index(release, "actions/upload-artifact@v4")
	tag := strings.Index(release, "git tag -a")
	if upload < 0 || tag < 0 || upload > tag {
		t.Fatal("immutable workflow bundle must be stored before creating the release tag")
	}
}

func TestReleaseScriptsBuildAndValidateTheDeclaredAssetSet(t *testing.T) {
	build := readScript(t, "build-release-assets.sh")
	verify := readScript(t, "verify-release-assets.sh")
	contract := readScript(t, "release-artifacts.py")
	for _, want := range []string{
		"release output already exists",
		"release build requires a clean worktree",
		"linux_commands=(yfctl yufeng-agentd yufeng-brain yufeng-dataplane yufeng-edge yufeng-host yufeng-jarvis yufeng-run)",
		"GOARCH=\"$target_arch\"",
		"python3 -m pip wheel --no-deps",
		"cp deploy/secrets/README.md",
		"docker build --platform linux/amd64",
		"docker save \"$edge_image\"",
		"release-artifacts.py seal",
	} {
		if !strings.Contains(build, want) {
			t.Errorf("release build script missing %q", want)
		}
	}
	if strings.Contains(build, "cp -R deploy/secrets") {
		t.Fatal("release build must never copy the local secrets directory")
	}
	for _, want := range []string{
		"release-artifacts.py verify",
		"go version -m",
		"python3 -m zipfile -t",
		"deployment archive contains secret material",
		"docker image inspect",
		"org.opencontainers.image.version",
		"org.opencontainers.image.revision",
		"docker run --rm \"$edge_image\" -h",
		"docker run --rm \"$modelside_image\" --help",
	} {
		if !strings.Contains(verify, want) {
			t.Errorf("release verification script missing %q", want)
		}
	}
	for _, want := range []string{
		"yufeng.software-release/v1",
		"ARCHIVE_KINDS",
		"require_exact_entries",
		"validate_tar",
		"refusing to overwrite",
		"checksum file set mismatch",
	} {
		if !strings.Contains(contract, want) {
			t.Errorf("release artifact contract missing %q", want)
		}
	}
}

func TestRetiredReleaseLoopEntrypointsAreRemoved(t *testing.T) {
	for _, name := range []string{
		".github/workflows/pull-request.yml",
		".github/workflows/release-gate.yml",
		"scripts/preflight-release-evidence.sh",
		"scripts/archive-live-evidence.sh",
		"scripts/release-environment.py",
		"scripts/release-evidence.py",
		"scripts/release-metadata.py",
	} {
		if _, err := os.Stat(filepath.Join("..", name)); !os.IsNotExist(err) {
			t.Errorf("retired release loop entrypoint still exists: %s", name)
		}
	}
}

func readRepositoryContractFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
