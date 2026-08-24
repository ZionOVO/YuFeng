package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgeclient"
	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	evidencev1 "yufeng/proto/gen/evidencev1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	"yufeng/proto/gen/telemetryv1/telemetryv1connect"
)

type trafficReviewUploadServer struct {
	telemetryv1connect.UnimplementedTelemetryServiceHandler

	mu                 sync.Mutex
	windowFailuresLeft int
	calls              []string
	callNotifications  chan string
}

func (s *trafficReviewUploadServer) UploadTrafficWindows(_ context.Context, request *connect.Request[telemetryv1.UploadTrafficWindowsRequest]) (*connect.Response[telemetryv1.UploadTrafficWindowsResponse], error) {
	s.mu.Lock()
	s.calls = append(s.calls, "windows")
	fail := s.windowFailuresLeft > 0
	if fail {
		s.windowFailuresLeft--
	}
	s.mu.Unlock()
	s.callNotifications <- "windows"
	if fail {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("temporary traffic store outage"))
	}
	return connect.NewResponse(&telemetryv1.UploadTrafficWindowsResponse{Accepted: int32(len(request.Msg.GetWindows()))}), nil
}

func (s *trafficReviewUploadServer) UploadReviewCandidates(_ context.Context, request *connect.Request[telemetryv1.UploadReviewCandidatesRequest]) (*connect.Response[telemetryv1.UploadReviewCandidatesResponse], error) {
	s.mu.Lock()
	s.calls = append(s.calls, "candidates")
	s.mu.Unlock()
	s.callNotifications <- "candidates"
	return connect.NewResponse(&telemetryv1.UploadReviewCandidatesResponse{Accepted: int32(len(request.Msg.GetCandidates()))}), nil
}

func TestBuildEvidenceBundleCapsModelInputBelowCaseStorageLimit(t *testing.T) {
	vault, err := edgecore.NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	handles := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		raw, err := json.Marshal(evidenceDocument{Method: "GET", RouteTemplate: "/large", Fields: []evidenceField{{
			Selector: "query.payload", Surface: "query", Length: 7900, Charset: "alpha", Digest: "sha256:test",
			Value: string(bytes.Repeat([]byte{byte('a' + index)}, 7900)),
		}}})
		if err != nil {
			t.Fatal(err)
		}
		handle, _, _, err := vault.Put(raw, now)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
	}
	request := &evidencev1.EvidenceRequest{RequestId: "request-1", ApprovalId: "approval-1", CaseId: "case-1",
		EvidenceHandles: handles, AllowedFields: []string{"query"}, MaxBytes: kernel.TrafficReviewCaseEvidenceBytes,
		ExpiresAt: timestamppb.New(now.Add(15 * time.Minute)), LeaseId: "lease-1", LeaseEpoch: 1,
		LeaseDeadline: timestamppb.New(now.Add(5 * time.Minute))}
	bundle, err := buildEvidenceBundle(vault, request, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("build evidence bundle: %v", err)
	}
	var total int
	for _, fragment := range bundle.GetFragments() {
		total += len(fragment.GetContent())
	}
	if total > kernel.TrafficReviewModelEvidenceBytes {
		t.Fatalf("model evidence bytes=%d", total)
	}
	covered := map[string]bool{}
	for _, fragment := range bundle.GetFragments() {
		covered[fragment.GetEvidenceHandle()] = true
	}
	if len(covered) != len(handles) {
		t.Fatalf("bounded model bundle omitted approved handles: covered=%d handles=%d", len(covered), len(handles))
	}
}

func TestBuildEvidenceBundleFailsWhenApprovedHandleIsMissing(t *testing.T) {
	vault, err := edgecore.NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	request := &evidencev1.EvidenceRequest{RequestId: "request-missing", ApprovalId: "approval-missing", CaseId: "case-missing",
		EvidenceHandles: []string{"missing-handle"}, AllowedFields: []string{"body"}, MaxBytes: 1024,
		ExpiresAt: timestamppb.New(now.Add(15 * time.Minute)), LeaseId: "lease-missing", LeaseEpoch: 1,
		LeaseDeadline: timestamppb.New(now.Add(5 * time.Minute))}
	if _, err := buildEvidenceBundle(vault, request, now.Add(time.Minute)); err == nil {
		t.Fatal("missing approved evidence must fail closed")
	}
}

func TestBuildEvidenceBundleRejectsRequestsOutsideClosedEvidenceSchema(t *testing.T) {
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		raw     []byte
		fields  []string
		handles func(string) []string
	}{
		{
			name:   "unknown approved field",
			raw:    []byte(`{"method":"GET","route_template":"/items"}`),
			fields: []string{"method", "headers"},
		},
		{
			name:   "duplicate approved field",
			raw:    []byte(`{"method":"GET","route_template":"/items"}`),
			fields: []string{"method", "method"},
		},
		{
			name:   "unknown document field",
			raw:    []byte(`{"method":"GET","route_template":"/items","headers":{"authorization":"secret"}}`),
			fields: []string{"method"},
		},
		{
			name: "unknown projected surface",
			raw: []byte(`{"method":"GET","route_template":"/items","fields":[` +
				`{"selector":"header.authorization","surface":"header","length":6,"charset":"alpha","digest":"sha256:test"}]}`),
			fields: []string{"method"},
		},
		{
			name: "duplicate projected selector",
			raw: []byte(`{"method":"GET","route_template":"/items","fields":[` +
				`{"selector":"query.id","surface":"query","length":1,"charset":"digit","digest":"sha256:test"},` +
				`{"selector":"QUERY.ID","surface":"query","length":1,"charset":"digit","digest":"sha256:test"}]}`),
			fields: []string{"method"},
		},
		{
			name:    "duplicate approved handle",
			raw:     []byte(`{"method":"GET","route_template":"/items"}`),
			fields:  []string{"method"},
			handles: func(handle string) []string { return []string{handle, handle} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vault, err := edgecore.NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{23}, 32))
			if err != nil {
				t.Fatal(err)
			}
			handle, _, _, err := vault.Put(test.raw, now)
			if err != nil {
				t.Fatal(err)
			}
			handles := []string{handle}
			if test.handles != nil {
				handles = test.handles(handle)
			}
			request := &evidencev1.EvidenceRequest{
				RequestId: "request-closed", ApprovalId: "approval-closed", CaseId: "case-closed",
				EvidenceHandles: handles, AllowedFields: test.fields, MaxBytes: 1024,
				ExpiresAt: timestamppb.New(now.Add(15 * time.Minute)), LeaseId: "lease-closed", LeaseEpoch: 1,
				LeaseDeadline: timestamppb.New(now.Add(5 * time.Minute)),
			}
			if _, err := buildEvidenceBundle(vault, request, now.Add(time.Minute)); err == nil {
				t.Fatal("evidence outside the closed request and document schema must fail closed")
			}
		})
	}
}

func TestBuildEvidenceBundleKeepsStructuredFragmentsValidWhenFairlyTruncated(t *testing.T) {
	vault, err := edgecore.NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	handles := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		raw, err := json.Marshal(evidenceDocument{Method: "GET", RouteTemplate: "/large", Fields: []evidenceField{{
			Selector: "query.payload", Surface: "query", Length: 7900, Charset: "alpha", Digest: "sha256:test",
			Value: string(bytes.Repeat([]byte{byte('a' + index)}, 7900)),
		}}})
		if err != nil {
			t.Fatal(err)
		}
		handle, _, _, err := vault.Put(raw, now)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
	}
	request := &evidencev1.EvidenceRequest{
		RequestId: "request-structured", ApprovalId: "approval-structured", CaseId: "case-structured",
		EvidenceHandles: handles, AllowedFields: []string{"query"}, MaxBytes: kernel.TrafficReviewCaseEvidenceBytes,
		ExpiresAt: timestamppb.New(now.Add(15 * time.Minute)), LeaseId: "lease-structured", LeaseEpoch: 1,
		LeaseDeadline: timestamppb.New(now.Add(5 * time.Minute)),
	}
	bundle, err := buildEvidenceBundle(vault, request, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("build evidence bundle: %v", err)
	}
	covered := make(map[string]bool, len(handles))
	for _, fragment := range bundle.GetFragments() {
		covered[fragment.GetEvidenceHandle()] = true
		if fragment.GetField() == "query" || fragment.GetField() == "body" {
			if !json.Valid(fragment.GetContent()) {
				t.Fatalf("truncated %s fragment for %s is not valid JSON", fragment.GetField(), fragment.GetEvidenceHandle())
			}
		}
	}
	if len(covered) != len(handles) {
		t.Fatalf("fair truncation covered=%d handles=%d", len(covered), len(handles))
	}
}

func TestBuildEvidenceBundleRejectsExpiredRequestAndLease(t *testing.T) {
	vault, err := edgecore.NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{31}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	handle, _, _, err := vault.Put([]byte(`{"method":"GET","route_template":"/items"}`), now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		expiresAt     *timestamppb.Timestamp
		leaseDeadline *timestamppb.Timestamp
	}{
		{name: "expired request", expiresAt: timestamppb.New(now), leaseDeadline: timestamppb.New(now.Add(time.Minute))},
		{name: "expired lease", expiresAt: timestamppb.New(now.Add(15 * time.Minute)), leaseDeadline: timestamppb.New(now)},
		{name: "missing lease deadline", expiresAt: timestamppb.New(now.Add(15 * time.Minute))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &evidencev1.EvidenceRequest{
				RequestId: "request-expired", ApprovalId: "approval-expired", CaseId: "case-expired",
				EvidenceHandles: []string{handle}, AllowedFields: []string{"method"}, MaxBytes: 1024,
				ExpiresAt: test.expiresAt, LeaseId: "lease-expired", LeaseEpoch: 1, LeaseDeadline: test.leaseDeadline,
			}
			if _, err := buildEvidenceBundle(vault, request, now); err == nil {
				t.Fatal("expired request or delivery lease must fail closed")
			}
		})
	}
}

func TestSplitRejectedWindowsKeepsOnlyRetryableItems(t *testing.T) {
	values := []*telemetryv1.TrafficWindow{{WindowId: "accepted"}, {WindowId: "retry"}, {WindowId: "permanent"}}
	retryable, permanent, err := splitRejectedWindows(values, []*telemetryv1.RejectedEvent{
		{EventId: "retry", Retryable: true}, {EventId: "permanent", Retryable: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(retryable) != 1 || retryable[0].GetWindowId() != "retry" || len(permanent) != 1 || permanent[0].GetWindowId() != "permanent" {
		t.Fatalf("retryable=%v permanent=%v", retryable, permanent)
	}
}

func TestSplitRejectedCandidatesRejectsUnknownAcknowledgement(t *testing.T) {
	_, _, err := splitRejectedCandidates([]*telemetryv1.ReviewCandidate{{CandidateId: "candidate-1"}},
		[]*telemetryv1.RejectedEvent{{EventId: "unknown", Retryable: true}})
	if err == nil {
		t.Fatal("unknown item acknowledgement must preserve the source frame")
	}
}

func TestSplitRejectedWindowsRejectsAmbiguousIdentities(t *testing.T) {
	tests := []struct {
		name     string
		values   []*telemetryv1.TrafficWindow
		rejected []*telemetryv1.RejectedEvent
	}{
		{name: "unknown acknowledgement", values: []*telemetryv1.TrafficWindow{{WindowId: "window-1"}},
			rejected: []*telemetryv1.RejectedEvent{{EventId: "unknown"}}},
		{name: "duplicate acknowledgement", values: []*telemetryv1.TrafficWindow{{WindowId: "window-1"}},
			rejected: []*telemetryv1.RejectedEvent{{EventId: "window-1"}, {EventId: "window-1"}}},
		{name: "duplicate source identity", values: []*telemetryv1.TrafficWindow{{WindowId: "window-1"}, {WindowId: "window-1"}},
			rejected: []*telemetryv1.RejectedEvent{{EventId: "window-1"}}},
		{name: "empty source identity", values: []*telemetryv1.TrafficWindow{{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := splitRejectedWindows(test.values, test.rejected); err == nil {
				t.Fatal("ambiguous traffic window acknowledgement must preserve the source frame")
			}
		})
	}
}

func TestSplitRejectedCandidatesRejectsAmbiguousIdentities(t *testing.T) {
	tests := []struct {
		name     string
		values   []*telemetryv1.ReviewCandidate
		rejected []*telemetryv1.RejectedEvent
	}{
		{name: "unknown acknowledgement", values: []*telemetryv1.ReviewCandidate{{CandidateId: "candidate-1"}},
			rejected: []*telemetryv1.RejectedEvent{{EventId: "unknown"}}},
		{name: "duplicate acknowledgement", values: []*telemetryv1.ReviewCandidate{{CandidateId: "candidate-1"}},
			rejected: []*telemetryv1.RejectedEvent{{EventId: "candidate-1"}, {EventId: "candidate-1"}}},
		{name: "duplicate source identity", values: []*telemetryv1.ReviewCandidate{{CandidateId: "candidate-1"}, {CandidateId: "candidate-1"}},
			rejected: []*telemetryv1.RejectedEvent{{EventId: "candidate-1"}}},
		{name: "empty source identity", values: []*telemetryv1.ReviewCandidate{{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := splitRejectedCandidates(test.values, test.rejected); err == nil {
				t.Fatal("ambiguous review candidate acknowledgement must preserve the source frame")
			}
		})
	}
}

func TestReviewUploadLoopRetriesWindowsBeforeUploadingCandidates(t *testing.T) {
	server := &trafficReviewUploadServer{windowFailuresLeft: 1, callNotifications: make(chan string, 4)}
	_, handler := telemetryv1connect.NewTelemetryServiceHandler(server)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	spool, err := edgeclient.NewReviewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendWindows([]*telemetryv1.TrafficWindow{{WindowId: "window-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendCandidates([]*telemetryv1.ReviewCandidate{{CandidateId: "candidate-1"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reviewUploadLoopWithInterval(ctx, edgeclient.New(httpServer.URL, httpServer.Client()),
			&edgeclient.Session{Token: "unit-access"}, spool, 5*time.Millisecond)
		close(done)
	}()
	for index, want := range []string{"windows", "windows", "candidates"} {
		select {
		case got := <-server.callNotifications:
			if got != want {
				t.Fatalf("upload call %d=%s want=%s", index, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for upload call %d", index)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		windowFiles, windowErr := spool.WindowFiles()
		candidateFiles, candidateErr := spool.CandidateFiles()
		if windowErr != nil || candidateErr != nil {
			t.Fatalf("list traffic review spool: windows=%v candidates=%v", windowErr, candidateErr)
		}
		if len(windowFiles) == 0 && len(candidateFiles) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("acknowledged spool files remain: windows=%v candidates=%v", windowFiles, candidateFiles)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("traffic review upload loop did not stop")
	}
}

func TestDrainTrafficReviewRetriesSnapshotAfterSpoolBecomesAvailable(t *testing.T) {
	policy := edgecore.DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY
	collector, err := edgecore.NewReviewCollector(policy, "sha256:retry", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	collector.Observe(start, "unit-1", "asset-1", "request-1", edgecore.Request{Method: "GET", Path: "/health"},
		edgecore.Decision{Action: edgecore.ActionAllow, GenerationID: "generation-1", GenerationSeq: 1})
	runtime := &edgeRuntime{reviewCollector: collector}
	runtime.drainTrafficReview(start.Add(6 * time.Minute))
	windows, _, _ := collector.PrepareDrain(start.Add(7 * time.Minute))
	if len(windows) != 1 {
		t.Fatalf("failed spool attempt lost frozen snapshot: windows=%d", len(windows))
	}
	runtime.reviewSpool, err = edgeclient.NewReviewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime.drainTrafficReview(start.Add(7 * time.Minute))
	files, err := runtime.reviewSpool.WindowFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("retried snapshot files=%v", files)
	}
	remaining, _, _ := collector.PrepareDrain(start.Add(8 * time.Minute))
	if len(remaining) != 0 {
		t.Fatalf("persisted snapshot was not committed: %v", remaining)
	}
}
