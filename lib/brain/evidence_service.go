package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	evidencev1 "yufeng/proto/gen/evidencev1"
	"yufeng/proto/gen/evidencev1/evidencev1connect"
)

// EvidenceServer 只接受 Edge 主动领取和提交已批准的精确证据片段。
type EvidenceServer struct {
	pool     *pgxpool.Pool
	relay    *SensitiveRelay
	agents   *AgentServer
	jarvisID string
}

// NewEvidenceServer 构造证据请求服务。
func NewEvidenceServer(pool *pgxpool.Pool, relay *SensitiveRelay, agents *AgentServer, jarvisID string) *EvidenceServer {
	return &EvidenceServer{pool: pool, relay: relay, agents: agents, jarvisID: strings.TrimSpace(jarvisID)}
}

// ResetSensitiveEvidenceRequests 在 Brain 重启后让尚未消费的批准重新由 Edge 提交。
// 丢失内存中继的旧 run 必须先终止，避免旧敏感引用在租约重领时永久阻塞案件。
func ResetSensitiveEvidenceRequests(ctx context.Context, pool *pgxpool.Pool) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.request_id, r.case_id
			FROM evidence_requests r JOIN evidence_approvals a USING(approval_id)
			WHERE r.state='submitted' AND a.state='approved' AND r.expires_at>now()
			FOR UPDATE OF r`)
		if err != nil {
			return err
		}
		var requests []struct{ requestID, caseID string }
		for rows.Next() {
			var item struct{ requestID, caseID string }
			if err := rows.Scan(&item.requestID, &item.caseID); err != nil {
				rows.Close()
				return err
			}
			requests = append(requests, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range requests {
			runRows, err := tx.Query(ctx, `SELECT r.run_id, COALESCE(b.budget_id,'')
				FROM runs r JOIN work_items w USING(run_id)
				LEFT JOIN run_budget_accounts b ON b.run_id=r.run_id
				WHERE w.investigation_case_id=$1 AND r.state IN ('pending','running') FOR UPDATE OF r`, item.caseID)
			if err != nil {
				return err
			}
			var runIDs, budgetIDs []string
			for runRows.Next() {
				var runID, budgetID string
				if err := runRows.Scan(&runID, &budgetID); err != nil {
					runRows.Close()
					return err
				}
				runIDs = append(runIDs, runID)
				budgetIDs = append(budgetIDs, budgetID)
			}
			if err := runRows.Err(); err != nil {
				runRows.Close()
				return err
			}
			runRows.Close()
			for _, budgetID := range budgetIDs {
				if err := closeRunBudget(ctx, tx, budgetID, "failed", false, time.Now()); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE capability_budget SET revoked=true WHERE budget_id=$1`, budgetID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE capability_token_instances SET revoked=true WHERE budget_id=$1`, budgetID); err != nil {
					return err
				}
			}
			if len(runIDs) > 0 {
				if _, err := tx.Exec(ctx, `UPDATE work_items SET status='failed', lease_id='', lease_deadline=NULL,
					capability_token='', updated_at=now() WHERE run_id=ANY($1::text[]) AND status IN ('pending','leased')`, runIDs); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE runs SET state='failed', error=$2, updated_at=now()
					WHERE run_id=ANY($1::text[]) AND state IN ('pending','running')`, runIDs,
					auditPayloadDigest("sensitive relay lost before model effect")); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE evidence_requests SET state='pending', sensitive_content_ref='', submitted_at=NULL,
				lease_id='', lease_deadline=NULL, bundle_digest=''
				WHERE request_id=$1`, item.requestID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='waiting_evidence_approval', updated_at=now()
				WHERE case_id=$1 AND state IN ('queued','investigating','failed')`, item.caseID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
				SELECT $1,'run_progress',$2,'Brain 重启后内存证据已失效，旧调查终止并等待 Edge 重新提交已批准证据'
				WHERE NOT EXISTS (SELECT 1 FROM case_activities WHERE case_id=$1 AND ref_id=$2)`,
				item.caseID, "relay-reset:"+item.requestID); err != nil {
				return err
			}
		}
		return nil
	})
}

// Handler 返回 Connect 服务端处理器。
func (s *EvidenceServer) Handler() (string, http.Handler) {
	return evidencev1connect.NewEvidenceServiceHandler(s, handlerOptions()...)
}

// PollEvidenceRequests 为当前单元长轮询未过期批准；Brain 不反向连接 Edge。
func (s *EvidenceServer) PollEvidenceRequests(ctx context.Context, req *connect.Request[evidencev1.PollEvidenceRequestsRequest]) (*connect.Response[evidencev1.PollEvidenceRequestsResponse], error) {
	unitID, err := requireUnitRPC(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetUnitId() != "" && req.Msg.GetUnitId() != unitID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("unit_id does not match authenticated unit"))
	}
	wait := time.Duration(req.Msg.GetLongPollSeconds()) * time.Second
	if wait <= 0 {
		wait = 20 * time.Second
	}
	if wait > pollMaxWait {
		wait = pollMaxWait
	}
	deadline := time.Now().Add(wait)
	for {
		out, err := s.pollEvidence(ctx, unitID)
		if err != nil || len(out.GetRequests()) > 0 || !time.Now().Before(deadline) {
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(out), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollTick):
		}
	}
}

func (s *EvidenceServer) pollEvidence(ctx context.Context, unitID string) (*evidencev1.PollEvidenceRequestsResponse, error) {
	resp := &evidencev1.PollEvidenceRequestsResponse{}
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT request_id, approval_id, case_id, asset_id, unit_id,
			evidence_handles, allowed_fields, max_bytes, model_config_digest, expires_at, lease_epoch, bundle_group_id
			FROM evidence_requests WHERE unit_id=$1 AND expires_at>now()
			AND (state='pending' OR (state='leased' AND lease_deadline<=now()))
			ORDER BY expires_at LIMIT 4 FOR UPDATE SKIP LOCKED`, unitID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item evidencev1.EvidenceRequest
			var handlesRaw, fieldsRaw []byte
			var expires time.Time
			if err := rows.Scan(&item.RequestId, &item.ApprovalId, &item.CaseId, &item.AssetId, &item.UnitId,
				&handlesRaw, &fieldsRaw, &item.MaxBytes, &item.ModelConfigDigest, &expires, &item.LeaseEpoch, &item.BundleGroupId); err != nil {
				return err
			}
			if err := json.Unmarshal(handlesRaw, &item.EvidenceHandles); err != nil {
				return err
			}
			if err := json.Unmarshal(fieldsRaw, &item.AllowedFields); err != nil {
				return err
			}
			item.ExpiresAt = timestamppb.New(expires)
			resp.Requests = append(resp.Requests, &item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range resp.Requests {
			item.LeaseEpoch++
			item.LeaseId, err = newID("evidence-lease")
			if err != nil {
				return err
			}
			expires := item.GetExpiresAt().AsTime()
			leaseDeadline := time.Now().Add(time.Minute)
			if expires.Before(leaseDeadline) {
				leaseDeadline = expires
			}
			item.LeaseDeadline = timestamppb.New(leaseDeadline)
			if _, err := tx.Exec(ctx, `UPDATE evidence_requests SET state='leased', lease_id=$2,
				lease_epoch=$3, lease_deadline=$4 WHERE request_id=$1`, item.GetRequestId(), item.GetLeaseId(), item.GetLeaseEpoch(), leaseDeadline); err != nil {
				return err
			}
		}
		return nil
	})
	return resp, err
}

// SubmitEvidenceBundle 校验字段、句柄、摘要与总字节上限，只把正文放入短期内存中继。
func (s *EvidenceServer) SubmitEvidenceBundle(ctx context.Context, req *connect.Request[evidencev1.SubmitEvidenceBundleRequest]) (*connect.Response[evidencev1.SubmitEvidenceBundleResponse], error) {
	unitID, err := requireUnitRPC(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	var boundUnit, approvalID, caseID, state, leaseID, storedBundleDigest, storedRef string
	var handlesRaw, fieldsRaw []byte
	var maxBytes, leaseEpoch int64
	var expires time.Time
	var leaseDeadline *time.Time
	err = s.pool.QueryRow(ctx, `SELECT unit_id, approval_id, case_id, state, evidence_handles, allowed_fields, max_bytes, expires_at,
		lease_id, lease_epoch, lease_deadline, bundle_digest, sensitive_content_ref
		FROM evidence_requests WHERE request_id=$1`, req.Msg.GetRequestId()).Scan(&boundUnit, &approvalID, &caseID, &state,
		&handlesRaw, &fieldsRaw, &maxBytes, &expires, &leaseID, &leaseEpoch, &leaseDeadline, &storedBundleDigest, &storedRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("evidence request not found"))
	}
	if err != nil {
		return nil, err
	}
	if boundUnit != unitID || approvalID != req.Msg.GetApprovalId() || caseID != req.Msg.GetCaseId() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("evidence request binding mismatch"))
	}
	bundleDigest := sensitiveEntryDigest(req.Msg.GetFragments())
	if !strings.EqualFold(req.Msg.GetBundleDigest(), bundleDigest) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("evidence bundle digest mismatch"))
	}
	if state == "submitted" {
		if !strings.EqualFold(storedBundleDigest, bundleDigest) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("evidence request was submitted with different content"))
		}
		return connect.NewResponse(&evidencev1.SubmitEvidenceBundleResponse{
			SensitiveContentRef: storedRef, ExpiresAt: timestamppb.New(expires), ContentDigest: storedBundleDigest,
		}), nil
	}
	if !expires.After(time.Now()) || state != "leased" || leaseDeadline == nil || !leaseDeadline.After(time.Now()) ||
		leaseID == "" || req.Msg.GetLeaseId() != leaseID || req.Msg.GetLeaseEpoch() != leaseEpoch {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("evidence request lease is invalid or expired"))
	}
	var handles, fields []string
	if err := json.Unmarshal(handlesRaw, &handles); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fieldsRaw, &fields); err != nil {
		return nil, err
	}
	allowedHandles := stringSet(handles)
	allowedFields := stringSet(fields)
	seenFragments := make(map[string]bool, len(req.Msg.GetFragments()))
	coveredHandles := make(map[string]bool, len(handles))
	var total int64
	for _, fragment := range req.Msg.GetFragments() {
		if fragment == nil || !allowedHandles[fragment.GetEvidenceHandle()] || !allowedFields[fragment.GetField()] {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("evidence fragment exceeds approved fields or handles"))
		}
		fragmentKey := fragment.GetEvidenceHandle() + "\x00" + fragment.GetField()
		if seenFragments[fragmentKey] {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("evidence fragment is duplicated"))
		}
		seenFragments[fragmentKey] = true
		coveredHandles[fragment.GetEvidenceHandle()] = true
		total += int64(len(fragment.GetContent()))
		sum := sha256.Sum256(fragment.GetContent())
		want := "sha256:" + hex.EncodeToString(sum[:])
		if !strings.EqualFold(fragment.GetContentDigest(), want) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("evidence fragment digest mismatch"))
		}
	}
	for _, handle := range handles {
		if !coveredHandles[handle] {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("evidence bundle does not cover every approved representative"))
		}
	}
	if total == 0 || total > maxBytes {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("evidence bundle exceeds approved byte limit"))
	}
	ref, err := newID("sensitive")
	if err != nil {
		return nil, err
	}
	if err := s.relay.put(ref, sensitiveRelayEntry{approvalID: approvalID, caseID: caseID, fragments: req.Msg.GetFragments(), bytes: total, expiresAt: expires}); err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}
	finalRef := ref
	var groupRef string
	var submittedRefs []string
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), 12)`, approvalID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE evidence_requests SET state='submitted', submitted_at=now(), sensitive_content_ref=$2,
			bundle_digest=$3 WHERE request_id=$1 AND state='leased' AND lease_id=$4 AND lease_epoch=$5
			AND lease_deadline>now() AND expires_at>now()`, req.Msg.GetRequestId(), ref, bundleDigest, leaseID, leaseEpoch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeAborted, errors.New("evidence request was consumed concurrently"))
		}
		var requestCount, submittedCount int
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE state='submitted')
			FROM evidence_requests WHERE approval_id=$1`, approvalID).Scan(&requestCount, &submittedCount); err != nil {
			return err
		}
		if requestCount == 0 || submittedCount != requestCount {
			_, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
				VALUES($1,'state_changed',$2,'一个 Edge 已提交批准证据，等待同一案件的其他 Edge')`, caseID, req.Msg.GetRequestId())
			return err
		}
		rows, err := tx.Query(ctx, `SELECT sensitive_content_ref FROM evidence_requests
			WHERE approval_id=$1 AND state='submitted' ORDER BY unit_id FOR UPDATE`, approvalID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var submittedRef string
			if err := rows.Scan(&submittedRef); err != nil {
				rows.Close()
				return err
			}
			submittedRefs = append(submittedRefs, submittedRef)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		var combined sensitiveRelayEntry
		combined.approvalID, combined.caseID, combined.expiresAt = approvalID, caseID, expires
		for _, submittedRef := range submittedRefs {
			entry, ok := s.relay.get(submittedRef)
			if !ok || entry.approvalID != approvalID || entry.caseID != caseID {
				return errors.New("submitted evidence relay entry is unavailable")
			}
			combined.fragments = append(combined.fragments, entry.fragments...)
			combined.bytes += entry.bytes
			if entry.expiresAt.Before(combined.expiresAt) {
				combined.expiresAt = entry.expiresAt
			}
		}
		groupRef, err = newID("sensitive-group")
		if err != nil {
			return err
		}
		if err := s.relay.put(groupRef, combined); err != nil {
			return err
		}
		finalRef = groupRef
		if _, err := tx.Exec(ctx, `UPDATE evidence_requests SET sensitive_content_ref=$2
			WHERE approval_id=$1 AND state='submitted'`, approvalID, groupRef); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='queued', updated_at=now() WHERE case_id=$1`, caseID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET sensitive_content_ref=$2, updated_at=now()
			WHERE investigation_case_id=$1 AND status='pending'`, caseID, groupRef); err != nil {
			return err
		}
		if s.agents == nil || s.jarvisID == "" {
			return errors.New("case review orchestrator is unavailable")
		}
		if _, err := enqueueCaseReviewInstruction(ctx, tx, s.agents, s.jarvisID, caseID, "evidence-submitted:"+groupRef); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			VALUES($1,'state_changed',$2,'全部 Edge 的批准证据已到达内存中继，案件进入调查队列')`, caseID, groupRef)
		return err
	})
	if err != nil {
		_, _ = s.relay.consume(ref)
		if groupRef != "" {
			_, _ = s.relay.consume(groupRef)
		}
		return nil, err
	}
	if groupRef != "" {
		for _, submittedRef := range submittedRefs {
			_, _ = s.relay.consume(submittedRef)
		}
	}
	return connect.NewResponse(&evidencev1.SubmitEvidenceBundleResponse{
		SensitiveContentRef: finalRef, ExpiresAt: timestamppb.New(expires), ContentDigest: bundleDigest,
	}), nil
}
