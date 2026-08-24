package brain

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
)

func (s *ToolGatewayServer) toolCaseGet(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	caseID := argString(args, "case_id")
	item, err := scanInvestigationCase(s.pool.QueryRow(ctx, `SELECT `+investigationCaseSelectColumns+`
		FROM investigation_cases WHERE case_id=$1`, caseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, deniedObject()
	}
	if err != nil {
		return nil, err
	}
	if !bindingsCoverAssets(claims.Bindings, []string{item.GetAssetId()}) {
		return nil, deniedObject()
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(item)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	var sensitiveRef, approvalID, contentDigest string
	var maxBytes int64
	var expires time.Time
	err = s.pool.QueryRow(ctx, `SELECT r.sensitive_content_ref, a.approval_id, a.max_bytes, a.expires_at
		FROM evidence_approvals a JOIN LATERAL (
			SELECT sensitive_content_ref, max(submitted_at) AS submitted_at
			FROM evidence_requests WHERE approval_id=a.approval_id AND state='submitted'
			GROUP BY sensitive_content_ref
		) r ON true
		WHERE a.case_id=$1 AND a.state='approved' AND a.expires_at>now()
		ORDER BY r.submitted_at DESC LIMIT 1`, caseID).Scan(&sensitiveRef, &approvalID, &maxBytes, &expires)
	if err == nil && sensitiveRef != "" {
		var fragmentsJSON []byte
		if entry, ok := s.sensitiveRelayEntry(sensitiveRef); ok {
			contentDigest = sensitiveEntryDigest(entry.fragments)
			fragmentsJSON, _ = json.Marshal(len(entry.fragments))
		}
		out["sensitive_content_ref"] = map[string]any{
			"ref_id": sensitiveRef, "approval_id": approvalID, "case_id": caseID,
			"content_digest": contentDigest, "max_bytes": maxBytes, "expires_at": expires,
			"fragment_count": json.RawMessage(fragmentsJSON),
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return out, nil
}

func (s *ToolGatewayServer) sensitiveRelayEntry(ref string) (sensitiveRelayEntry, bool) {
	// ToolGateway 与模型网关共享的中继由 NewMux 显式注入。
	if s.sensitiveRelay == nil {
		return sensitiveRelayEntry{}, false
	}
	return s.sensitiveRelay.get(ref)
}

func (s *ToolGatewayServer) toolCaseRequestEvidence(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	caseID := argString(args, "case_id")
	var approvalID string
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var requestErr error
		approvalID, requestErr = requestCaseEvidence(ctx, tx, caseID, claims.Subject)
		return requestErr
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"case_id": caseID, "approval_id": approvalID, "state": "pending", "expires_in_seconds": 900}, nil
}

func (s *ToolGatewayServer) toolCaseRunCreate(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	caseID := argString(args, "case_id")
	sensitiveRef := argString(args, "sensitive_content_ref")
	runID, created, err := createCaseInvestigationRun(ctx, s.pool, caseID, sensitiveRef, claims.Subject)
	if err != nil {
		return nil, err
	}
	return map[string]any{"case_id": caseID, "run_id": runID, "created": created, "state": "queued"}, nil
}

func (s *ToolGatewayServer) toolCaseComplete(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	caseID := argString(args, "case_id")
	state := argString(args, "state")
	if state == "" {
		state = "resolved"
	}
	if state != "resolved" && state != "failed" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("case completion state must be resolved or failed"))
	}
	tag, err := s.pool.Exec(ctx, `UPDATE investigation_cases SET state=$2, resolved_at=CASE WHEN $2='resolved' THEN now() ELSE resolved_at END,
		updated_at=now() WHERE case_id=$1 AND state IN ('finding_ready','shadow_observing','failed')`, caseID, state)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("case is not ready to complete"))
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
		VALUES($1,'state_changed',$2,'Jarvis 完成案件')`, caseID, claims.Subject); err != nil {
		return nil, err
	}
	return map[string]any{"case_id": caseID, "state": state}, nil
}

func (s *ToolGatewayServer) toolWorkerCapacityRequest(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	caseID := argString(args, "case_id")
	workerID := argString(args, "worker_id")
	requestedValue, ok := args["requested_capacity"].(float64)
	if !ok || math.Trunc(requestedValue) != requestedValue {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("requested_capacity must be an integer"))
	}
	requested := int32(requestedValue)
	if requested < 2 || requested > 4 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("requested_capacity must be between 2 and 4"))
	}
	var changeID string
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var assetID string
		if err := tx.QueryRow(ctx, `SELECT asset_id FROM investigation_cases WHERE case_id=$1 FOR SHARE`, caseID).Scan(&assetID); err != nil {
			return err
		}
		var current int32
		var central bool
		if err := tx.QueryRow(ctx, `SELECT w.max_concurrency, EXISTS(SELECT 1 FROM grants g
			WHERE g.subject_kind='worker' AND g.subject_id=w.worker_id AND g.created_by='system')
			FROM workers w WHERE w.worker_id=$1 FOR UPDATE`, workerID).Scan(&current, &central); err != nil {
			return err
		}
		if !central {
			return connect.NewError(connect.CodePermissionDenied, errors.New("capacity requests only cover the registered central worker"))
		}
		if requested <= current {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("requested capacity must exceed current capacity"))
		}
		var existingRequested int32
		err := tx.QueryRow(ctx, `SELECT change_id, requested_capacity FROM worker_capacity_changes
			WHERE worker_id=$1 AND state IN ('pending','approved') AND expires_at>now()
			ORDER BY created_at DESC LIMIT 1`, workerID).Scan(&changeID, &existingRequested)
		if err == nil {
			requested = existingRequested
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		changeID, err = newID("capacity")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO worker_capacity_changes(change_id, case_id, worker_id, requested_by,
			requested_capacity, previous_capacity, expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			changeID, caseID, workerID, claims.Subject, requested, current, time.Now().Add(24*time.Hour)); err != nil {
			return err
		}
		if _, activityErr := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			VALUES($1,'approval_requested',$2,'Jarvis 请求中央调查执行池临时扩容，等待管理员批准')`, caseID, changeID); activityErr != nil {
			return activityErr
		}
		if err := notifyCaseSessions(ctx, tx, claims.Subject, assetID,
			"SESSION_ATTACHMENT_KIND_WORKER_CAPACITY", changeID, "中央调查执行池等待临时扩容批准。"); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "agent", claims.Subject, "worker_capacity.request", "worker_capacity_change", changeID,
			map[string]any{"case_id": caseID, "asset_id": assetID, "worker_id": workerID, "previous_capacity": current, "requested_capacity": requested})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"approval_id": changeID, "worker_id": workerID, "requested_capacity": requested, "expires_in_seconds": 86400}, nil
}

func createCaseInvestigationRun(ctx context.Context, db *pgxpool.Pool, caseID, sensitiveRef, createdBy string) (string, bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), 9)`, caseID); err != nil {
		return "", false, err
	}
	var assetID, state, candidateID, assignedAgentID string
	var profileSnapshot []byte
	if err := tx.QueryRow(ctx, `SELECT c.asset_id, c.state, COALESCE(r->>'candidate_id',''),
		c.assigned_agent_id, c.agent_profile_snapshot
		FROM investigation_cases c LEFT JOIN LATERAL jsonb_array_elements(c.representatives) r ON true
		WHERE c.case_id=$1 ORDER BY r->>'occurred_at' LIMIT 1`, caseID).
		Scan(&assetID, &state, &candidateID, &assignedAgentID, &profileSnapshot); err != nil {
		return "", false, err
	}
	if state != "queued" {
		return "", false, connect.NewError(connect.CodeFailedPrecondition, errors.New("case is not queued with approved evidence"))
	}
	var frozenProfile frozenAgentProfile
	if assignedAgentID == "" || len(profileSnapshot) == 0 || string(profileSnapshot) == "{}" || json.Unmarshal(profileSnapshot, &frozenProfile) != nil ||
		frozenProfile.AgentID != assignedAgentID || frozenProfile.ConfigDigest == "" || !frozenProfileCoversAsset(frozenProfile, assetID) ||
		!containsString(frozenProfile.Tools, "run.create") {
		return "", false, connect.NewError(connect.CodeFailedPrecondition, errors.New("case agent profile does not permit run creation"))
	}
	if sensitiveRef == "" {
		if err := tx.QueryRow(ctx, `SELECT sensitive_content_ref FROM evidence_requests r JOIN evidence_approvals a USING(approval_id)
			WHERE r.case_id=$1 AND r.state='submitted' AND a.state='approved' AND a.expires_at>now()
			ORDER BY r.submitted_at DESC LIMIT 1`, caseID).Scan(&sensitiveRef); err != nil {
			return "", false, err
		}
	}
	var approvalCount, submittedCount, requestCount int
	if err := tx.QueryRow(ctx, `WITH selected AS (
			SELECT approval_id FROM evidence_requests WHERE case_id=$1 AND sensitive_content_ref=$2 LIMIT 1
		)
		SELECT count(DISTINCT a.approval_id), count(*),
			count(*) FILTER (WHERE r.state='submitted' AND r.sensitive_content_ref=$2)
		FROM evidence_requests r JOIN evidence_approvals a USING(approval_id)
		WHERE r.case_id=$1 AND a.approval_id=(SELECT approval_id FROM selected)
		AND a.state='approved' AND a.expires_at>now()`, caseID, sensitiveRef).
		Scan(&approvalCount, &requestCount, &submittedCount); err != nil {
		return "", false, err
	}
	if approvalCount != 1 || submittedCount != requestCount {
		return "", false, connect.NewError(connect.CodeFailedPrecondition, errors.New("sensitive evidence reference is not approved for case"))
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT run_id FROM runs WHERE created_by=$1 AND state IN ('pending','running') LIMIT 1`, "case:"+caseID).Scan(&existing)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return existing, false, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	runID, err := newID("run")
	if err != nil {
		return "", false, err
	}
	workID, err := newID("work")
	if err != nil {
		return "", false, err
	}
	toolset, _ := json.Marshal([]string{"model.generate"})
	bindings, _ := json.Marshal([]string{assetBinding(assetID), "case:" + caseID})
	createdAt := time.Now()
	ttl := 15 * time.Minute
	planRef := "traffic-investigation:" + caseID
	if _, err := tx.Exec(ctx, `INSERT INTO runs(run_id, state, role, plan_ref, toolset, budget, ttl, bindings, created_by,
		created_at, deadline, agent_profile_id, agent_profile_snapshot,agent_config_digest,case_id)
		VALUES($1,'pending','traffic-investigator',$2,$3::jsonb,'1',$4,$5::jsonb,$6,$7,$8,$9,$10::jsonb,$11,$12)`, runID, planRef, toolset,
		ttl.String(), bindings, "case:"+caseID, createdAt, createdAt.Add(ttl), assignedAgentID, profileSnapshot,
		frozenProfile.ConfigDigest, caseID); err != nil {
		return "", false, err
	}
	budgetID := "work:" + workID
	if _, err := createRunBudgetAccount(ctx, tx, budgetID, runID, 1, ttl, createdAt); err != nil {
		return "", false, err
	}
	_, turnID, err := ensureAgentTurn(ctx, tx, turnSeed{SourceKind: threadSourceRun, SourceRef: runID, SubjectID: runID,
		SourceVersion: 1, SourceCursor: map[string]any{"caseId": caseID, "workId": workID},
		InputSnapshot: map[string]any{"caseId": caseID, "candidateId": candidateID, "sensitiveContentRef": sensitiveRef},
		BudgetID:      budgetID, ContentRef: "case:" + caseID})
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO work_items(work_id, run_id, turn_id, plan_digest, budget_id,
		investigation_case_id, review_candidate_id, sensitive_content_ref,agent_id,agent_config_digest)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, workID, runID, turnID, runPlanDigest(planRef), budgetID,
		caseID, candidateID, sensitiveRef, assignedAgentID, frozenProfile.ConfigDigest); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO run_sagas(run_id, work_id) VALUES($1,$2)`, runID, workID); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
		VALUES($1,'run_progress',$2,'短命只读调查执行实例已排队')`, caseID, runID); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET assigned_run_id=$2,assigned_agent_config_digest=$3,updated_at=now()
		WHERE case_id=$1`, caseID, runID, frozenProfile.ConfigDigest); err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return runID, true, nil
}
