package brain

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTelemetryAndSessionDoNotSign(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dir := filepath.Dir(file)
	for _, name := range []string{"telemetry.go", "session.go"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SignArtifact") || strings.Contains(string(raw), ".Sign(") {
			t.Fatalf("%s must not call Sign", name)
		}
	}
}
