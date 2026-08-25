package modelside

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPythonModelSideContractsAndSampling(t *testing.T) {
	python, prefix := modelSidePython()
	if python == "" {
		t.Skip("Python 3 is not installed; ModelSide contracts run in continuous integration")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller information is unavailable")
	}
	dir := filepath.Dir(source)
	args := append(append([]string(nil), prefix...), "-m", "unittest", "discover", "-s", "tests", "-p", "test_*.py")
	cmd := exec.Command(python, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python modelside tests: %v\n%s", err, output)
	}
	if bytes.Contains(bytes.ToLower(output), []byte("redis")) {
		t.Fatalf("modelside test output must not expose a Redis dependency: %s", output)
	}
}

func modelSidePython() (string, []string) {
	type candidate struct {
		executable string
		prefix     []string
	}
	candidates := []candidate{}
	if configured := strings.TrimSpace(os.Getenv("PYTHON")); configured != "" {
		candidates = append(candidates, candidate{executable: configured})
	}
	candidates = append(candidates,
		candidate{executable: "python3"},
		candidate{executable: "python"},
		candidate{executable: "py", prefix: []string{"-3"}},
	)
	for _, item := range candidates {
		resolved, err := exec.LookPath(item.executable)
		if err != nil {
			continue
		}
		args := append(append([]string(nil), item.prefix...), "--version")
		output, err := exec.Command(resolved, args...).CombinedOutput()
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), "Python 3") {
			return resolved, item.prefix
		}
	}
	return "", nil
}

func TestModelSidePackageUsesCrossPlatformTensorFlowDistribution(t *testing.T) {
	raw, err := os.ReadFile("pyproject.toml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `tensorflow = ["tensorflow>=2.16,<3"]`) {
		t.Fatal("modelside tensorflow extra must use the cross-platform TensorFlow distribution")
	}
	if strings.Contains(text, "tensorflow-cpu") {
		t.Fatal("tensorflow-cpu has no Linux ARM64 wheel and must not enter the delivery package")
	}
}
