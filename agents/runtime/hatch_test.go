package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"
)

func TestHatchTTLKillsChild(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	child := newRuntimeProcessTestHelper(t, runtimeProcessHelperConfig{Mode: "block", ReadyPath: ready})
	res := Hatch(context.Background(), HatchConfig{
		Bin: child, TTL: 2 * time.Second,
	})
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("ttl must fail the run with deadline exceeded: %v", res.Err)
	}
	if res.PID == 0 {
		t.Fatal("ttl test child was not started")
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("ttl test child did not become ready: %v", err)
	}
	if ProcessAlive(res.PID) {
		t.Fatalf("child pid %d still alive after ttl", res.PID)
	}
}

func TestHatchSupervisorCancelKillsChild(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	child := newRuntimeProcessTestHelper(t, runtimeProcessHelperConfig{Mode: "block", ReadyPath: ready})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan HatchResult, 1)
	go func() {
		done <- Hatch(ctx, HatchConfig{Bin: child, TTL: time.Minute})
	}()
	waitRuntimeProcessHelperReady(t, ready)
	cancel()
	var res HatchResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("hatch did not return after supervisor cancellation")
	}
	if ProcessAlive(res.PID) {
		_ = KillProcessGroup(res.PID)
		t.Fatalf("child pid %d remained after supervisor cancel", res.PID)
	}
}

func TestHatchRejectsCapabilityEnv(t *testing.T) {
	res := Hatch(context.Background(), HatchConfig{
		Bin: "must-not-start",
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
	res := Hatch(context.Background(), HatchConfig{Bin: "must-not-start", Budget: b})
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
	child := newRuntimeProcessTestHelper(t, runtimeProcessHelperConfig{Mode: "check-limits"})
	res := Hatch(context.Background(), HatchConfig{
		Bin:    child,
		Limits: ResourceLimit{Files: 128, MemoryBytes: 64 << 20, CPUSeconds: 7},
		TTL:    10 * time.Second,
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
	result := Hatch(context.Background(), HatchConfig{
		Bin: os.Args[0], Args: []string{"-test.run=^TestLimitResourcesUsesGoMemoryLimit$"},
		Env: append(os.Environ(), childEnv+"=1"), TTL: 10 * time.Second,
	})
	if result.Err != nil {
		t.Fatalf("resource-limited Go child failed: %v", result.Err)
	}
}
