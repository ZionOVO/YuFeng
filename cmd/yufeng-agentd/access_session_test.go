package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/agents/runtime"
	"yufeng/lib/kernel"
	modelv1 "yufeng/proto/gen/modelv1"
	"yufeng/proto/gen/modelv1/modelv1connect"
	runv1 "yufeng/proto/gen/runv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	"yufeng/proto/gen/toolgatewayv1/toolgatewayv1connect"
	workerv1 "yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"
)

type accessGenerationRecorder struct {
	mu      sync.Mutex
	byRoute map[string][]string
}

func (r *accessGenerationRecorder) record(route string, header http.Header) string {
	access := strings.TrimPrefix(header.Get("Authorization"), "Bearer ")
	r.mu.Lock()
	if r.byRoute == nil {
		r.byRoute = make(map[string][]string)
	}
	r.byRoute[route] = append(r.byRoute[route], access)
	r.mu.Unlock()
	return access
}

func (r *accessGenerationRecorder) generations(route string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.byRoute[route], ",")
}

type rotatingWorkerService struct {
	workerv1connect.UnimplementedWorkerServiceHandler
	recorder     *accessGenerationRecorder
	mu           sync.Mutex
	rejectRoute  string
	refreshCalls int
	certificate  string
	chain        string
}

func (s *rotatingWorkerService) RenewWorkerCertificate(context.Context, *connect.Request[workerv1.RenewWorkerCertificateRequest]) (*connect.Response[workerv1.RenewWorkerCertificateResponse], error) {
	return connect.NewResponse(&workerv1.RenewWorkerCertificateResponse{ClientCertificate: s.certificate, CertificateChain: s.chain}), nil
}

func (s *rotatingWorkerService) PollWork(_ context.Context, req *connect.Request[workerv1.PollWorkRequest]) (*connect.Response[workerv1.PollWorkResponse], error) {
	access := s.recorder.record("poll", req.Header())
	if s.rejectRoute == "poll" && access == "access-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("access token expired"))
	}
	return connect.NewResponse(&workerv1.PollWorkResponse{}), nil
}

func (s *rotatingWorkerService) ExtendLease(_ context.Context, req *connect.Request[workerv1.ExtendLeaseRequest]) (*connect.Response[workerv1.ExtendLeaseResponse], error) {
	access := s.recorder.record("extend", req.Header())
	if s.rejectRoute == "extend" && access == "access-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("access token expired"))
	}
	return connect.NewResponse(&workerv1.ExtendLeaseResponse{CapabilityToken: "capability-new"}), nil
}

func (s *rotatingWorkerService) ReportProgress(_ context.Context, req *connect.Request[workerv1.ReportProgressRequest]) (*connect.Response[workerv1.ReportProgressResponse], error) {
	route := "progress"
	if req.Msg.GetSagaPlan() != nil || req.Msg.GetSagaReceipt() != nil {
		route = "saga"
	}
	s.recorder.record(route, req.Header())
	if s.rejectRoute == route && strings.TrimPrefix(req.Header().Get("Authorization"), "Bearer ") == "access-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("access token expired"))
	}
	return connect.NewResponse(&workerv1.ReportProgressResponse{}), nil
}

func (s *rotatingWorkerService) CompleteWork(_ context.Context, req *connect.Request[workerv1.CompleteWorkRequest]) (*connect.Response[workerv1.CompleteWorkResponse], error) {
	access := s.recorder.record("complete", req.Header())
	if s.rejectRoute == "complete" && access == "access-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("access token expired"))
	}
	return connect.NewResponse(&workerv1.CompleteWorkResponse{}), nil
}

func (s *rotatingWorkerService) FailWork(_ context.Context, req *connect.Request[workerv1.FailWorkRequest]) (*connect.Response[workerv1.FailWorkResponse], error) {
	access := s.recorder.record("fail", req.Header())
	if s.rejectRoute == "fail" && access == "access-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("access token expired"))
	}
	return connect.NewResponse(&workerv1.FailWorkResponse{}), nil
}

func (s *rotatingWorkerService) RefreshWorkerAccessToken(_ context.Context, req *connect.Request[workerv1.RefreshWorkerAccessTokenRequest]) (*connect.Response[workerv1.RefreshWorkerAccessTokenResponse], error) {
	s.mu.Lock()
	s.refreshCalls++
	s.mu.Unlock()
	if req.Msg.GetRefreshToken() != "refresh-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("refresh token rejected"))
	}
	return connect.NewResponse(&workerv1.RefreshWorkerAccessTokenResponse{AccessToken: "access-new", RefreshToken: "refresh-new"}), nil
}

type rotatingToolService struct {
	toolgatewayv1connect.UnimplementedToolGatewayServiceHandler
	recorder *accessGenerationRecorder
	code     connect.Code
}

func (s *rotatingToolService) InvokeTool(_ context.Context, req *connect.Request[toolgatewayv1.InvokeToolRequest]) (*connect.Response[toolgatewayv1.InvokeToolResponse], error) {
	access := s.recorder.record("tool", req.Header())
	if s.code != 0 {
		return nil, connect.NewError(s.code, errors.New("tool rejected"))
	}
	if access == "access-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("access token expired"))
	}
	return connect.NewResponse(&toolgatewayv1.InvokeToolResponse{ResultJson: `{}`}), nil
}

type rotatingModelService struct {
	modelv1connect.UnimplementedModelGatewayServiceHandler
	recorder  *accessGenerationRecorder
	rejectOld bool
}

func (s *rotatingModelService) Generate(_ context.Context, req *connect.Request[modelv1.GenerateRequest]) (*connect.Response[modelv1.GenerateResponse], error) {
	access := s.recorder.record("model", req.Header())
	if s.rejectOld && access == "access-old" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("access token expired"))
	}
	return connect.NewResponse(&modelv1.GenerateResponse{}), nil
}

func newAccessTestClients(t *testing.T, worker *rotatingWorkerService, tool *rotatingToolService, model *rotatingModelService) (workerv1connect.WorkerServiceClient, toolgatewayv1connect.ToolGatewayServiceClient, modelv1connect.ModelGatewayServiceClient) {
	t.Helper()
	mux := http.NewServeMux()
	workerPath, workerHandler := workerv1connect.NewWorkerServiceHandler(worker)
	toolPath, toolHandler := toolgatewayv1connect.NewToolGatewayServiceHandler(tool)
	modelPath, modelHandler := modelv1connect.NewModelGatewayServiceHandler(model)
	mux.Handle(workerPath, workerHandler)
	mux.Handle(toolPath, toolHandler)
	mux.Handle(modelPath, modelHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return workerv1connect.NewWorkerServiceClient(server.Client(), server.URL),
		toolgatewayv1connect.NewToolGatewayServiceClient(server.Client(), server.URL),
		modelv1connect.NewModelGatewayServiceClient(server.Client(), server.URL)
}

func TestActiveWorkUsesRotatedAccessForBrokerLeaseProgressSagaAndTerminalCalls(t *testing.T) {
	recorder := &accessGenerationRecorder{}
	workerService := &rotatingWorkerService{recorder: recorder}
	toolService := &rotatingToolService{recorder: recorder}
	modelService := &rotatingModelService{recorder: recorder}
	workerClient, toolClient, modelClient := newAccessTestClients(t, workerService, toolService, modelService)
	var renews int
	session := &runtime.AccessSession{Access: "access-old", Refresh: "refresh-old", Renew: func(context.Context, string) (string, string, error) {
		renews++
		return "access-new", "refresh-new", nil
	}}
	work := connectWork{client: workerClient, workerID: "worker", sess: session}
	tools := connectTools{client: toolClient, models: modelClient, sess: session}
	ctx := context.Background()
	if _, err := work.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := work.Progress(ctx, "work", "lease", 1, "started", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Invoke(ctx, "stale-snapshot", "capability", "event.get", `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Generate(ctx, "stale-snapshot", "capability", &modelv1.GenerateRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := work.Extend(ctx, "work", "lease", 1); err != nil {
		t.Fatal(err)
	}
	if err := work.Progress(ctx, "work", "lease", 1, "after-rotation", ""); err != nil {
		t.Fatal(err)
	}
	plan := runtime.SagaPlan{PlanDigest: "sha256:plan"}
	if _, err := work.Saga(ctx, "work", "lease", 1, runtime.SagaProgress{Plan: &plan}); err != nil {
		t.Fatal(err)
	}
	if err := work.Complete(ctx, "work", "lease", 1, "result", "receipt"); err != nil {
		t.Fatal(err)
	}
	if err := work.Fail(ctx, "work", "lease", 1, "failed", "message"); err != nil {
		t.Fatal(err)
	}
	if renews != 1 {
		t.Fatalf("renew calls=%d", renews)
	}
	want := map[string]string{
		"poll": "access-old", "tool": "access-old,access-new", "model": "access-new",
		"extend": "access-new", "saga": "access-new", "complete": "access-new", "fail": "access-new",
	}
	for route, generations := range want {
		if got := recorder.generations(route); got != generations {
			t.Errorf("%s generations=%q want=%q", route, got, generations)
		}
	}
	if got := recorder.generations("progress"); got != "access-old,access-new" {
		t.Fatalf("progress generations=%q", got)
	}
}

func TestConnectToolsDoesNotRetryNonAuthenticationError(t *testing.T) {
	recorder := &accessGenerationRecorder{}
	workerService := &rotatingWorkerService{recorder: recorder}
	toolService := &rotatingToolService{recorder: recorder, code: connect.CodeFailedPrecondition}
	modelService := &rotatingModelService{recorder: recorder}
	_, toolClient, modelClient := newAccessTestClients(t, workerService, toolService, modelService)
	var renews int
	session := &runtime.AccessSession{Access: "access-old", Refresh: "refresh-old", Renew: func(context.Context, string) (string, string, error) {
		renews++
		return "access-new", "refresh-new", nil
	}}
	_, err := (connectTools{client: toolClient, models: modelClient, sess: session}).Invoke(context.Background(), "", "capability", "event.get", `{}`)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || renews != 0 || recorder.generations("tool") != "access-old" {
		t.Fatalf("err=%v renews=%d calls=%q", err, renews, recorder.generations("tool"))
	}
}

func TestEveryActiveWorkCallRetriesOneUnauthenticatedGeneration(t *testing.T) {
	tests := []struct {
		route string
		call  func(context.Context, connectWork, connectTools) error
	}{
		{route: "poll", call: func(ctx context.Context, work connectWork, _ connectTools) error {
			_, err := work.Poll(ctx)
			return err
		}},
		{route: "extend", call: func(ctx context.Context, work connectWork, _ connectTools) error {
			_, err := work.Extend(ctx, "work", "lease", 1)
			return err
		}},
		{route: "progress", call: func(ctx context.Context, work connectWork, _ connectTools) error {
			return work.Progress(ctx, "work", "lease", 1, "stage", "")
		}},
		{route: "saga", call: func(ctx context.Context, work connectWork, _ connectTools) error {
			plan := runtime.SagaPlan{PlanDigest: "sha256:plan"}
			_, err := work.Saga(ctx, "work", "lease", 1, runtime.SagaProgress{Plan: &plan})
			return err
		}},
		{route: "complete", call: func(ctx context.Context, work connectWork, _ connectTools) error {
			return work.Complete(ctx, "work", "lease", 1, "result", "receipt")
		}},
		{route: "fail", call: func(ctx context.Context, work connectWork, _ connectTools) error {
			return work.Fail(ctx, "work", "lease", 1, "code", "message")
		}},
		{route: "tool", call: func(ctx context.Context, _ connectWork, tools connectTools) error {
			_, err := tools.Invoke(ctx, "stale", "capability", "event.get", `{}`)
			return err
		}},
		{route: "model", call: func(ctx context.Context, _ connectWork, tools connectTools) error {
			_, err := tools.Generate(ctx, "stale", "capability", &modelv1.GenerateRequest{})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.route, func(t *testing.T) {
			recorder := &accessGenerationRecorder{}
			workerService := &rotatingWorkerService{recorder: recorder, rejectRoute: test.route}
			toolService := &rotatingToolService{recorder: recorder}
			modelService := &rotatingModelService{recorder: recorder, rejectOld: test.route == "model"}
			workerClient, toolClient, modelClient := newAccessTestClients(t, workerService, toolService, modelService)
			var renews int
			session := &runtime.AccessSession{Access: "access-old", Refresh: "refresh-old", Renew: func(context.Context, string) (string, string, error) {
				renews++
				return "access-new", "refresh-new", nil
			}}
			work := connectWork{client: workerClient, workerID: "worker", sess: session}
			tools := connectTools{client: toolClient, models: modelClient, sess: session}
			if err := test.call(context.Background(), work, tools); err != nil {
				t.Fatal(err)
			}
			if renews != 1 || recorder.generations(test.route) != "access-old,access-new" {
				t.Fatalf("renew calls=%d %s generations=%q", renews, test.route, recorder.generations(test.route))
			}
		})
	}
}

func TestRefreshPersistenceFailureCancelsActiveRunAndCompensates(t *testing.T) {
	recorder := &accessGenerationRecorder{}
	workerService := &rotatingWorkerService{recorder: recorder, rejectRoute: "extend"}
	workerClient, _, _ := newAccessTestClients(t, workerService, &rotatingToolService{recorder: recorder}, &rotatingModelService{recorder: recorder})
	persistErr := errors.New("injected refresh persistence failure")
	session := &runtime.AccessSession{Access: "access-old", Refresh: "refresh-old"}
	session.Renew = workerAccessRenewer(workerClient, "worker", filepath.Join(t.TempDir(), "worker-refresh"), func(string, string, string) error {
		return persistErr
	})

	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	compensatedPath := filepath.Join(dir, "compensated")
	runBin := newAgentdProcessTestHelper(t, agentdProcessHelperConfig{
		Mode: "cancel-and-compensate", StartedPath: startedPath, CompensatedPath: compensatedPath,
	})
	client := &activePersistenceFailureWork{
		connectWork: connectWork{client: workerClient, workerID: "worker", sess: session},
		startedPath: startedPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := runtime.RunWorker(ctx, client, nil, session, runBin)
	if !errors.Is(err, runtime.ErrAccessSessionFailed) || !errors.Is(err, persistErr) {
		t.Fatalf("run worker err=%v", err)
	}
	if _, err := os.Stat(startedPath); err != nil {
		t.Fatalf("active child did not start: %v", err)
	}
	if _, err := os.Stat(compensatedPath); err != nil {
		t.Fatalf("active child did not compensate: %v", err)
	}
	workerService.mu.Lock()
	refreshCalls := workerService.refreshCalls
	workerService.mu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("refresh calls=%d", refreshCalls)
	}
	access, refresh := session.Tokens()
	if access != "access-old" || refresh != "refresh-old" {
		t.Fatalf("failed persistence changed tokens: %q %q", access, refresh)
	}
	before := recorder.generations("poll")
	if _, err := client.connectWork.Poll(context.Background()); !errors.Is(err, runtime.ErrAccessSessionFailed) {
		t.Fatalf("closed session poll err=%v", err)
	}
	if after := recorder.generations("poll"); after != before {
		t.Fatalf("closed session called server again: before=%q after=%q", before, after)
	}
}

func TestCertificateRefreshPersistenceFailureKeepsOldSessionAndFailsClosed(t *testing.T) {
	state := t.TempDir()
	material, err := loadOrCreateEnrollmentMaterial(state, "worker")
	if err != nil {
		t.Fatal(err)
	}
	certificateAuthorityDir := t.TempDir()
	issuer, err := kernel.LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(certificateAuthorityDir, "ca.key"), filepath.Join(certificateAuthorityDir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := issuer.Issue("worker", material.CertificateRequest, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certificatePath := workerClientCertificatePath(state)
	if err := os.WriteFile(certificatePath, []byte(issued.Certificate+issued.Chain), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &accessGenerationRecorder{}
	workerService := &rotatingWorkerService{recorder: recorder, certificate: issued.Certificate, chain: issued.Chain}
	workerClient, _, _ := newAccessTestClients(t, workerService, &rotatingToolService{recorder: recorder}, &rotatingModelService{recorder: recorder})
	session := &runtime.AccessSession{Access: "access-old", Refresh: "refresh-old"}
	persistErr := errors.New("injected certificate refresh persistence failure")
	renew := workerCertificateRenewerWithSaver(http.DefaultClient, workerClient, "worker", certificatePath,
		workerClientKeyPath(state), workerRefreshFile(state), session, func(string, string, string) error { return persistErr })
	err = renew(context.Background())
	if !errors.Is(err, runtime.ErrAccessSessionFailed) || !errors.Is(err, persistErr) {
		t.Fatalf("renew err=%v", err)
	}
	if failure := session.FailureErr(); !errors.Is(failure, persistErr) {
		t.Fatalf("failure=%v", failure)
	}
	access, refresh := session.Tokens()
	if access != "access-old" || refresh != "refresh-old" {
		t.Fatalf("failed persistence changed tokens: %q %q", access, refresh)
	}
}

type activePersistenceFailureWork struct {
	connectWork
	mu          sync.Mutex
	leased      bool
	startedPath string
}

func (c *activePersistenceFailureWork) Poll(ctx context.Context) (*workerv1.WorkItem, error) {
	c.mu.Lock()
	if !c.leased {
		c.leased = true
		c.mu.Unlock()
		return &workerv1.WorkItem{
			WorkId: "work", RunId: "run", LeaseId: "lease", LeaseEpoch: 1, Ttl: "2s",
			CapabilityToken: "capability", BudgetSnapshot: &runv1.RunBudgetSnapshot{State: "active"},
		}, nil
	}
	c.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *activePersistenceFailureWork) Extend(ctx context.Context, workID, leaseID string, leaseEpoch int64) (runtime.LeaseExtension, error) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(c.startedPath); err == nil {
			return c.connectWork.Extend(ctx, workID, leaseID, leaseEpoch)
		} else if !errors.Is(err, os.ErrNotExist) {
			return runtime.LeaseExtension{}, err
		}
		select {
		case <-ctx.Done():
			return runtime.LeaseExtension{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
