package runtime

import (
	"context"
	"os"
	"os/exec"
	"runtime/debug"
	"testing"
	"time"
)

func TestHatchTTLKillsChild(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	res := Hatch(context.Background(), HatchConfig{
		Bin: "sleep", Args: []string{"30"}, TTL: 200 * time.Millisecond,
	})
	if res.Err == nil {
		t.Fatal("ttl must fail the run")
	}
	time.Sleep(50 * time.Millisecond)
	if ProcessAlive(res.PID) {
		t.Fatalf("child pid %d still alive after ttl", res.PID)
	}
}

func TestHatchSupervisorCancelKillsChild(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan HatchResult, 1)
	go func() {
		done <- Hatch(ctx, HatchConfig{Bin: "sleep", Args: []string{"30"}, TTL: time.Minute})
	}()
	time.Sleep(80 * time.Millisecond)
	cancel()
	res := <-done
	time.Sleep(50 * time.Millisecond)
	if ProcessAlive(res.PID) {
		_ = KillProcessGroup(res.PID)
		t.Fatalf("child pid %d remained after supervisor cancel", res.PID)
	}
}

func TestHatchRejectsCapabilityEnv(t *testing.T) {
	res := Hatch(context.Background(), HatchConfig{
		Bin: "sleep", Args: []string{"1"},
		Env: []string{"YUFENG_CAPABILITY=stolen"},
	})
	if res.Err == nil {
		t.Fatal("capability env must fail closed")
	}
	if res.PID != 0 {
		t.Fatal("must not spawn when capability is in env")
	}
}

func TestHatchBudgetExhausted(t *testing.T) {
	b := &CallBudget{Remaining: 0}
	res := Hatch(context.Background(), HatchConfig{Bin: "sleep", Args: []string{"1"}, Budget: b})
	if res.Err == nil || res.Err.Error() != "resource_exhausted" {
		t.Fatalf("got %v", res.Err)
	}
	if res.PID != 0 {
		t.Fatal("must not spawn when budget exhausted")
	}
}

func TestExecuteFailsWhenMemoryExceedsLimit(t *testing.T) {
	var rec RunRecord
	err := Execute(context.Background(), []Step{{Name: "big", MemBytes: 200}}, false, &rec, ResourceLimit{MemoryBytes: 100})
	if err == nil || err.Error() != "resource_exhausted" {
		t.Fatalf("got %v", err)
	}
}

func TestHatchPassesResourceLimits(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	res := Hatch(context.Background(), HatchConfig{
		Bin:    "sh",
		Args:   []string{"-c", `test "$YUFENG_RLIMIT_NOFILE" = "128" && test "$YUFENG_MEMORY_LIMIT" = "67108864" && test "$YUFENG_RLIMIT_CPU" = "7"`},
		Env:    []string{"PATH=/bin:/usr/bin"},
		Limits: ResourceLimit{Files: 128, MemoryBytes: 64 << 20, CPUSeconds: 7},
		TTL:    time.Second,
	})
	if res.Err != nil {
		t.Fatalf("child must see injected resource limits: %v", res.Err)
	}
}

func TestLimitResourcesUsesGoMemoryLimit(t *testing.T) {
	const childEnv = "YUFENG_TEST_RESOURCE_LIMIT_CHILD"
	const wantMemory = int64(256 << 20)
	if os.Getenv(childEnv) == "1" {
		if err := LimitResources(ResourceLimit{Files: 128, MemoryBytes: uint64(wantMemory), CPUSeconds: 7}); err != nil {
			t.Fatal(err)
		}
		if got := debug.SetMemoryLimit(-1); got != wantMemory {
			t.Fatalf("Go memory limit=%d want=%d", got, wantMemory)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLimitResourcesUsesGoMemoryLimit$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resource-limited Go child failed: %v\n%s", err, output)
	}
}
