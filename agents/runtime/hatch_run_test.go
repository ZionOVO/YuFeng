package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if strings.HasPrefix(filepath.Base(os.Args[0]), runtimeProcessHelperPrefix) {
		if err := runRuntimeProcessHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	code := m.Run()
	for _, path := range []string{sharedYufengRunPath(), sharedYufengRunPath() + ".building"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "remove shared yufeng-run: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func TestHatchYufengRunBinary(t *testing.T) {
	bin := buildYufengRun(t)
	res := Supervise(context.Background(), SuperviseConfig{
		Bin:    bin,
		WorkID: "work-hatch-1",
		RunID:  "run-hatch-1",
		TTL:    8 * time.Second,
		Client: &fakeWork{},
	})
	if res.Err != nil {
		t.Fatalf("yufeng-run hatch: %v", res.Err)
	}
	if res.PID == 0 {
		t.Fatal("yufeng-run must start")
	}
	for _, key := range res.EnvKeys {
		if isSecretEnv(key) {
			t.Fatalf("child env leaked %s", key)
		}
	}
	if res.TerminalKind != "done" || res.TerminalPayload != "ok" {
		t.Fatalf("terminal=%s payload=%s", res.TerminalKind, res.TerminalPayload)
	}
}

func buildYufengRun(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Join(filepath.Dir(file), "../..")
	out := sharedYufengRunPath()
	if info, err := os.Stat(out); err == nil && info.Mode().IsRegular() {
		return out
	}
	building := out + ".building"
	if err := os.Remove(building); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove incomplete yufeng-run: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", building, "./cmd/yufeng-run")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build yufeng-run: %v\n%s", err, raw)
	}
	if err := os.Rename(building, out); err != nil {
		t.Fatalf("publish shared yufeng-run: %v", err)
	}
	return out
}

func sharedYufengRunPath() string {
	name := "yufeng-run-test-" + strconv.Itoa(os.Getpid())
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(os.TempDir(), name)
}
