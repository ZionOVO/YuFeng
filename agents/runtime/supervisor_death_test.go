package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const runtimeProcessHelperPrefix = "yufeng-runtime-test-helper"

type runtimeProcessHelperConfig struct {
	PIDFile string `json:"pid_file"`
}

func TestKilledSupervisorReapsRunProcessTree(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	pidFile := filepath.Join(t.TempDir(), "tree.pids")
	childBin := newRuntimeProcessTestHelper(t, runtimeProcessHelperConfig{PIDFile: pidFile})
	parent := exec.Command(os.Args[0], "-test.run=^TestSupervisorParentProcess$")
	parent.Env = append(os.Environ(),
		"YUFENG_TEST_SUPERVISOR_PARENT=1",
		"YUFENG_TEST_RUN_BIN="+childBin,
		"YUFENG_TEST_PID_FILE="+pidFile,
	)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if parent.Process != nil {
			_ = parent.Process.Kill()
		}
	})
	childPID, grandchildPID := waitProcessTreePIDs(t, pidFile)
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (ProcessAlive(childPID) || ProcessAlive(grandchildPID)) {
		time.Sleep(20 * time.Millisecond)
	}
	if ProcessAlive(childPID) || ProcessAlive(grandchildPID) {
		_ = KillProcessGroup(childPID)
		t.Fatalf("process tree survived supervisor death: child=%d alive=%t grandchild=%d alive=%t",
			childPID, ProcessAlive(childPID), grandchildPID, ProcessAlive(grandchildPID))
	}
}

func TestSupervisorParentProcess(t *testing.T) {
	if os.Getenv("YUFENG_TEST_SUPERVISOR_PARENT") != "1" {
		return
	}
	result := Supervise(context.Background(), SuperviseConfig{
		Bin: os.Getenv("YUFENG_TEST_RUN_BIN"), Args: []string{"-pid-file", os.Getenv("YUFENG_TEST_PID_FILE")},
		WorkID: "work-parent-death", RunID: "run-parent-death", TTL: time.Minute,
	})
	t.Fatalf("supervisor returned before forced death: %v", result.Err)
}

func newRuntimeProcessTestHelper(t *testing.T, config runtimeProcessHelperConfig) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, runtimeProcessHelperPrefix)
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(testBinary, out); err != nil {
		raw, readErr := os.ReadFile(testBinary)
		if readErr != nil {
			t.Fatalf("read test binary after hard-link failure: %v", readErr)
		}
		if writeErr := os.WriteFile(out, raw, 0o700); writeErr != nil {
			t.Fatalf("copy test binary after hard-link failure: %v", writeErr)
		}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out+".json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}

func runRuntimeProcessHelper() error {
	raw, err := os.ReadFile(os.Args[0] + ".json")
	if err != nil {
		return fmt.Errorf("read runtime process helper config: %w", err)
	}
	var config runtimeProcessHelperConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("decode runtime process helper config: %w", err)
	}
	if err := LimitProcess(); err != nil {
		return err
	}
	if err := WatchSupervisor(4); err != nil {
		return err
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(config.PIDFile, []byte(fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)), 0o600); err != nil {
		return err
	}
	return child.Wait()
}

func waitProcessTreePIDs(t *testing.T, path string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) == 2 {
				child, childErr := strconv.Atoi(fields[0])
				grandchild, grandchildErr := strconv.Atoi(fields[1])
				if childErr == nil && grandchildErr == nil {
					return child, grandchild
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("process tree pid file was not written"))
	return 0, 0
}
