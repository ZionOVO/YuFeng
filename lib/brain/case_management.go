package brain

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	authv1 "yufeng/proto/gen/authv1"
	casev1 "yufeng/proto/gen/casev1"
)

const caseNoteMaxBytes = 2048

// ResolveCase 以类型化原因解决当前资产范围内的案件。
func (s *CaseServer) ResolveCase(ctx context.Context, req *connect.Request[casev1.ResolveCaseRequest]) (*connect.Response[casev1.ResolveCaseResponse], error) {
	user, assetID, err := s.caseManager(ctx, req, req.Msg.GetCaseId())
	if err != nil {
		return nil, err
	}
	resolution := caseResolutionName(req.Msg.GetResolution())
	if resolution == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("case resolution is required"))
	}
	if len(req.Msg.GetNote()) > caseNoteMaxBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("case note is too large"))
	}
	resp := &casev1.ResolveCaseResponse{}
	err = idempotentProto(ctx, s.pool, "case.resolve:"+user.GetUserId(), idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='resolved',resolution=$2,resolved_at=now(),updated_at=now()
			WHERE case_id=$1 AND state<>'resolved'`, req.Msg.GetCaseId(), resolution)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return persistRPCError(connect.NewError(connect.CodeFailedPrecondition, errors.New("case is already resolved")))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id,kind,ref_id,summary)
			VALUES($1,'state_changed',$2,$3)`, req.Msg.GetCaseId(), user.GetUserId(), firstNonEmpty(strings.TrimSpace(req.Msg.GetNote()), "案件已由操作员解决")); err != nil {
			return err
		}
		if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "case.resolve", "case", req.Msg.GetCaseId(),
			map[string]any{"asset_id": assetID, "resolution": resolution}); err != nil {
			return auditFailedError(err)
		}
		item, err := scanInvestigationCase(tx.QueryRow(ctx, `SELECT `+investigationCaseSelectColumns+` FROM investigation_cases WHERE case_id=$1`, req.Msg.GetCaseId()))
		resp.Case = item
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ReopenCase 清除终态处置并重新进入 Agent 匹配队列。
func (s *CaseServer) ReopenCase(ctx context.Context, req *connect.Request[casev1.ReopenCaseRequest]) (*connect.Response[casev1.ReopenCaseResponse], error) {
	user, assetID, err := s.caseManager(ctx, req, req.Msg.GetCaseId())
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetNote()) > caseNoteMaxBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("case note is too large"))
	}
	resp := &casev1.ReopenCaseResponse{}
	err = idempotentProto(ctx, s.pool, "case.reopen:"+user.GetUserId(), idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='open',resolution='',resolved_at=NULL,
			assigned_agent_id='',assigned_agent_display_name='',assigned_agent_config_digest='',assigned_run_id='',
			agent_profile_snapshot='{}',automation_suppressed_reason='',updated_at=now()
			WHERE case_id=$1 AND state IN ('resolved','failed','evidence_expired')`, req.Msg.GetCaseId())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return persistRPCError(connect.NewError(connect.CodeFailedPrecondition, errors.New("case is not terminal")))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id,kind,ref_id,summary)
			VALUES($1,'state_changed',$2,$3)`, req.Msg.GetCaseId(), user.GetUserId(), firstNonEmpty(strings.TrimSpace(req.Msg.GetNote()), "案件已重新打开")); err != nil {
			return err
		}
		if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "case.reopen", "case", req.Msg.GetCaseId(), map[string]any{"asset_id": assetID}); err != nil {
			return auditFailedError(err)
		}
		item, err := scanInvestigationCase(tx.QueryRow(ctx, `SELECT `+investigationCaseSelectColumns+` FROM investigation_cases WHERE case_id=$1`, req.Msg.GetCaseId()))
		resp.Case = item
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// RecordCaseFeedback 记录真正的人工反馈，供后续案件优先级使用。
func (s *CaseServer) RecordCaseFeedback(ctx context.Context, req *connect.Request[casev1.RecordCaseFeedbackRequest]) (*connect.Response[casev1.RecordCaseFeedbackResponse], error) {
	user, assetID, err := s.caseManager(ctx, req, req.Msg.GetCaseId())
	if err != nil {
		return nil, err
	}
	resolution := caseResolutionName(req.Msg.GetResolution())
	if resolution == "" || len(req.Msg.GetNote()) > caseNoteMaxBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("valid feedback resolution and bounded note are required"))
	}
	resp := &casev1.RecordCaseFeedbackResponse{}
	err = idempotentProto(ctx, s.pool, "case.feedback:"+user.GetUserId(), idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		feedbackID, err := newID("feedback")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO case_feedback(feedback_id,case_id,asset_id,resolution,note,created_by)
			VALUES($1,$2,$3,$4,$5,$6)`, feedbackID, req.Msg.GetCaseId(), assetID, resolution, strings.TrimSpace(req.Msg.GetNote()), user.GetUserId()); err != nil {
			return err
		}
		if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "case.feedback", "case", req.Msg.GetCaseId(),
			map[string]any{"asset_id": assetID, "resolution": resolution}); err != nil {
			return auditFailedError(err)
		}
		item, err := scanInvestigationCase(tx.QueryRow(ctx, `SELECT `+investigationCaseSelectColumns+` FROM investigation_cases WHERE case_id=$1`, req.Msg.GetCaseId()))
		resp.Case = item
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *CaseServer) caseManager(ctx context.Context, req interface{ Header() http.Header }, caseID string) (*authv1.User, string, error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, "", err
	}
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, errors.New("case_id is required"))
	}
	var assetID string
	if err := s.pool.QueryRow(ctx, `SELECT asset_id FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&assetID); errors.Is(err, pgx.ErrNoRows) {
		return nil, "", connect.NewError(connect.CodeNotFound, errors.New("case not found"))
	} else if err != nil {
		return nil, "", err
	}
	if err := requireUserGrant(ctx, s.pool, user.GetUserId(), "case.manage", "asset", assetID); err != nil {
		return nil, "", err
	}
	return user, assetID, nil
}
