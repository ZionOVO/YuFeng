package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
	"yufeng/proto/gen/modelsidev1/modelsidev1connect"
)

type connectModelTrafficSender struct {
	client modelsidev1connect.ModelSideIngressServiceClient
}

func newModelTrafficSender(endpoint, caFile, certFile, keyFile string, devInsecure bool) (modelTrafficSender, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}
	var client *http.Client
	baseURL := endpoint
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		socketPath := strings.TrimPrefix(endpoint, "unix://")
		if !filepath.IsAbs(socketPath) {
			return nil, errors.New("modelside unix socket path must be absolute")
		}
		dialer := &net.Dialer{Timeout: kernel.ModelSideIngressTimeout}
		client = &http.Client{
			Timeout: kernel.ModelSideIngressTimeout,
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			}},
		}
		baseURL = "http://modelside"
	case strings.HasPrefix(endpoint, "https://"):
		if strings.TrimSpace(caFile) == "" || strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" {
			return nil, errors.New("remote modelside requires mutual tls ca, certificate and key")
		}
		var err error
		client, err = kernel.HTTPClient(caFile, certFile, keyFile)
		if err != nil {
			return nil, err
		}
		client.Timeout = kernel.ModelSideIngressTimeout
	case strings.HasPrefix(endpoint, "http://") && devInsecure:
		client = &http.Client{Timeout: kernel.ModelSideIngressTimeout}
	default:
		return nil, errors.New("modelside endpoint must use unix socket or mutual tls")
	}
	return &connectModelTrafficSender{client: modelsidev1connect.NewModelSideIngressServiceClient(
		client, baseURL, connect.WithProtoJSON(), connect.WithSendMaxBytes(kernel.ModelSideIngressReceiveMaxBytes),
	)}, nil
}

func (s *connectModelTrafficSender) SubmitTraffic(ctx context.Context, batch *edgecore.ModelIngressBatch) (uint32, error) {
	if s == nil || s.client == nil || batch == nil || batch.Profile == nil || len(batch.Traffic) == 0 || batch.ProfileDigest == "" {
		return 0, errors.New("modelside traffic batch is incomplete")
	}
	callCtx, cancel := context.WithTimeout(ctx, kernel.ModelSideIngressTimeout)
	defer cancel()
	response, err := s.client.SubmitTraffic(callCtx, connect.NewRequest(&modelsidev1.SubmitTrafficRequest{
		ModelProfile: batch.Profile, ModelProfileDigest: batch.ProfileDigest, Traffic: batch.Traffic,
	}))
	if err != nil {
		return 0, err
	}
	accepted := response.Msg.GetAccepted()
	if accepted < 0 || uint64(accepted)+uint64(len(response.Msg.GetDropped())) != uint64(len(batch.Traffic)) {
		return 0, errors.New("modelside returned an invalid traffic acknowledgement")
	}
	return uint32(accepted), nil
}

var _ modelTrafficSender = (*connectModelTrafficSender)(nil)
