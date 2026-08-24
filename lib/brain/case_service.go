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

	casev1 "yufeng/proto/gen/casev1"
	"yufeng/proto/gen/casev1/casev1connect"
	modelv1 "yufeng/proto/gen/modelv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

// CaseServer 提供资产授权裁剪后的案件及只追加活动读取。
type CaseServer struct {
	pool *pgxpool.Pool
}

const investigationCaseSelectColumns = `case_id, module_id, asset_id, cluster_id, state, priority, title,
	summary, representatives, finding, shadow_release_id, created_at, updated_at,
	assigned_agent_id, assigned_agent_display_name, resolution, automation_suppressed_reason,
	assigned_run_id, assigned_agent_config_digest, resolved_at`

// NewCaseServer 构造案件服务。
func NewCaseServer(pool *pgxpool.Pool) *CaseServer { return &CaseServer{pool: pool} }

// Handler 返回 Connect 服务端处理器。
func (s *CaseServer) Handler() (string, http.Handler) {
	return casev1connect.NewCaseServiceHandler(s, handlerOptions()...)
}

// ListCases 返回调用者资产 Bindings 内的案件。
func (s *CaseServer) ListCases(ctx context.Context, req *connect.Request[casev1.ListCasesRequest]) (*connect.Response[casev1.ListCasesResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool("case.read") || scope.emptyObjects() {
		return nil, objectDenied()
	}
	assetIDs := scope.assetIDs()
	if req.Msg.GetAssetId() != "" {
		if !scope.coversAsset(req.Msg.GetAssetId()) {
			return nil, objectDenied()
		}
		assetIDs = []string{req.Msg.GetAssetId()}
	}
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	state := caseStateName(req.Msg.GetState())
	rows, err := s.pool.Query(ctx, `SELECT `+investigationCaseSelectColumns+`
		FROM investigation_cases
		WHERE asset_id = ANY($1) AND ($2='' OR module_id=$2) AND ($3='' OR state=$3)
		ORDER BY priority DESC, updated_at DESC LIMIT $4 OFFSET $5`, assetIDs, strings.TrimSpace(req.Msg.GetModuleId()), state, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &casev1.ListCasesResponse{}
	for rows.Next() {
		item, err := scanInvestigationCase(rows)
		if err != nil {
			return nil, err
		}
		resp.Cases = append(resp.Cases, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Cases) > limit {
		resp.Cases = resp.Cases[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// GetCase 返回当前授权范围内的单个案件冻结摘要。
func (s *CaseServer) GetCase(ctx context.Context, req *connect.Request[casev1.GetCaseRequest]) (*connect.Response[casev1.GetCaseResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	item, err := scanInvestigationCase(s.pool.QueryRow(ctx, `SELECT `+investigationCaseSelectColumns+`
		FROM investigation_cases WHERE case_id=$1`, req.Msg.GetCaseId()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("case not found"))
	}
	if err != nil {
		return nil, err
	}
	if err := authorizeCaseRead(ctx, s.pool, user, item.GetAssetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&casev1.GetCaseResponse{Case: item}), nil
}

// PollCaseActivities 长轮询案件活动，消息只返回引用和摘要。
func (s *CaseServer) PollCaseActivities(ctx context.Context, req *connect.Request[casev1.PollCaseActivitiesRequest]) (*connect.Response[casev1.PollCaseActivitiesResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	var assetID string
	if err := s.pool.QueryRow(ctx, `SELECT asset_id FROM investigation_cases WHERE case_id=$1`, req.Msg.GetCaseId()).Scan(&assetID); errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("case not found"))
	} else if err != nil {
		return nil, err
	}
	if err := authorizeCaseRead(ctx, s.pool, user, assetID); err != nil {
		return nil, err
	}
	wait := time.Duration(req.Msg.GetLongPollSeconds()) * time.Second
	if wait < 0 {
		wait = 0
	}
	if wait > pollMaxWait {
		wait = pollMaxWait
	}
	deadline := time.Now().Add(wait)
	for {
		resp, err := s.caseActivities(ctx, req.Msg.GetCaseId(), req.Msg.GetAfterSequence())
		if err != nil || len(resp.GetActivities()) > 0 || !time.Now().Before(deadline) {
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(resp), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollTick):
		}
	}
}

func (s *CaseServer) caseActivities(ctx context.Context, caseID string, after int64) (*casev1.PollCaseActivitiesResponse, error) {
	rows, err := s.pool.Query(ctx, `SELECT sequence, case_id, kind, ref_id, summary, occurred_at
		FROM case_activities WHERE case_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 100`, caseID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &casev1.PollCaseActivitiesResponse{NextAfterSequence: after}
	for rows.Next() {
		var item casev1.CaseActivity
		var kind string
		var at time.Time
		if err := rows.Scan(&item.Sequence, &item.CaseId, &kind, &item.RefId, &item.Summary, &at); err != nil {
			return nil, err
		}
		item.Kind = caseActivityKind(kind)
		item.OccurredAt = timestamppb.New(at)
		resp.Activities = append(resp.Activities, &item)
		resp.NextAfterSequence = item.Sequence
	}
	return resp, rows.Err()
}

func authorizeCaseRead(ctx context.Context, pool *pgxpool.Pool, user interface{ GetUserId() string }, assetID string) error {
	return requireUserGrant(ctx, pool, user.GetUserId(), "case.read", "asset", assetID)
}

func scanInvestigationCase(row pgx.Row) (*casev1.InvestigationCase, error) {
	var item casev1.InvestigationCase
	var state string
	var representativesRaw, findingRaw []byte
	var created, updated time.Time
	var resolution string
	var resolvedAt *time.Time
	if err := row.Scan(&item.CaseId, &item.ModuleId, &item.AssetId, &item.ClusterId, &state, &item.Priority,
		&item.Title, &item.Summary, &representativesRaw, &findingRaw, &item.ShadowReleaseId, &created, &updated,
		&item.AssignedAgentId, &item.AssignedAgentDisplayName, &resolution, &item.AutomationSuppressedReason,
		&item.AssignedRunId, &item.AssignedAgentConfigDigest, &resolvedAt); err != nil {
		return nil, err
	}
	item.State = caseState(state)
	item.CreatedAt = timestamppb.New(created)
	item.UpdatedAt = timestamppb.New(updated)
	item.Resolution = caseResolution(resolution)
	if resolvedAt != nil {
		item.ResolvedAt = timestamppb.New(*resolvedAt)
	}
	var representatives []json.RawMessage
	if len(representativesRaw) > 0 {
		if err := json.Unmarshal(representativesRaw, &representatives); err != nil {
			return nil, err
		}
	}
	for _, raw := range representatives {
		candidate := &telemetryv1.ReviewCandidate{}
		if err := protojson.Unmarshal(raw, candidate); err != nil {
			return nil, err
		}
		item.Representatives = append(item.Representatives, candidate)
	}
	if len(findingRaw) > 0 && string(findingRaw) != "{}" {
		item.Finding = &modelv1.TrafficFinding{}
		if err := protojson.Unmarshal(findingRaw, item.Finding); err != nil {
			return nil, err
		}
	}
	return &item, nil
}

func caseResolution(raw string) casev1.CaseResolution {
	switch raw {
	case "confirmed_malicious":
		return casev1.CaseResolution_CASE_RESOLUTION_CONFIRMED_MALICIOUS
	case "false_positive":
		return casev1.CaseResolution_CASE_RESOLUTION_FALSE_POSITIVE
	case "benign":
		return casev1.CaseResolution_CASE_RESOLUTION_BENIGN
	case "insufficient_evidence":
		return casev1.CaseResolution_CASE_RESOLUTION_INSUFFICIENT_EVIDENCE
	case "evidence_denied":
		return casev1.CaseResolution_CASE_RESOLUTION_EVIDENCE_DENIED
	case "shadow_published":
		return casev1.CaseResolution_CASE_RESOLUTION_SHADOW_PUBLISHED
	case "failed":
		return casev1.CaseResolution_CASE_RESOLUTION_FAILED
	default:
		return casev1.CaseResolution_CASE_RESOLUTION_UNSPECIFIED
	}
}

func caseResolutionName(value casev1.CaseResolution) string {
	switch value {
	case casev1.CaseResolution_CASE_RESOLUTION_CONFIRMED_MALICIOUS:
		return "confirmed_malicious"
	case casev1.CaseResolution_CASE_RESOLUTION_FALSE_POSITIVE:
		return "false_positive"
	case casev1.CaseResolution_CASE_RESOLUTION_BENIGN:
		return "benign"
	case casev1.CaseResolution_CASE_RESOLUTION_INSUFFICIENT_EVIDENCE:
		return "insufficient_evidence"
	case casev1.CaseResolution_CASE_RESOLUTION_EVIDENCE_DENIED:
		return "evidence_denied"
	case casev1.CaseResolution_CASE_RESOLUTION_SHADOW_PUBLISHED:
		return "shadow_published"
	case casev1.CaseResolution_CASE_RESOLUTION_FAILED:
		return "failed"
	default:
		return ""
	}
}

func caseState(raw string) casev1.InvestigationCaseState {
	switch raw {
	case "open":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_OPEN
	case "waiting_evidence_approval":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL
	case "queued":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_QUEUED
	case "investigating":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_INVESTIGATING
	case "finding_ready":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_FINDING_READY
	case "shadow_observing":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_SHADOW_OBSERVING
	case "resolved":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_RESOLVED
	case "failed":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_FAILED
	case "evidence_expired":
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED
	default:
		return casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_UNSPECIFIED
	}
}

func caseStateName(state casev1.InvestigationCaseState) string {
	switch state {
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_OPEN:
		return "open"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL:
		return "waiting_evidence_approval"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_QUEUED:
		return "queued"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_INVESTIGATING:
		return "investigating"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_FINDING_READY:
		return "finding_ready"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_SHADOW_OBSERVING:
		return "shadow_observing"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_RESOLVED:
		return "resolved"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_FAILED:
		return "failed"
	case casev1.InvestigationCaseState_INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED:
		return "evidence_expired"
	default:
		return ""
	}
}

func caseActivityKind(raw string) casev1.CaseActivityKind {
	switch raw {
	case "created":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_CREATED
	case "evidence_requested":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_EVIDENCE_REQUESTED
	case "approval_decided":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_APPROVAL_DECIDED
	case "approval_requested":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_APPROVAL_REQUESTED
	case "run_progress":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_RUN_PROGRESS
	case "finding":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_FINDING
	case "shadow_candidate":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_SHADOW_CANDIDATE
	case "recommendation":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_RECOMMENDATION
	case "state_changed":
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_STATE_CHANGED
	default:
		return casev1.CaseActivityKind_CASE_ACTIVITY_KIND_UNSPECIFIED
	}
}
