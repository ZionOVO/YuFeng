package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentruntime "yufeng/agents/runtime"
	runv1 "yufeng/proto/gen/runv1"
	workerv1 "yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"
)

func TestKilledAgentdReapsRunProcessTree(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "run-tree.pids")
	runBin := newAgentdProcessTestHelper(t, agentdProcessHelperConfig{Mode: "watch-supervisor", PIDFile: pidFile})

	worker := &agentdTestWorker{extended: make(chan struct{}, 1)}
	path, handler := workerv1connect.NewWorkerServiceHandler(worker)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	agentdBin := newAgentdProcessTestHelper(t, agentdProcessHelperConfig{Mode: "agentd-main", Args: []string{
		"-brain", server.URL,
		"-dev-insecure",
		"-worker", "agentd-system-test",
		"-worker-bootstrap-token", "bootstrap",
		"-public-key", "test-public-key",
		"-state-dir", filepath.Join(dir, "state"),
		"-run", runBin,
	}})

	var processOutput bytes.Buffer
	cmd := exec.Command(agentdBin)
	cmd.Stdout = &processOutput
	cmd.Stderr = &processOutput
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop := func() {
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	t.Cleanup(stop)
	childPID, grandchildPID, err := waitAgentdProcessTree(pidFile)
	if err != nil {
		stop()
		t.Fatalf("%v\n%s", err, processOutput.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (agentruntime.ProcessAlive(childPID) || agentruntime.ProcessAlive(grandchildPID)) {
		time.Sleep(20 * time.Millisecond)
	}
	if agentruntime.ProcessAlive(childPID) || agentruntime.ProcessAlive(grandchildPID) {
		_ = agentruntime.KillProcessGroup(childPID)
		t.Fatalf("run tree survived agentd death: child=%d alive=%t grandchild=%d alive=%t",
			childPID, agentruntime.ProcessAlive(childPID), grandchildPID, agentruntime.ProcessAlive(grandchildPID))
	}
}

type agentdTestWorker struct {
	workerv1connect.UnimplementedWorkerServiceHandler
	mu       sync.Mutex
	leased   bool
	extended chan struct{}
}

func (w *agentdTestWorker) RegisterWorkerIdentity(context.Context,
	*connect.Request[workerv1.RegisterWorkerIdentityRequest]) (*connect.Response[workerv1.RegisterWorkerIdentityResponse], error) {
	return connect.NewResponse(&workerv1.RegisterWorkerIdentityResponse{
		WorkerId: "agentd-system-test", WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
		AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600,
	}), nil
}

func (w *agentdTestWorker) RegisterWorker(context.Context,
	*connect.Request[workerv1.RegisterWorkerRequest]) (*connect.Response[workerv1.RegisterWorkerResponse], error) {
	return connect.NewResponse(&workerv1.RegisterWorkerResponse{
		WorkerId: "agentd-system-test", WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
	}), nil
}

func (w *agentdTestWorker) PollWork(ctx context.Context,
	_ *connect.Request[workerv1.PollWorkRequest]) (*connect.Response[workerv1.PollWorkResponse], error) {
	w.mu.Lock()
	if !w.leased {
		w.leased = true
		w.mu.Unlock()
		return connect.NewResponse(&workerv1.PollWorkResponse{Work: &workerv1.WorkItem{
			WorkId: "work-agentd-death", RunId: "run-agentd-death", LeaseId: "lease-agentd-death", LeaseEpoch: 1,
			Ttl: "1m", CapabilityToken: "capability", BudgetSnapshot: &runv1.RunBudgetSnapshot{State: "active"},
		}}), nil
	}
	w.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (w *agentdTestWorker) ExtendLease(context.Context,
	*connect.Request[workerv1.ExtendLeaseRequest]) (*connect.Response[workerv1.ExtendLeaseResponse], error) {
	select {
	case w.extended <- struct{}{}:
	default:
	}
	return connect.NewResponse(&workerv1.ExtendLeaseResponse{CapabilityToken: "capability"}), nil
}

func waitAgentdProcessTree(path string) (int, int, error) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) == 2 {
				child, childErr := strconv.Atoi(fields[0])
				grandchild, grandchildErr := strconv.Atoi(fields[1])
				if childErr == nil && grandchildErr == nil {
					return child, grandchild, nil
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, 0, fmt.Errorf("agentd did not hatch run process tree")
}
