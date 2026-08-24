// Package brain 实现中台服务端装配与各契约服务。
package brain

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/store"

	healthv1 "yufeng/proto/gen/healthv1"
	"yufeng/proto/gen/healthv1/healthv1connect"
)

// HealthServer 提供 Connect 版健康检查。
type HealthServer struct {
	store           *store.Store
	version         string
	contractVersion string
	buildSHA        string
	buildTime       string
}

// NewHealthServer 构造健康服务。
func NewHealthServer(st *store.Store, version, contractVersion, buildSHA, buildTime string) *HealthServer {
	return &HealthServer{
		store:           st,
		version:         version,
		contractVersion: contractVersion,
		buildSHA:        buildSHA,
		buildTime:       buildTime,
	}
}

// Handler 返回 Connect 服务端处理器。
func (h *HealthServer) Handler() (string, http.Handler) {
	return healthv1connect.NewHealthServiceHandler(h, handlerOptions()...)
}

// Livez 报告中台进程是否仍能处理请求，不检查外部依赖。
func (h *HealthServer) Livez(ctx context.Context, _ *connect.Request[healthv1.LivezRequest]) (*connect.Response[healthv1.LivezResponse], error) {
	return connect.NewResponse(&healthv1.LivezResponse{
		Status:     "ok",
		ServerTime: timestamppb.Now(),
	}), nil
}

// pingPostgres 以固定超时探测连接池；超文本传输协议 /readyz 与 Connect Readyz 共用，
// 超时值只此一处。
func pingPostgres(ctx context.Context, st *store.Store) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return st.Pool().Ping(pingCtx)
}

// Readyz 检查数据库等必需依赖后报告中台是否可接收业务请求。
func (h *HealthServer) Readyz(ctx context.Context, _ *connect.Request[healthv1.ReadyzRequest]) (*connect.Response[healthv1.ReadyzResponse], error) {
	if err := pingPostgres(ctx, h.store); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&healthv1.ReadyzResponse{
		Status:     "ok",
		ServerTime: timestamppb.Now(),
	}), nil
}

// Version 返回当前构建版本与提交标识。
func (h *HealthServer) Version(ctx context.Context, _ *connect.Request[healthv1.VersionRequest]) (*connect.Response[healthv1.VersionResponse], error) {
	return connect.NewResponse(&healthv1.VersionResponse{
		Version:         h.version,
		ContractVersion: h.contractVersion,
		BuildSha:        h.buildSHA,
		BuildTime:       h.buildTime,
	}), nil
}
