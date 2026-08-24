package brain

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	consolev1 "yufeng/proto/gen/consolev1"
	"yufeng/proto/gen/consolev1/consolev1connect"
	eventv1 "yufeng/proto/gen/eventv1"
)

// ConsoleServer 是控制台查询服务。
type ConsoleServer struct {
	pool *pgxpool.Pool
}

// NewConsoleServer 构造控制台服务。
func NewConsoleServer(pool *pgxpool.Pool) *ConsoleServer { return &ConsoleServer{pool: pool} }

// Handler 返回 Connect 服务端处理器。
func (s *ConsoleServer) Handler() (string, http.Handler) {
	return consolev1connect.NewConsoleServiceHandler(s, handlerOptions()...)
}

// Dashboard 按账户授权范围汇总控制台首页所需的资产、事件与发布状态。
func (s *ConsoleServer) Dashboard(ctx context.Context, req *connect.Request[consolev1.DashboardRequest]) (*connect.Response[consolev1.DashboardResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	resp := &consolev1.DashboardResponse{ReleasesByState: map[string]int64{}}
	if !scope.hasTool("console.read") || scope.emptyObjects() {
		return connect.NewResponse(resp), nil
	}
	ids := scope.assetIDs()
	// 仪表盘统计失败即整体失败：返回全零的"健康"面板比报错更危险。
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM assets WHERE asset_id = ANY($1)`, ids).Scan(&resp.AssetsTotal); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM units u
		WHERE u.health='degraded' AND (
			u.unit_id = ANY($2) OR EXISTS (SELECT 1 FROM unit_assets ua WHERE ua.unit_id=u.unit_id AND ua.asset_id = ANY($1))
		)`, ids, mapKeys(scope.units)).Scan(&resp.DegradedUnits); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE ingested_at > now() - interval '24 hours' AND asset_id = ANY($1)`, ids).Scan(&resp.Events_24HTotal); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE ingested_at > now() - interval '24 hours' AND verdict='block' AND asset_id = ANY($1)`, ids).Scan(&resp.Events_24HBlocked); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT r.state, count(*) FROM releases r
		WHERE EXISTS (SELECT 1 FROM release_assets ra WHERE ra.release_id=r.release_id AND ra.asset_id = ANY($1))
		   OR r.release_id = ANY($2)
		GROUP BY r.state`, ids, mapKeys(scope.releases))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		resp.ReleasesByState[releaseStateEnum(state).String()] = n
	}
	return connect.NewResponse(resp), rows.Err()
}

// ListEvents 按授权资产范围和查询条件分页列出安全事件。
func (s *ConsoleServer) ListEvents(ctx context.Context, req *connect.Request[consolev1.ListEventsRequest]) (*connect.Response[consolev1.ListEventsResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool("console.read") || scope.emptyObjects() {
		return connect.NewResponse(&consolev1.ListEventsResponse{}), nil
	}
	if req.Msg.AssetId != "" && !scope.coversAsset(req.Msg.AssetId) {
		return nil, objectDenied()
	}
	verdict := ""
	if req.Msg.Verdict != eventv1.Verdict_VERDICT_UNSPECIFIED {
		verdict = eventVerdictString(req.Msg.Verdict)
	}
	kind := ""
	if req.Msg.Kind != eventv1.Kind_KIND_UNSPECIFIED {
		kind = eventKindString(req.Msg.Kind)
	}
	var since, until *time.Time
	if ts := req.Msg.GetSince(); ts != nil && ts.IsValid() {
		t := ts.AsTime()
		since = &t
	}
	if ts := req.Msg.GetUntil(); ts != nil && ts.IsValid() {
		t := ts.AsTime()
		until = &t
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM events
	WHERE asset_id = ANY($4) AND ($1='' OR asset_id=$1) AND ($2='' OR verdict=$2) AND ($3='' OR kind=$3)
	  AND ($5::timestamptz IS NULL OR occurred_at >= $5)
	  AND ($6::timestamptz IS NULL OR occurred_at < $6)
	  AND ($7='' OR payload::text ILIKE '%'||$7||'%')
	  AND ($8='' OR release_traces::text ILIKE '%'||$8||'%')
	ORDER BY occurred_at DESC LIMIT $9 OFFSET $10`,
		req.Msg.AssetId, verdict, kind, scope.assetIDs(), since, until, req.Msg.GetQuery(), req.Msg.GetReleaseId(), limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &consolev1.ListEventsResponse{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e eventv1.Event
		if err := protojson.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		resp.Events = append(resp.Events, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Events) > limit {
		resp.Events = resp.Events[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// GetEvent 返回调用者有权查看的单个安全事件及其治理关联。
func (s *ConsoleServer) GetEvent(ctx context.Context, req *connect.Request[consolev1.GetEventRequest]) (*connect.Response[consolev1.GetEventResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT payload FROM events WHERE event_id=$1`, req.Msg.EventId).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, objectDenied()
	}
	if err != nil {
		return nil, err
	}
	var e eventv1.Event
	if err := protojson.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	if !scopeFromAccess(access).coversAsset(e.AssetId) {
		return nil, objectDenied()
	}
	return connect.NewResponse(&consolev1.GetEventResponse{Event: &e}), nil
}
