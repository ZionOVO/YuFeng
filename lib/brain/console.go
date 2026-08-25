package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "yufeng/proto/gen/commonv1"
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
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE ingested_at > now() - interval '24 hours'
		AND kind='model_alert' AND asset_id = ANY($1)`, ids).Scan(&resp.ModelAlerts_24H); err != nil {
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
	scope := scopeFromAccess(access)
	if !scope.hasTool("console.read") {
		return nil, objectDenied()
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
	if !scope.coversAsset(e.AssetId) {
		return nil, objectDenied()
	}
	resp := &consolev1.GetEventResponse{Event: &e}
	rows, err := s.pool.Query(ctx, `SELECT inference_id,event_id,model_group,model_type,model_version,
		threshold,score,attack_class,taxonomy_version,recorded_at,model_profile_digest,request_id,result_kind
		FROM model_inferences WHERE event_id=$1 ORDER BY recorded_at,inference_id`, e.Id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var inference eventv1.ModelInference
		var attackClass string
		var recordedAt time.Time
		if err := rows.Scan(&inference.InferenceId, &inference.EventId, &inference.ModelGroup, &inference.ModelType,
			&inference.ModelVersion, &inference.Threshold, &inference.Score, &attackClass, &inference.TaxonomyVersion,
			&recordedAt, &inference.ModelProfileDigest, &inference.RequestId, &inference.ResultKind); err != nil {
			rows.Close()
			return nil, err
		}
		inference.AttackClass = attackClassEnum(attackClass)
		inference.RecordedAt = timestamppb.New(recordedAt)
		resp.ModelInferences = append(resp.ModelInferences, &inference)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	deliveries, err := loadEventTriageDeliveries(ctx, s.pool, e.Id)
	if err != nil {
		return nil, err
	}
	resp.TriageDeliveries = deliveries
	return connect.NewResponse(resp), nil
}

func attackClassEnum(value string) commonv1.AttackClass {
	if number, ok := commonv1.AttackClass_value[strings.TrimSpace(value)]; ok {
		return commonv1.AttackClass(number)
	}
	return commonv1.AttackClass_ATTACK_CLASS_UNSPECIFIED
}

func loadEventTriageDeliveries(ctx context.Context, db dbTX, eventID string) ([]*consolev1.TriageDelivery, error) {
	caseRows, err := db.Query(ctx, `SELECT DISTINCT r.case_id,i.instruction_id,i.agent_id,i.kind,i.status,i.created_at,i.acked_at
		FROM model_result_receipts r
		LEFT JOIN triage_clusters c ON c.event_ids @> jsonb_build_array(r.event_id)
		LEFT JOIN agent_threads th ON th.source_kind='triage' AND th.source_ref=c.cluster_id
		LEFT JOIN agent_turns t ON t.thread_id=th.thread_id
		LEFT JOIN agent_instructions i ON i.turn_id=t.turn_id OR i.payload_ref=c.cluster_id OR i.payload_ref=r.case_id
		WHERE r.event_id=$1 AND r.case_id<>''
		ORDER BY r.case_id,i.created_at,i.instruction_id`, eventID)
	if err != nil {
		return nil, err
	}
	out := []*consolev1.TriageDelivery{}
	for caseRows.Next() {
		delivery, err := scanTriageDelivery(caseRows)
		if err != nil {
			caseRows.Close()
			return nil, err
		}
		out = append(out, delivery)
	}
	if err := caseRows.Err(); err != nil {
		caseRows.Close()
		return nil, err
	}
	caseRows.Close()
	if len(out) > 0 {
		return out, nil
	}
	eventRaw, err := json.Marshal([]string{eventID})
	if err != nil {
		return nil, err
	}
	triageRows, err := db.Query(ctx, `SELECT DISTINCT '' AS case_id,i.instruction_id,i.agent_id,i.kind,i.status,i.created_at,i.acked_at
		FROM triage_clusters c
		JOIN agent_threads th ON th.source_kind='triage' AND th.source_ref=c.cluster_id
		JOIN agent_turns t ON t.thread_id=th.thread_id
		JOIN agent_instructions i ON i.turn_id=t.turn_id
		WHERE c.event_ids @> $1::jsonb
		UNION
		SELECT DISTINCT '' AS case_id,i.instruction_id,i.agent_id,i.kind,i.status,i.created_at,i.acked_at
		FROM triage_clusters c JOIN agent_instructions i ON i.payload_ref=c.cluster_id
		WHERE c.event_ids @> $1::jsonb
		ORDER BY created_at,instruction_id`, eventRaw)
	if err != nil {
		return nil, err
	}
	defer triageRows.Close()
	for triageRows.Next() {
		delivery, err := scanTriageDelivery(triageRows)
		if err != nil {
			return nil, err
		}
		out = append(out, delivery)
	}
	return out, triageRows.Err()
}

func scanTriageDelivery(row enrollmentScanner) (*consolev1.TriageDelivery, error) {
	var caseID string
	var instructionID, handlerID, kind, status *string
	var createdAt, acknowledgedAt *time.Time
	if err := row.Scan(&caseID, &instructionID, &handlerID, &kind, &status, &createdAt, &acknowledgedAt); err != nil {
		return nil, err
	}
	delivery := &consolev1.TriageDelivery{CaseId: caseID}
	if instructionID == nil {
		delivery.Status = "not_queued"
		return delivery, nil
	}
	delivery.InstructionId = *instructionID
	delivery.HandlerId = *handlerID
	delivery.Kind = *kind
	delivery.Status = *status
	if createdAt != nil {
		delivery.CreatedAt = timestamppb.New(*createdAt)
	}
	if acknowledgedAt != nil {
		delivery.AcknowledgedAt = timestamppb.New(*acknowledgedAt)
	}
	return delivery, nil
}
