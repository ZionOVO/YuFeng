package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	agentruntime "yufeng/agents/runtime"
)

const agentdProcessHelperPrefix = "yufeng-agentd-test-helper"

type agentdProcessHelperConfig struct {
	Mode            string   `json:"mode"`
	Args            []string `json:"args,omitempty"`
	PIDFile         string   `json:"pid_file,omitempty"`
	StartedPath     string   `json:"started_path,omitempty"`
	CompensatedPath string   `json:"compensated_path,omitempty"`
}

func TestMain(m *testing.M) {
	if strings.HasPrefix(filepath.Base(os.Args[0]), agentdProcessHelperPrefix) {
		if err := runAgentdProcessHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func newAgentdProcessTestHelper(t *testing.T, config agentdProcessHelperConfig) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, agentdProcessHelperPrefix)
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
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

func runAgentdProcessHelper() error {
	raw, err := os.ReadFile(os.Args[0] + ".json")
	if err != nil {
		return fmt.Errorf("read agentd process helper config: %w", err)
	}
	var config agentdProcessHelperConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("decode agentd process helper config: %w", err)
	}
	switch config.Mode {
	case "agentd-main":
		flag.CommandLine = flag.NewFlagSet("yufeng-agentd", flag.ExitOnError)
		os.Args = append([]string{os.Args[0]}, config.Args...)
		main()
		return nil
	case "watch-supervisor":
		return runSupervisorWatchHelper(config)
	case "cancel-and-compensate":
		return runCancelableCompensationHelper(config)
	default:
		return errors.New("unknown agentd process helper mode")
	}
}

func runSupervisorWatchHelper(config agentdProcessHelperConfig) error {
	if err := agentruntime.LimitProcess(); err != nil {
		return err
	}
	if err := agentruntime.WatchSupervisor(4); err != nil {
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

func runCancelableCompensationHelper(config agentdProcessHelperConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var record agentruntime.RunRecord
	err := agentruntime.Execute(ctx, []agentruntime.Step{
		{
			Name: "prepare",
			Run: func(context.Context) error {
				return os.WriteFile(config.StartedPath, []byte("started"), 0o600)
			},
			Compensate: func(context.Context) error {
				return os.WriteFile(config.CompensatedPath, []byte("compensated"), 0o600)
			},
		},
		{Name: "wait", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
	}, false, &record)
	if err == nil {
		return errors.New("cancelable helper completed without cancellation")
	}
	return nil
}
