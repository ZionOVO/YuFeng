package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "yufeng/proto/gen/agentv1"
	"yufeng/proto/gen/agentv1/agentv1connect"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

// AgentInteractionServer 统一处理证据与中央执行池容量审批。
type AgentInteractionServer struct{ pool *pgxpool.Pool }

// NewAgentInteractionServer 构造审批服务。
func NewAgentInteractionServer(pool *pgxpool.Pool) *AgentInteractionServer {
	return &AgentInteractionServer{pool: pool}
}

// Handler 返回 Connect 服务端处理器。
func (s *AgentInteractionServer) Handler() (string, http.Handler) {
	return agentv1connect.NewAgentInteractionServiceHandler(s, handlerOptions()...)
}

// GetApproval 返回当前用户有权决定的冻结审批投影，不返回证据正文或模型密钥。
func (s *AgentInteractionServer) GetApproval(ctx context.Context, req *connect.Request[agentv1.GetApprovalRequest]) (*connect.Response[agentv1.GetApprovalResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	approvalID := strings.TrimSpace(req.Msg.GetApprovalId())
	if approvalID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("approval_id is required"))
	}
	view := &agentv1.ApprovalView{ApprovalId: approvalID}
	var fieldsRaw []byte
	var expires, created time.Time
	var baseURL string
	err = s.pool.QueryRow(ctx, `SELECT ea.case_id, ea.asset_id, ea.state, ea.allowed_fields, ea.max_bytes,
		ea.model_config_digest, ea.expires_at, ea.created_at, d.base_url, d.model
		FROM evidence_approvals ea CROSS JOIN deployment_onboarding d
		WHERE ea.approval_id=$1 AND d.id=1`, approvalID).
		Scan(&view.CaseId, &view.AssetId, &view.State, &fieldsRaw, &view.MaxBytes, &view.ModelConfigDigest,
			&expires, &created, &baseURL, &view.ModelName)
	if err == nil {
		if err := requireUserGrant(ctx, s.pool, user.GetUserId(), "case.read", "asset", view.AssetId); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(fieldsRaw, &view.AllowedFields); err != nil {
			return nil, err
		}
		view.Kind = agentv1.ApprovalKind_APPROVAL_KIND_EVIDENCE
		view.ModelHost = modelApprovalHost(baseURL)
		view.ExpiresAt, view.CreatedAt = timestamppb.New(expires), timestamppb.New(created)
		if view.State == "pending" && !expires.After(time.Now()) {
			view.State = "expired"
		}
		return connect.NewResponse(&agentv1.GetApprovalResponse{Approval: view}), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = s.pool.QueryRow(ctx, `SELECT c.case_id, i.asset_id, c.worker_id, c.state, c.previous_capacity,
		c.requested_capacity, c.expires_at, c.created_at FROM worker_capacity_changes c
		JOIN investigation_cases i ON i.case_id=c.case_id WHERE c.change_id=$1`, approvalID).
		Scan(&view.CaseId, &view.AssetId, &view.WorkerId, &view.State, &view.PreviousCapacity, &view.RequestedCapacity, &expires, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("approval not found"))
	}
	if err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, s.pool, user, "worker.capacity.approve", "asset", view.AssetId, false); err != nil {
		return nil, err
	}
	view.Kind = agentv1.ApprovalKind_APPROVAL_KIND_WORKER_CAPACITY
	view.ExpiresAt, view.CreatedAt = timestamppb.New(expires), timestamppb.New(created)
	if view.State == "pending" && !expires.After(time.Now()) {
		view.State = "expired"
	}
	return connect.NewResponse(&agentv1.GetApprovalResponse{Approval: view}), nil
}

func modelApprovalHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// DecideApproval 原子决定一种待审批对象；重复决定不会再次消费授权。
func (s *AgentInteractionServer) DecideApproval(ctx context.Context, req *connect.Request[agentv1.DecideApprovalRequest]) (*connect.Response[agentv1.DecideApprovalResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	approvalID := strings.TrimSpace(req.Msg.GetApprovalId())
	if approvalID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("approval_id is required"))
	}
	reason := strings.TrimSpace(req.Msg.GetReason())
	if len([]rune(reason)) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("approval reason exceeds 500 characters"))
	}
	var assetID string
	err = s.pool.QueryRow(ctx, `SELECT asset_id FROM evidence_approvals WHERE approval_id=$1`, approvalID).Scan(&assetID)
	if err == nil {
		if err := authorizeWrite(ctx, s.pool, user, "evidence.approve", "asset", assetID, false); err != nil {
			return nil, err
		}
		state, err := s.decideEvidenceWithReason(ctx, approvalID, user.GetUserId(), req.Msg.GetApproved(), reason)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&agentv1.DecideApprovalResponse{ApprovalId: approvalID, State: state}), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = s.pool.QueryRow(ctx, `SELECT i.asset_id FROM worker_capacity_changes c
		JOIN investigation_cases i ON i.case_id=c.case_id WHERE c.change_id=$1`, approvalID).Scan(&assetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("approval not found"))
	}
	if err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, s.pool, user, "worker.capacity.approve", "asset", assetID, false); err != nil {
		return nil, err
	}
	state, err := s.decideCapacityWithReason(ctx, approvalID, user.GetUserId(), req.Msg.GetApproved(), reason)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.DecideApprovalResponse{ApprovalId: approvalID, State: state}), nil
}

func (s *AgentInteractionServer) decideEvidence(ctx context.Context, approvalID, userID string, approved bool) (string, error) {
	return s.decideEvidenceWithReason(ctx, approvalID, userID, approved, "")
}

func (s *AgentInteractionServer) decideEvidenceWithReason(ctx context.Context, approvalID, userID string, approved bool, reason string) (string, error) {
	state := "denied"
	if approved {
		state = "approved"
	}
	modelConfigurationChanged := false
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var caseID, assetID, unitID, current, modelDigest string
		var handlesRaw, fieldsRaw []byte
		var maxBytes int64
		var expires time.Time
		if err := tx.QueryRow(ctx, `SELECT case_id, asset_id, unit_id, evidence_handles, allowed_fields,
			max_bytes, model_config_digest, state, expires_at FROM evidence_approvals WHERE approval_id=$1 FOR UPDATE`, approvalID).
			Scan(&caseID, &assetID, &unitID, &handlesRaw, &fieldsRaw, &maxBytes, &modelDigest, &current, &expires); err != nil {
			return err
		}
		if current != "pending" || !expires.After(time.Now()) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("approval is expired or already decided"))
		}
		if approved {
			actual, err := currentModelConfigDigest(ctx, tx)
			if err != nil {
				return err
			}
			if actual != modelDigest {
				modelConfigurationChanged = true
				if _, err := expireSensitiveApproval(ctx, tx, approvalID, caseID, "模型配置变化，待批准证据请求已失效"); err != nil {
					return err
				}
				return appendAuditTx(ctx, tx, "user", userID, "evidence_approval.expire", "evidence_approval", approvalID,
					map[string]any{"case_id": caseID, "asset_id": assetID, "reason": "model_configuration_changed"})
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE evidence_approvals SET state=$2, decided_by=$3, decided_at=now() WHERE approval_id=$1`, approvalID, state, userID); err != nil {
			return err
		}
		if approved {
			var handles []string
			if err := json.Unmarshal(handlesRaw, &handles); err != nil || len(handles) == 0 {
				return errors.New("approved evidence handles are invalid")
			}
			rows, err := tx.Query(ctx, `SELECT evidence_handle, unit_id FROM traffic.review_candidates
				WHERE asset_id=$1 AND evidence_handle=ANY($2::text[]) AND evidence_expires_at>$3
				ORDER BY unit_id, evidence_handle`, assetID, handles, time.Now())
			if err != nil {
				return err
			}
			byUnit := make(map[string][]string)
			found := make(map[string]bool, len(handles))
			for rows.Next() {
				var handle, candidateUnit string
				if err := rows.Scan(&handle, &candidateUnit); err != nil {
					rows.Close()
					return err
				}
				if candidateUnit == "" || found[handle] {
					rows.Close()
					return errors.New("approved evidence binding is ambiguous")
				}
				found[handle] = true
				byUnit[candidateUnit] = append(byUnit[candidateUnit], handle)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			if len(found) != len(handles) {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("approved evidence set changed or expired"))
			}
			for candidateUnit, unitHandles := range byUnit {
				requestID, err := newID("evidence")
				if err != nil {
					return err
				}
				unitHandlesRaw, err := json.Marshal(unitHandles)
				if err != nil {
					return err
				}
				unitMaxBytes := maxBytes * int64(len(unitHandles)) / int64(len(handles))
				if unitMaxBytes < int64(len(unitHandles)) {
					return errors.New("approved evidence byte budget is too small")
				}
				if _, err := tx.Exec(ctx, `INSERT INTO evidence_requests(request_id, approval_id, case_id, asset_id, unit_id,
					evidence_handles, allowed_fields, max_bytes, model_config_digest, expires_at, bundle_group_id)
					VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8,$9,$10,$11)`, requestID, approvalID, caseID, assetID,
					candidateUnit, unitHandlesRaw, fieldsRaw, unitMaxBytes, modelDigest, expires, approvalID); err != nil {
					return err
				}
			}
		}
		caseState := "waiting_evidence_approval"
		resolution := ""
		var resolvedAt any
		if !approved {
			caseState = "resolved"
			resolution = "evidence_denied"
			resolvedAt = time.Now()
		}
		if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET state=$2, resolution=$3, resolved_at=$4, updated_at=now()
			WHERE case_id=$1`, caseID, caseState, resolution, resolvedAt); err != nil {
			return err
		}
		summary := "证据访问已拒绝"
		if approved {
			summary = "证据访问已批准"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			VALUES($1,'approval_decided',$2,$3)`, caseID, approvalID, summary); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "user", userID, "evidence_approval.decide", "evidence_approval", approvalID,
			map[string]any{"case_id": caseID, "asset_id": assetID, "state": state, "reason": reason})
	})
	if err == nil && modelConfigurationChanged {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("model configuration changed after evidence request"))
	}
	return state, err
}

func expireSensitiveApproval(ctx context.Context, db dbTX, approvalID, caseID, summary string) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE evidence_approvals SET state='expired'
		WHERE approval_id=$1 AND case_id=$2 AND state IN ('pending','approved')`, approvalID, caseID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := db.Exec(ctx, `UPDATE evidence_requests SET state='expired'
		WHERE approval_id=$1 AND state IN ('pending','leased','submitted')`, approvalID); err != nil {
		return false, err
	}
	if _, err := db.Exec(ctx, `UPDATE investigation_cases SET state='evidence_expired', updated_at=now()
		WHERE case_id=$1 AND state NOT IN ('resolved','failed')`, caseID); err != nil {
		return false, err
	}
	if _, err := db.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
		VALUES($1,'state_changed',$2,$3)`, caseID, approvalID, summary); err != nil {
		return false, err
	}
	return true, nil
}

func (s *AgentInteractionServer) decideCapacityWithReason(ctx context.Context, changeID, userID string, approved bool, reason string) (string, error) {
	state := "denied"
	if approved {
		state = "approved"
	}
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var workerID, caseID, current string
		var requested int
		var expires time.Time
		if err := tx.QueryRow(ctx, `SELECT worker_id, case_id, requested_capacity, state, expires_at
			FROM worker_capacity_changes WHERE change_id=$1 FOR UPDATE`, changeID).Scan(&workerID, &caseID, &requested, &current, &expires); err != nil {
			return err
		}
		if current != "pending" || !expires.After(time.Now()) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("capacity approval is expired or already decided"))
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_capacity_changes SET state=$2, decided_by=$3, decided_at=now() WHERE change_id=$1`, changeID, state, userID); err != nil {
			return err
		}
		if approved {
			if _, err := tx.Exec(ctx, `UPDATE workers SET max_concurrency=$2, updated_at=now() WHERE worker_id=$1`, workerID, requested); err != nil {
				return err
			}
		}
		summary := "中央调查执行池扩容已拒绝"
		if approved {
			summary = "中央调查执行池扩容已批准，二十四小时后自动恢复"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			VALUES($1,'approval_decided',$2,$3)`, caseID, changeID, summary); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "user", userID, "worker_capacity.decide", "worker_capacity_change", changeID,
			map[string]any{"case_id": caseID, "worker_id": workerID, "requested_capacity": requested, "state": state, "reason": reason})
	})
	return state, err
}

func currentModelConfigDigest(ctx context.Context, db dbTX) (string, error) {
	var baseURL, model, dialect string
	if err := db.QueryRow(ctx, `SELECT base_url, model, dialect FROM deployment_onboarding WHERE id=1`).Scan(&baseURL, &model, &dialect); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{baseURL, model, dialect}, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func requestCaseEvidence(ctx context.Context, db dbTX, caseID, requestedBy string) (string, error) {
	var consumed bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM evidence_approvals WHERE case_id=$1 AND state='consumed')`, caseID).Scan(&consumed); err != nil {
		return "", err
	}
	if consumed {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("case sensitive model call was already consumed"))
	}
	var existing string
	err := db.QueryRow(ctx, `SELECT approval_id FROM evidence_approvals
		WHERE case_id=$1 AND state IN ('pending','approved') AND expires_at>now() ORDER BY created_at DESC LIMIT 1`, caseID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var assetID string
	var representativesRaw []byte
	if err := db.QueryRow(ctx, `SELECT asset_id, representatives FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&assetID, &representativesRaw); err != nil {
		return "", err
	}
	var representatives []map[string]any
	if err := json.Unmarshal(representativesRaw, &representatives); err != nil {
		return "", err
	}
	handles := make([]string, 0, len(representatives))
	units := make(map[string]bool)
	expires := time.Now().Add(15 * time.Minute)
	for _, representative := range representatives {
		mode, _ := representative["review_mode"].(string)
		if mode != artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL.String() &&
			mode != artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES.String() {
			continue
		}
		handle, _ := representative["evidence_handle"].(string)
		unitID, _ := representative["unit_id"].(string)
		expiresRaw, _ := representative["evidence_expires_at"].(string)
		if handle == "" || unitID == "" || expiresRaw == "" {
			continue
		}
		representativeExpires, err := time.Parse(time.RFC3339Nano, expiresRaw)
		if err != nil || !representativeExpires.After(time.Now()) {
			continue
		}
		if representativeExpires.Before(expires) {
			expires = representativeExpires
		}
		handles = append(handles, handle)
		units[unitID] = true
	}
	if len(handles) == 0 {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("case has no live evidence handles"))
	}
	unitID := ""
	if len(units) == 1 {
		for value := range units {
			unitID = value
		}
	}
	modelDigest, err := currentModelConfigDigest(ctx, db)
	if err != nil {
		return "", err
	}
	approvalID, err := newID("approval")
	if err != nil {
		return "", err
	}
	handlesRaw, _ := json.Marshal(handles)
	fieldsRaw, _ := json.Marshal([]string{"method", "path", "query", "body"})
	if _, err := db.Exec(ctx, `INSERT INTO evidence_approvals(approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, requested_by, expires_at)
		VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb,40960,$7,$8,$9)`, approvalID, caseID, assetID, unitID,
		handlesRaw, fieldsRaw, modelDigest, requestedBy, expires); err != nil {
		return "", err
	}
	if _, err := db.Exec(ctx, `UPDATE investigation_cases SET state='waiting_evidence_approval', updated_at=now() WHERE case_id=$1`, caseID); err != nil {
		return "", err
	}
	if _, err := db.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
		VALUES($1,'evidence_requested',$2,'Jarvis 请求一次性案件证据审批')`, caseID, approvalID); err != nil {
		return "", err
	}
	if err := notifyCaseSessions(ctx, db, requestedBy, assetID,
		"SESSION_ATTACHMENT_KIND_APPROVAL", approvalID, "案件等待一次性证据批准。"); err != nil {
		return "", err
	}
	if err := appendAuditTx(ctx, db, "agent", requestedBy, "evidence_approval.request", "evidence_approval", approvalID,
		map[string]any{"case_id": caseID, "asset_id": assetID, "unit_id": unitID, "handle_count": len(handles), "max_bytes": 40960}); err != nil {
		return "", err
	}
	return approvalID, nil
}
