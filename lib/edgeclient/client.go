package edgeclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	artifactv1 "yufeng/proto/gen/artifactv1"
	"yufeng/proto/gen/artifactv1/artifactv1connect"
	eventv1 "yufeng/proto/gen/eventv1"
	evidencev1 "yufeng/proto/gen/evidencev1"
	"yufeng/proto/gen/evidencev1/evidencev1connect"
	registryv1 "yufeng/proto/gen/registryv1"
	"yufeng/proto/gen/registryv1/registryv1connect"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	"yufeng/proto/gen/telemetryv1/telemetryv1connect"

	"yufeng/lib/kernel"
)

// defaultHeartbeatInterval 是中台未返回心跳间隔时的兜底值。
const defaultHeartbeatInterval = 30 * time.Second

// Client 是中台客户端。HTTPClient 为 nil 时使用默认客户端。
type Client struct {
	// BootstrapToken 是单元首次注册用的部署级引导令牌；非空时随 Register
	// 请求发送，中台据此放行注册（已注册单元凭会话令牌即可）。
	BootstrapToken string

	registry  registryv1connect.RegistryServiceClient
	artifacts artifactv1connect.ArtifactServiceClient
	telemetry telemetryv1connect.TelemetryServiceClient
	evidence  evidencev1connect.EvidenceServiceClient
}

// Session 是注册后的单元会话。
type Session struct {
	mu                sync.Mutex
	UnitID            string
	AssetID           string
	Token             string
	Refresh           string
	HeartbeatInterval time.Duration
	Cursor            string
	TokenIssuedAt     time.Time
}

// SessionSnapshot 是并发安全的会话只读副本。
type SessionSnapshot struct {
	UnitID            string
	AssetID           string
	Token             string
	Refresh           string
	HeartbeatInterval time.Duration
	Cursor            string
	TokenIssuedAt     time.Time
}

// Snapshot 返回并发安全的会话只读副本。
func (s *Session) Snapshot() SessionSnapshot {
	if s == nil {
		return SessionSnapshot{}
	}
	unit, asset, token, refresh, cursor, issued, hb := s.snapshot()
	return SessionSnapshot{UnitID: unit, AssetID: asset, Token: token, Refresh: refresh, Cursor: cursor, TokenIssuedAt: issued, HeartbeatInterval: hb}
}

// snapshot 在会话锁内复制注册身份、令牌、游标与心跳状态。
func (s *Session) snapshot() (unit, asset, token, refresh, cursor string, issued time.Time, hb time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.UnitID, s.AssetID, s.Token, s.Refresh, s.Cursor, s.TokenIssuedAt, s.HeartbeatInterval
}

// New 构造客户端。
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: kernel.ControlPlaneHTTPTimeout}
	}
	return &Client{
		registry:  registryv1connect.NewRegistryServiceClient(hc, baseURL),
		artifacts: artifactv1connect.NewArtifactServiceClient(hc, baseURL),
		telemetry: telemetryv1connect.NewTelemetryServiceClient(hc, baseURL),
		evidence:  evidencev1connect.NewEvidenceServiceClient(hc, baseURL),
	}
}

// PollEvidenceRequests 主动长轮询当前单元已批准的证据请求。
func (c *Client) PollEvidenceRequests(ctx context.Context, sess *Session, longPollSeconds int32) (*evidencev1.PollEvidenceRequestsResponse, error) {
	unitID := sess.Snapshot().UnitID
	resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[evidencev1.PollEvidenceRequestsResponse], error) {
		req := connect.NewRequest(&evidencev1.PollEvidenceRequestsRequest{UnitId: unitID, LongPollSeconds: longPollSeconds})
		req.Header().Set("Authorization", "Bearer "+token)
		return c.evidence.PollEvidenceRequests(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("poll evidence requests: %w", err)
	}
	return resp.Msg, nil
}

// SubmitEvidenceBundle 提交精确字段片段；客户端不缓存已提交正文。
func (c *Client) SubmitEvidenceBundle(ctx context.Context, sess *Session, bundle *evidencev1.SubmitEvidenceBundleRequest) (*evidencev1.SubmitEvidenceBundleResponse, error) {
	resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[evidencev1.SubmitEvidenceBundleResponse], error) {
		req := connect.NewRequest(bundle)
		req.Header().Set("Authorization", "Bearer "+token)
		return c.evidence.SubmitEvidenceBundle(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("submit evidence bundle: %w", err)
	}
	return resp.Msg, nil
}

// Register 注册单元并返回会话。配置了 BootstrapToken 时随请求发送。
func (c *Client) Register(ctx context.Context, req *registryv1.RegisterRequest) (*Session, error) {
	creq := connect.NewRequest(req)
	if c.BootstrapToken != "" {
		creq.Header().Set("Authorization", "Bearer "+c.BootstrapToken)
	}
	resp, err := c.registry.Register(ctx, creq)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	interval := time.Duration(resp.Msg.HeartbeatInterval) * time.Second
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	return &Session{UnitID: resp.Msg.UnitId, AssetID: resp.Msg.AssetId, Token: resp.Msg.Token, Refresh: resp.Msg.RefreshToken, HeartbeatInterval: interval, TokenIssuedAt: time.Now()}, nil
}

// Refresh 用刷新令牌轮换访问令牌；中台重启后走这条，不得重跑公开注册。
func (c *Client) Refresh(ctx context.Context, unitID, refresh string) (*Session, error) {
	resp, err := c.registry.Refresh(ctx, connect.NewRequest(&registryv1.RefreshRequest{UnitId: unitID, RefreshToken: refresh}))
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	return &Session{
		UnitID: resp.Msg.UnitId, Token: resp.Msg.Token, Refresh: resp.Msg.RefreshToken,
		HeartbeatInterval: defaultHeartbeatInterval, TokenIssuedAt: time.Now(),
	}, nil
}

// EnsureAccess 在访问令牌过半寿时用刷新令牌轮换。
func (c *Client) EnsureAccess(ctx context.Context, sess *Session) error {
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.Refresh == "" {
		return nil
	}
	if !sess.TokenIssuedAt.IsZero() && time.Since(sess.TokenIssuedAt) < kernel.AccessTokenTTL/2 {
		return nil
	}
	return c.refreshLocked(ctx, sess)
}

// refreshLocked 在调用方持有会话锁时轮换访问令牌并更新会话状态。
func (c *Client) refreshLocked(ctx context.Context, sess *Session) error {
	next, err := c.Refresh(ctx, sess.UnitID, sess.Refresh)
	if err != nil {
		return err
	}
	sess.Token = next.Token
	if next.Refresh != "" {
		sess.Refresh = next.Refresh
	}
	sess.TokenIssuedAt = next.TokenIssuedAt
	if next.HeartbeatInterval > 0 {
		sess.HeartbeatInterval = next.HeartbeatInterval
	}
	return nil
}

// refreshRejected 仅在被拒令牌仍是当前令牌时执行一次串行轮换。
func (c *Client) refreshRejected(ctx context.Context, sess *Session, rejectedToken string) error {
	if sess == nil {
		return fmt.Errorf("session is required")
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.Token != rejectedToken {
		return nil
	}
	if sess.Refresh == "" {
		return fmt.Errorf("refresh token is missing")
	}
	return c.refreshLocked(ctx, sess)
}

// callWithSessionRefresh 在访问令牌失效时轮换一次令牌并重试同一远程调用。
func callWithSessionRefresh[T any](ctx context.Context, c *Client, sess *Session, call func(string) (T, error)) (T, error) {
	_, _, token, _, _, _, _ := sess.snapshot()
	result, err := call(token)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		return result, err
	}
	if refreshErr := c.refreshRejected(ctx, sess, token); refreshErr != nil {
		var zero T
		return zero, fmt.Errorf("refresh rejected access: %w", refreshErr)
	}
	_, _, token, _, _, _, _ = sess.snapshot()
	return call(token)
}

// ReleasePage 是 ListReleases 的一页：世代信封优先于逐条 items。
type ReleasePage struct {
	Items      []*artifactv1.ReleaseItem
	NextCursor string
	Snapshot   bool
	HasMore    bool
	Generation *artifactv1.AssetGeneration
}

// ListReleases 拉取发布；首次快照，之后游标增量。
func (c *Client) ListReleases(ctx context.Context, sess *Session, full bool) (*ReleasePage, error) {
	unit, _, _, _, cursor, _, _ := sess.snapshot()
	resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[artifactv1.ListReleasesResponse], error) {
		req := connect.NewRequest(&artifactv1.ListReleasesRequest{
			UnitId: unit, Cursor: cursor, FullSnapshot: full || cursor == "" || strings.HasPrefix(cursor, "s:"),
		})
		req.Header().Set("Authorization", "Bearer "+token)
		return c.artifacts.ListReleases(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	return &ReleasePage{
		Items: resp.Msg.Items, NextCursor: resp.Msg.NextCursor,
		Snapshot: resp.Msg.Snapshot, HasMore: resp.Msg.HasMore,
		Generation: resp.Msg.Generation,
	}, nil
}

// ListGenerations 按序号追赶已签名资产世代，直到服务端 has_more 为假。
func (c *Client) ListGenerations(ctx context.Context, sess *Session, assetID string, sinceSeq int64) ([]*artifactv1.AssetGeneration, error) {
	unit, _, _, _, _, _, _ := sess.snapshot()
	var out []*artifactv1.AssetGeneration
	since := sinceSeq
	for {
		resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[artifactv1.ListGenerationsResponse], error) {
			req := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: unit, AssetId: assetID, SinceSeq: since})
			req.Header().Set("Authorization", "Bearer "+token)
			return c.artifacts.ListGenerations(ctx, req)
		})
		if err != nil {
			return nil, fmt.Errorf("list generations: %w", err)
		}
		page := resp.Msg.GetGenerations()
		out = append(out, page...)
		if !resp.Msg.GetHasMore() {
			return out, nil
		}
		if len(page) == 0 {
			return nil, fmt.Errorf("list generations: has_more with empty page")
		}
		next := page[len(page)-1].GetGenerationSeq()
		if next <= since {
			return nil, fmt.Errorf("list generations: sequence did not advance")
		}
		since = next
	}
}

// ListUnitListenPlans 按版本追赶当前会话单元的已签名监听计划。
func (c *Client) ListUnitListenPlans(ctx context.Context, sess *Session, sinceVersion uint64) ([]*artifactv1.UnitListenPlan, error) {
	unit, _, _, _, _, _, _ := sess.snapshot()
	var out []*artifactv1.UnitListenPlan
	since := sinceVersion
	for {
		resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[artifactv1.ListUnitListenPlansResponse], error) {
			req := connect.NewRequest(&artifactv1.ListUnitListenPlansRequest{UnitId: unit, SinceVersion: since})
			req.Header().Set("Authorization", "Bearer "+token)
			return c.artifacts.ListUnitListenPlans(ctx, req)
		})
		if err != nil {
			return nil, fmt.Errorf("list unit listen plans: %w", err)
		}
		page := resp.Msg.GetPlans()
		out = append(out, page...)
		if !resp.Msg.GetHasMore() {
			return out, nil
		}
		if len(page) == 0 {
			return nil, fmt.Errorf("list unit listen plans: has_more with empty page")
		}
		next := page[len(page)-1].GetVersion()
		if next <= since {
			return nil, fmt.Errorf("list unit listen plans: version did not advance")
		}
		since = next
	}
}

// CommitCursor 在制品验签并落盘成功后推进会话游标。
func CommitCursor(sess *Session, cursor string) {
	if sess != nil {
		sess.mu.Lock()
		sess.Cursor = cursor
		sess.mu.Unlock()
	}
}

// UploadEvents 上传一批事件。
func (c *Client) UploadEvents(ctx context.Context, sess *Session, events []*eventv1.Event) (*telemetryv1.UploadEventsResponse, error) {
	resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[telemetryv1.UploadEventsResponse], error) {
		req := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: events})
		req.Header().Set("Authorization", "Bearer "+token)
		return c.telemetry.UploadEvents(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("upload events: %w", err)
	}
	return resp.Msg, nil
}

// UploadTrafficWindows 优先上传有界统计窗。
func (c *Client) UploadTrafficWindows(ctx context.Context, sess *Session, windows []*telemetryv1.TrafficWindow) (*telemetryv1.UploadTrafficWindowsResponse, error) {
	resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[telemetryv1.UploadTrafficWindowsResponse], error) {
		req := connect.NewRequest(&telemetryv1.UploadTrafficWindowsRequest{Windows: windows})
		req.Header().Set("Authorization", "Bearer "+token)
		return c.telemetry.UploadTrafficWindows(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("upload traffic windows: %w", err)
	}
	return resp.Msg, nil
}

// UploadReviewCandidates 在统计窗之后上传不含原文的代表候选。
func (c *Client) UploadReviewCandidates(ctx context.Context, sess *Session, candidates []*telemetryv1.ReviewCandidate) (*telemetryv1.UploadReviewCandidatesResponse, error) {
	resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[telemetryv1.UploadReviewCandidatesResponse], error) {
		req := connect.NewRequest(&telemetryv1.UploadReviewCandidatesRequest{Candidates: candidates})
		req.Header().Set("Authorization", "Bearer "+token)
		return c.telemetry.UploadReviewCandidates(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("upload review candidates: %w", err)
	}
	return resp.Msg, nil
}

// Heartbeat 上报心跳与计数器。
func (c *Client) Heartbeat(ctx context.Context, sess *Session, req *registryv1.HeartbeatRequest) (*registryv1.HeartbeatResponse, error) {
	resp, err := callWithSessionRefresh(ctx, c, sess, func(token string) (*connect.Response[registryv1.HeartbeatResponse], error) {
		creq := connect.NewRequest(req)
		creq.Header().Set("Authorization", "Bearer "+token)
		return c.registry.Heartbeat(ctx, creq)
	})
	if err != nil {
		return nil, fmt.Errorf("heartbeat: %w", err)
	}
	return resp.Msg, nil
}
