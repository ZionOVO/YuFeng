//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	agentruntime "yufeng/agents/runtime"
)

func TestYufengRunSIGTERMCompletesContextCancellationCompensation(t *testing.T) {
	if os.Getenv("YUFENG_RUN_SIGTERM_HELPER") == "1" {
		runSIGTERMCompensationHelper()
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	readyPath := filepath.Join(stateDir, "ready")
	compensatedPath := filepath.Join(stateDir, "compensated")
	command := exec.Command(executable, "-test.run=^TestYufengRunSIGTERMCompletesContextCancellationCompensation$")
	command.Env = append(os.Environ(),
		"YUFENG_RUN_SIGTERM_HELPER=1",
		"YUFENG_RUN_SIGTERM_READY="+readyPath,
		"YUFENG_RUN_SIGTERM_COMPENSATED="+compensatedPath,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatal("SIGTERM helper did not reach the cancellable step")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("SIGTERM terminated the run before compensation: %v", err)
	}
	if raw, err := os.ReadFile(compensatedPath); err != nil || string(raw) != "context canceled" {
		t.Fatalf("compensation receipt=%q err=%v", raw, err)
	}
}

func runSIGTERMCompensationHelper() {
	ctx, stop := runSignalContext(context.Background())
	defer stop()
	readyPath := os.Getenv("YUFENG_RUN_SIGTERM_READY")
	compensatedPath := os.Getenv("YUFENG_RUN_SIGTERM_COMPENSATED")
	var record agentruntime.RunRecord
	err := agentruntime.Execute(ctx, []agentruntime.Step{
		{
			Name: "prepare",
			Compensate: func(context.Context) error {
				return os.WriteFile(compensatedPath, []byte(ctx.Err().Error()), 0o600)
			},
		},
		{
			Name: "wait_for_cancellation",
			Run: func(context.Context) error {
				if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}, false, &record)
	if !errors.Is(err, context.Canceled) {
		os.Exit(2)
	}
}
