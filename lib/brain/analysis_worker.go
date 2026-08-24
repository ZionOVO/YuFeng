package brain

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	workerv1 "yufeng/proto/gen/workerv1"
)

func normalizeAnalyzerProfiles(kind workerv1.WorkerKind, supplied []*workerv1.AnalyzerProfile, requestedMax int32) ([]*workerv1.AnalyzerProfile, []byte, int32, error) {
	if kind != workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR {
		return nil, nil, 0, errors.New("analysis worker kind is retired")
	}
	if len(supplied) != 0 {
		return nil, nil, 0, errors.New("run supervisor cannot register analyzer profiles")
	}
	if requestedMax == 0 {
		requestedMax = 1
	}
	if requestedMax < 1 || requestedMax > 64 {
		return nil, nil, 0, errors.New("worker max_concurrency must be between 1 and 64")
	}
	return nil, []byte(`[]`), requestedMax, nil
}

// PollAnalysisWork 为历史线缆编码返回退役状态。
func (s *WorkerServer) PollAnalysisWork(context.Context, *connect.Request[workerv1.PollAnalysisWorkRequest]) (*connect.Response[workerv1.PollAnalysisWorkResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("analysis worker lane is retired"))
}

// ExtendAnalysisLease 为历史线缆编码返回退役状态。
func (s *WorkerServer) ExtendAnalysisLease(context.Context, *connect.Request[workerv1.ExtendAnalysisLeaseRequest]) (*connect.Response[workerv1.ExtendAnalysisLeaseResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("analysis worker lane is retired"))
}

// CompleteAnalysisWork 为历史线缆编码返回退役状态。
func (s *WorkerServer) CompleteAnalysisWork(context.Context, *connect.Request[workerv1.CompleteAnalysisWorkRequest]) (*connect.Response[workerv1.CompleteAnalysisWorkResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("analysis worker lane is retired"))
}

// FailAnalysisWork 为历史线缆编码返回退役状态。
func (s *WorkerServer) FailAnalysisWork(context.Context, *connect.Request[workerv1.FailAnalysisWorkRequest]) (*connect.Response[workerv1.FailAnalysisWorkResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("analysis worker lane is retired"))
}
