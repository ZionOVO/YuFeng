package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
	"yufeng/proto/gen/modelsidev1/modelsidev1connect"
)

type modelIngressAcknowledgementServer struct {
	response *modelsidev1.SubmitTrafficResponse
	request  *modelsidev1.SubmitTrafficRequest
	calls    int
}

func (s *modelIngressAcknowledgementServer) SubmitTraffic(_ context.Context, req *connect.Request[modelsidev1.SubmitTrafficRequest]) (*connect.Response[modelsidev1.SubmitTrafficResponse], error) {
	s.calls++
	s.request = req.Msg
	return connect.NewResponse(s.response), nil
}

func TestModelTrafficSenderSubmitsOneBatchAndValidatesAccounting(t *testing.T) {
	service := &modelIngressAcknowledgementServer{response: &modelsidev1.SubmitTrafficResponse{
		Accepted: 1,
		Dropped:  []*modelsidev1.RejectedTraffic{{RequestId: "request-2", Code: "ingress_queue_full"}},
	}}
	_, handler := modelsidev1connect.NewModelSideIngressServiceHandler(service)
	server := httptest.NewServer(handler)
	defer server.Close()
	sender, err := newModelTrafficSender(server.URL, "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	batch := &edgecore.ModelIngressBatch{
		Profile: kernel.DefaultModelProfile(), ProfileDigest: "sha256:profile",
		Traffic: []*modelsidev1.NormalizedTraffic{
			{RequestId: "request-1", ModelProfileDigest: "sha256:profile"},
			{RequestId: "request-2", ModelProfileDigest: "sha256:profile"},
		},
	}
	accepted, err := sender.SubmitTraffic(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || service.calls != 1 || len(service.request.GetTraffic()) != 2 || service.request.GetModelProfileDigest() != batch.ProfileDigest {
		t.Fatalf("accepted=%d calls=%d request=%v", accepted, service.calls, service.request)
	}

	service.response = &modelsidev1.SubmitTrafficResponse{Accepted: 2, Dropped: []*modelsidev1.RejectedTraffic{{RequestId: "request-2"}}}
	if _, err := sender.SubmitTraffic(context.Background(), batch); err == nil {
		t.Fatal("inconsistent ModelSide accounting must fail the whole at-most-once batch")
	}
	if service.calls != 2 {
		t.Fatalf("sender retried an invalid acknowledgement: calls=%d", service.calls)
	}
}
