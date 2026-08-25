package scripts

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const releaseFixtureCommit = "0123456789012345678901234567890123456789"

var releaseArchiveKinds = []string{
	"linux-amd64",
	"linux-arm64",
	"linux-mips",
	"windows-amd64",
	"darwin-amd64",
	"darwin-arm64",
	"console",
	"modelside-python",
	"deployment",
	"edge-image-linux-amd64",
	"modelside-image-linux-amd64",
}

func TestReleaseArtifactContractSealsAndVerifiesExactFiles(t *testing.T) {
	directory := releaseFixture(t, "payload/readme.txt")
	runReleaseArtifactCommand(t, true, "seal",
		"--directory", directory,
		"--version", "v0.1.0",
		"--source-commit", releaseFixtureCommit,
		"--workflow-run", "12345",
		"--generated-at", "2026-08-24T00:00:00Z",
	)

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 13 {
		t.Fatalf("release file count=%d, want 13", len(entries))
	}
	manifestRaw, err := os.ReadFile(filepath.Join(directory, "yufeng-v0.1.0-release-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Schema       string `json:"schema"`
		Version      string `json:"version"`
		SourceCommit string `json:"source-commit"`
		WorkflowRun  string `json:"workflow-run"`
		Assets       []struct {
			Name   string `json:"name"`
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "yufeng.software-release/v1" || manifest.Version != "v0.1.0" ||
		manifest.SourceCommit != releaseFixtureCommit || manifest.WorkflowRun != "12345" || len(manifest.Assets) != 11 {
		t.Fatalf("unexpected manifest metadata: %+v", manifest)
	}
	for _, asset := range manifest.Assets {
		if asset.Bytes < 1 || len(asset.SHA256) != 64 {
			t.Errorf("invalid manifest asset: %+v", asset)
		}
	}

	checksums, err := os.ReadFile(filepath.Join(directory, "yufeng-v0.1.0-checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(checksums)), "\n")+1 != 12 {
		t.Fatalf("checksum entry count is not 12:\n%s", checksums)
	}
	if !strings.Contains(string(checksums), "yufeng-v0.1.0-release-manifest.json") ||
		strings.Contains(string(checksums), "yufeng-v0.1.0-checksums.txt") {
		t.Fatal("checksums must cover the manifest without self-reference")
	}

	runReleaseArtifactCommand(t, true, "verify",
		"--directory", directory,
		"--version", "v0.1.0",
		"--source-commit", releaseFixtureCommit,
		"--workflow-run", "12345",
	)
}

func TestReleaseArtifactContractRejectsChangedOrExtraFiles(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(t *testing.T, directory string)
	}{
		{
			name: "changed archive bytes",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, "yufeng-v0.1.0-linux-amd64.tar.gz")
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("changed"); err != nil {
					closeErr := file.Close()
					t.Fatalf("write changed archive: %v; close: %v", err, closeErr)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra release file",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, "unexpected.txt"), []byte("unexpected"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := releaseFixture(t, "payload/readme.txt")
			runReleaseArtifactCommand(t, true, "seal",
				"--directory", directory,
				"--version", "v0.1.0",
				"--source-commit", releaseFixtureCommit,
				"--workflow-run", "12345",
				"--generated-at", "2026-08-24T00:00:00Z",
			)
			testCase.mutate(t, directory)
			runReleaseArtifactCommand(t, false, "verify",
				"--directory", directory,
				"--version", "v0.1.0",
				"--source-commit", releaseFixtureCommit,
				"--workflow-run", "12345",
			)
		})
	}
}

func TestReleaseArtifactContractRejectsArchivePathTraversal(t *testing.T) {
	directory := releaseFixture(t, "../../outside.txt")
	runReleaseArtifactCommand(t, false, "seal",
		"--directory", directory,
		"--version", "v0.1.0",
		"--source-commit", releaseFixtureCommit,
		"--workflow-run", "12345",
		"--generated-at", "2026-08-24T00:00:00Z",
	)
}

func releaseFixture(t *testing.T, memberName string) string {
	t.Helper()
	directory := t.TempDir()
	for _, kind := range releaseArchiveKinds {
		path := filepath.Join(directory, "yufeng-v0.1.0-"+kind+".tar.gz")
		writeReleaseArchive(t, path, memberName)
	}
	return directory
}

func writeReleaseArchive(t *testing.T, path, memberName string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	payload := []byte("release fixture")
	if err := tarWriter.WriteHeader(&tar.Header{Name: memberName, Mode: 0o644, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func runReleaseArtifactCommand(t *testing.T, wantSuccess bool, arguments ...string) {
	t.Helper()
	python := os.Getenv("PYTHON")
	if python == "" {
		for _, candidate := range []string{"python3", "python"} {
			if resolved, err := exec.LookPath(candidate); err == nil {
				python = resolved
				break
			}
		}
	}
	if python == "" {
		t.Skip("Python 3 is not installed; release artifact contracts run in continuous integration")
	}
	command := exec.Command(python, append([]string{"release-artifacts.py"}, arguments...)...)
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("release-artifacts.py failed: %v\n%s", err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("release-artifacts.py unexpectedly succeeded:\n%s", output)
	}
}
