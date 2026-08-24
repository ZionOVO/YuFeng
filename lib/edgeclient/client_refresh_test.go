package edgeclient

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	eventv1 "yufeng/proto/gen/eventv1"
	evidencev1 "yufeng/proto/gen/evidencev1"
	registryv1 "yufeng/proto/gen/registryv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

type rotatingRegistryClient struct {
	mu           sync.Mutex
	token        string
	refresh      string
	refreshCalls int
}

func (c *rotatingRegistryClient) Register(context.Context, *connect.Request[registryv1.RegisterRequest]) (*connect.Response[registryv1.RegisterResponse], error) {
	return nil, errors.New("unexpected register")
}

func (c *rotatingRegistryClient) Refresh(_ context.Context, req *connect.Request[registryv1.RefreshRequest]) (*connect.Response[registryv1.RefreshResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.Msg.GetRefreshToken() != c.refresh {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid refresh"))
	}
	c.refreshCalls++
	c.token = "new-access"
	c.refresh = "new-refresh"
	return connect.NewResponse(&registryv1.RefreshResponse{
		UnitId: "unit-a", Token: c.token, RefreshToken: c.refresh,
	}), nil
}

func (c *rotatingRegistryClient) Heartbeat(context.Context, *connect.Request[registryv1.HeartbeatRequest]) (*connect.Response[registryv1.HeartbeatResponse], error) {
	return nil, errors.New("unexpected heartbeat")
}

func (c *rotatingRegistryClient) accepts(raw string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return raw == "Bearer "+c.token
}

type rejectingTelemetryClient struct {
	registry *rotatingRegistryClient
	mu       sync.Mutex
	calls    int
}

type reviewTelemetryClient struct {
	registry       *rotatingRegistryClient
	windowCalls    int
	candidateCalls int
}

func (c *reviewTelemetryClient) UploadEvents(context.Context, *connect.Request[telemetryv1.UploadEventsRequest]) (*connect.Response[telemetryv1.UploadEventsResponse], error) {
	return nil, errors.New("unexpected upload events")
}

func (c *reviewTelemetryClient) UploadTrafficWindows(_ context.Context, req *connect.Request[telemetryv1.UploadTrafficWindowsRequest]) (*connect.Response[telemetryv1.UploadTrafficWindowsResponse], error) {
	c.windowCalls++
	if !c.registry.accepts(req.Header().Get("Authorization")) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid unit token"))
	}
	return connect.NewResponse(&telemetryv1.UploadTrafficWindowsResponse{Accepted: int32(len(req.Msg.GetWindows()))}), nil
}

func (c *reviewTelemetryClient) UploadReviewCandidates(_ context.Context, req *connect.Request[telemetryv1.UploadReviewCandidatesRequest]) (*connect.Response[telemetryv1.UploadReviewCandidatesResponse], error) {
	c.candidateCalls++
	if !c.registry.accepts(req.Header().Get("Authorization")) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid unit token"))
	}
	return connect.NewResponse(&telemetryv1.UploadReviewCandidatesResponse{Accepted: int32(len(req.Msg.GetCandidates()))}), nil
}

type reviewEvidenceClient struct {
	registry    *rotatingRegistryClient
	pollCalls   int
	submitCalls int
}

func (c *reviewEvidenceClient) PollEvidenceRequests(_ context.Context, req *connect.Request[evidencev1.PollEvidenceRequestsRequest]) (*connect.Response[evidencev1.PollEvidenceRequestsResponse], error) {
	c.pollCalls++
	if !c.registry.accepts(req.Header().Get("Authorization")) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid unit token"))
	}
	return connect.NewResponse(&evidencev1.PollEvidenceRequestsResponse{Requests: []*evidencev1.EvidenceRequest{{RequestId: req.Msg.GetUnitId()}}}), nil
}

func (c *reviewEvidenceClient) SubmitEvidenceBundle(_ context.Context, req *connect.Request[evidencev1.SubmitEvidenceBundleRequest]) (*connect.Response[evidencev1.SubmitEvidenceBundleResponse], error) {
	c.submitCalls++
	if !c.registry.accepts(req.Header().Get("Authorization")) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid unit token"))
	}
	return connect.NewResponse(&evidencev1.SubmitEvidenceBundleResponse{SensitiveContentRef: req.Msg.GetRequestId()}), nil
}

func (c *rejectingTelemetryClient) UploadEvents(_ context.Context, req *connect.Request[telemetryv1.UploadEventsRequest]) (*connect.Response[telemetryv1.UploadEventsResponse], error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if !c.registry.accepts(req.Header().Get("Authorization")) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid unit token"))
	}
	return connect.NewResponse(&telemetryv1.UploadEventsResponse{Accepted: 1}), nil
}

func (c *rejectingTelemetryClient) UploadTrafficWindows(context.Context, *connect.Request[telemetryv1.UploadTrafficWindowsRequest]) (*connect.Response[telemetryv1.UploadTrafficWindowsResponse], error) {
	return nil, errors.New("unexpected upload traffic windows")
}

func (c *rejectingTelemetryClient) UploadReviewCandidates(context.Context, *connect.Request[telemetryv1.UploadReviewCandidatesRequest]) (*connect.Response[telemetryv1.UploadReviewCandidatesResponse], error) {
	return nil, errors.New("unexpected upload review candidates")
}

func TestUploadRefreshesAccessRejectedAfterBrainRestart(t *testing.T) {
	registry := &rotatingRegistryClient{token: "new-access", refresh: "old-refresh"}
	telemetry := &rejectingTelemetryClient{registry: registry}
	client := &Client{registry: registry, telemetry: telemetry}
	session := &Session{
		UnitID: "unit-a", Token: "invalidated-access", Refresh: "old-refresh", TokenIssuedAt: time.Now(),
	}
	resp, err := client.UploadEvents(context.Background(), session, []*eventv1.Event{{Id: "event-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetAccepted() != 1 {
		t.Fatalf("response=%+v", resp)
	}
	registry.mu.Lock()
	refreshCalls := registry.refreshCalls
	registry.mu.Unlock()
	telemetry.mu.Lock()
	uploadCalls := telemetry.calls
	telemetry.mu.Unlock()
	if refreshCalls != 1 || uploadCalls != 2 {
		t.Fatalf("refresh_calls=%d upload_calls=%d", refreshCalls, uploadCalls)
	}
	snapshot := session.Snapshot()
	if snapshot.Token != "new-access" || snapshot.Refresh != "new-refresh" {
		t.Fatalf("session=%+v", snapshot)
	}
}

func TestRefreshRejectedDoesNotRotateAfterAnotherCallerRecovered(t *testing.T) {
	registry := &rotatingRegistryClient{token: "new-access", refresh: "new-refresh"}
	client := &Client{registry: registry}
	session := &Session{UnitID: "unit-a", Token: "new-access", Refresh: "new-refresh"}
	if err := client.refreshRejected(context.Background(), session, "old-access"); err != nil {
		t.Fatal(err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.refreshCalls != 0 {
		t.Fatalf("refresh_calls=%d", registry.refreshCalls)
	}
}

func TestTrafficReviewUploadsRefreshAndReuseRotatedAccess(t *testing.T) {
	registry := &rotatingRegistryClient{token: "new-access", refresh: "old-refresh"}
	telemetry := &reviewTelemetryClient{registry: registry}
	client := &Client{registry: registry, telemetry: telemetry}
	session := &Session{UnitID: "unit-a", Token: "stale-access", Refresh: "old-refresh", TokenIssuedAt: time.Now()}
	windows, err := client.UploadTrafficWindows(context.Background(), session, []*telemetryv1.TrafficWindow{{WindowId: "window-1"}})
	if err != nil || windows.GetAccepted() != 1 {
		t.Fatalf("upload traffic windows response=%v err=%v", windows, err)
	}
	candidates, err := client.UploadReviewCandidates(context.Background(), session, []*telemetryv1.ReviewCandidate{{CandidateId: "candidate-1"}})
	if err != nil || candidates.GetAccepted() != 1 {
		t.Fatalf("upload review candidates response=%v err=%v", candidates, err)
	}
	registry.mu.Lock()
	refreshCalls := registry.refreshCalls
	registry.mu.Unlock()
	if refreshCalls != 1 || telemetry.windowCalls != 2 || telemetry.candidateCalls != 1 {
		t.Fatalf("refresh=%d window_calls=%d candidate_calls=%d", refreshCalls, telemetry.windowCalls, telemetry.candidateCalls)
	}
}

func TestEvidenceRequestsRefreshAndBindSessionUnit(t *testing.T) {
	registry := &rotatingRegistryClient{token: "new-access", refresh: "old-refresh"}
	evidence := &reviewEvidenceClient{registry: registry}
	client := &Client{registry: registry, evidence: evidence}
	session := &Session{UnitID: "unit-a", Token: "stale-access", Refresh: "old-refresh", TokenIssuedAt: time.Now()}
	poll, err := client.PollEvidenceRequests(context.Background(), session, 30)
	if err != nil || len(poll.GetRequests()) != 1 || poll.GetRequests()[0].GetRequestId() != "unit-a" {
		t.Fatalf("poll evidence response=%v err=%v", poll, err)
	}
	submitted, err := client.SubmitEvidenceBundle(context.Background(), session, &evidencev1.SubmitEvidenceBundleRequest{RequestId: "request-1"})
	if err != nil || submitted.GetSensitiveContentRef() != "request-1" {
		t.Fatalf("submit evidence response=%v err=%v", submitted, err)
	}
	registry.mu.Lock()
	refreshCalls := registry.refreshCalls
	registry.mu.Unlock()
	if refreshCalls != 1 || evidence.pollCalls != 2 || evidence.submitCalls != 1 {
		t.Fatalf("refresh=%d poll_calls=%d submit_calls=%d", refreshCalls, evidence.pollCalls, evidence.submitCalls)
	}
}
