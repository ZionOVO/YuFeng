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
	python := os.Getenv("PYTHON")
	if python == "" {
		python = "python3"
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller information is unavailable")
	}
	dir := filepath.Dir(source)
	cmd := exec.Command(python, "-m", "unittest", "discover", "-s", "tests", "-p", "test_*.py")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python modelside tests: %v\n%s", err, output)
	}
	if bytes.Contains(bytes.ToLower(output), []byte("redis")) {
		t.Fatalf("modelside test output must not expose a Redis dependency: %s", output)
	}
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
