package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	evidencev1 "yufeng/proto/gen/evidencev1"
)

func TestSensitiveRelayOwnsImmutableEvidenceAndRejectsReferenceReuse(t *testing.T) {
	relay := NewSensitiveRelay()
	fragment := &evidencev1.EvidenceFragment{EvidenceHandle: "handle", Field: "body", Content: []byte("original")}
	entry := sensitiveRelayEntry{approvalID: "approval", fragments: []*evidencev1.EvidenceFragment{fragment}, bytes: int64(len(fragment.Content)), expiresAt: time.Now().Add(time.Minute)}
	if err := relay.put("sensitive-ref", entry); err != nil {
		t.Fatal(err)
	}
	fragment.Content[0] = 'x'
	loaded, ok := relay.get("sensitive-ref")
	if !ok || string(loaded.fragments[0].GetContent()) != "original" {
		t.Fatalf("relay retained caller-owned evidence: ok=%v content=%q", ok, loaded.fragments[0].GetContent())
	}
	loaded.fragments[0].Content[0] = 'y'
	loadedAgain, ok := relay.get("sensitive-ref")
	if !ok || string(loadedAgain.fragments[0].GetContent()) != "original" {
		t.Fatalf("relay exposed mutable evidence: ok=%v content=%q", ok, loadedAgain.fragments[0].GetContent())
	}
	if err := relay.put("sensitive-ref", entry); err == nil {
		t.Fatal("duplicate sensitive reference must fail closed")
	}
}

func TestSubmitEvidenceBundleDurablyContinuesCaseReviewOnce(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID, caseID := "submit-asset-"+suffix, "submit-case-"+suffix
	unitID, unitToken := "submit-unit-"+suffix, "submit-token-"+suffix
	profileID, jarvisID := "submit-profile-"+suffix, "submit-jarvis-"+suffix
	approvalID, requestID := "submit-approval-"+suffix, "submit-request-"+suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash, token_expires_at)
		VALUES($1,'edge',$2,now()+interval '1 hour')`, unitID, hashToken(unitToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,$2)`, unitID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'open',90,'submit evidence case')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO managed_agent_profiles(agent_id, display_name, tools, bindings, created_by)
		VALUES($1,'证据闭环 Agent','["case.get","case.request_evidence","run.create"]',$2::jsonb,'operator')`,
		profileID, fmt.Sprintf(`[{"kind":"asset","id":%q}]`, assetID)); err != nil {
		t.Fatal(err)
	}
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		_, err := assignCaseAgentProfile(ctx, tx, caseID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES($1,'x','orchestrator','registered-public-key')`, jarvisID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO evidence_approvals(approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, state, requested_by, expires_at)
		VALUES($1,$2,$3,$4,'["handle-test"]','["body"]',1024,'sha256:model','approved',$5,now()+interval '15 minutes')`,
		approvalID, caseID, assetID, unitID, jarvisID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO evidence_requests(request_id, approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, state, expires_at)
		VALUES($1,$2,$3,$4,$5,'["handle-test"]','["body"]',1024,'sha256:model','pending',now()+interval '15 minutes')`,
		requestID, approvalID, caseID, assetID, unitID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE investigation_cases SET state='waiting_evidence_approval' WHERE case_id=$1`, caseID); err != nil {
		t.Fatal(err)
	}
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := NewEvidenceServer(st.Pool(), NewSensitiveRelay(), NewAgentServer(st.Pool(), "unused", signingKey), jarvisID)
	poll := connect.NewRequest(&evidencev1.PollEvidenceRequestsRequest{UnitId: unitID})
	poll.Header().Set("Authorization", "Bearer "+unitToken)
	polled, err := server.PollEvidenceRequests(ctx, poll)
	if err != nil {
		t.Fatalf("poll evidence request: %v", err)
	}
	if len(polled.Msg.GetRequests()) != 1 {
		t.Fatalf("poll evidence request: requests=%v", polled.Msg.GetRequests())
	}
	content := []byte("approved-sensitive-body")
	digestBytes := sha256.Sum256(content)
	req := connect.NewRequest(&evidencev1.SubmitEvidenceBundleRequest{
		RequestId: requestID, ApprovalId: approvalID, CaseId: caseID,
		Fragments: []*evidencev1.EvidenceFragment{{EvidenceHandle: "handle-test", Field: "body", Content: content,
			ContentDigest: "sha256:" + hex.EncodeToString(digestBytes[:])}},
		LeaseId: polled.Msg.GetRequests()[0].GetLeaseId(), LeaseEpoch: polled.Msg.GetRequests()[0].GetLeaseEpoch(),
	})
	req.Msg.BundleDigest = sensitiveEntryDigest(req.Msg.GetFragments())
	req.Header().Set("Authorization", "Bearer "+unitToken)
	response, err := server.SubmitEvidenceBundle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var caseState string
	var continuations int
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&caseState); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions
		WHERE kind='CASE_REVIEW' AND payload_ref=$1 AND dedupe_key LIKE 'evidence-submitted:%'`, caseID).Scan(&continuations); err != nil {
		t.Fatal(err)
	}
	if caseState != "queued" || continuations != 1 {
		t.Fatalf("case state=%s continuations=%d want queued/1", caseState, continuations)
	}
	duplicate, err := server.SubmitEvidenceBundle(ctx, req)
	if err != nil || duplicate.Msg.GetSensitiveContentRef() != response.Msg.GetSensitiveContentRef() {
		t.Fatalf("duplicate evidence submit must be idempotent: response=%v err=%v", duplicate, err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions
		WHERE kind='CASE_REVIEW' AND payload_ref=$1 AND dedupe_key LIKE 'evidence-submitted:%'`, caseID).Scan(&continuations); err != nil {
		t.Fatal(err)
	}
	if continuations != 1 {
		t.Fatalf("duplicate submit created %d continuations", continuations)
	}
	runID, created, err := createCaseInvestigationRun(ctx, st.Pool(), caseID, response.Msg.GetSensitiveContentRef(), jarvisID)
	if err != nil || !created {
		t.Fatalf("create first case run id=%s created=%v err=%v", runID, created, err)
	}
	var bindingsRaw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT bindings FROM runs WHERE run_id=$1`, runID).Scan(&bindingsRaw); err != nil {
		t.Fatal(err)
	}
	var runBindings []string
	if err := json.Unmarshal(bindingsRaw, &runBindings); err != nil {
		t.Fatal(err)
	}
	if !bindingsSubset(runBindings, []string{assetBinding(assetID)}) {
		t.Fatalf("asset-bound worker must cover Brain-derived case binding: run=%v", runBindings)
	}
	if err := ResetSensitiveEvidenceRequests(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	var requestState, storedRef, runState, workState, budgetState string
	if err := st.Pool().QueryRow(ctx, `SELECT state, sensitive_content_ref FROM evidence_requests WHERE request_id=$1`, requestID).
		Scan(&requestState, &storedRef); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT r.state, w.status, b.state FROM runs r JOIN work_items w USING(run_id)
		JOIN run_budget_accounts b ON b.run_id=r.run_id WHERE r.run_id=$1`, runID).Scan(&runState, &workState, &budgetState); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&caseState); err != nil {
		t.Fatal(err)
	}
	if requestState != "pending" || storedRef != "" || runState != "failed" || workState != "failed" || budgetState != "failed" || caseState != "waiting_evidence_approval" {
		t.Fatalf("restart states request=%s ref=%q run=%s work=%s budget=%s case=%s", requestState, storedRef, runState, workState, budgetState, caseState)
	}
	restarted := NewEvidenceServer(st.Pool(), NewSensitiveRelay(), NewAgentServer(st.Pool(), "unused", signingKey), jarvisID)
	polled, err = restarted.PollEvidenceRequests(ctx, poll)
	if err != nil {
		t.Fatalf("poll reset evidence request: %v", err)
	}
	if len(polled.Msg.GetRequests()) != 1 {
		t.Fatalf("poll reset evidence request: requests=%v", polled.Msg.GetRequests())
	}
	req.Msg.LeaseId = polled.Msg.GetRequests()[0].GetLeaseId()
	req.Msg.LeaseEpoch = polled.Msg.GetRequests()[0].GetLeaseEpoch()
	retry, err := restarted.SubmitEvidenceBundle(ctx, req)
	if err != nil {
		t.Fatalf("resubmit approved evidence after restart: %v", err)
	}
	if retry.Msg.GetSensitiveContentRef() == response.Msg.GetSensitiveContentRef() {
		t.Fatal("restart must mint a fresh sensitive content reference")
	}
	newRunID, created, err := createCaseInvestigationRun(ctx, st.Pool(), caseID, retry.Msg.GetSensitiveContentRef(), jarvisID)
	if err != nil || !created || newRunID == runID {
		t.Fatalf("create replacement case run id=%s created=%v err=%v", newRunID, created, err)
	}
}

func TestRecoverSensitiveGenerationAfterEffectMarksOutcomeUnknown(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID := "recover-asset-" + suffix
	caseID := "recover-case-" + suffix
	profileID := "recover-profile-" + suffix
	approvalID := "recover-approval-" + suffix
	requestID := "recover-request-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'open',90,'recover outcome case')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO managed_agent_profiles(agent_id, display_name, tools, bindings, created_by)
		VALUES($1,'恢复测试 Agent','["case.get","case.request_evidence","run.create"]',
		jsonb_build_array(jsonb_build_object('kind','asset','id',$2::text)),'operator')`, profileID, assetID); err != nil {
		t.Fatal(err)
	}
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		_, assignErr := assignCaseAgentProfile(ctx, tx, caseID)
		return assignErr
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO evidence_approvals(approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, state, requested_by, expires_at)
		VALUES($1,$2,$3,'unit-recover','["handle-recover"]','["body"]',1024,'sha256:model','approved','jarvis',now()+interval '15 minutes')`,
		approvalID, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	sensitiveRef := "sensitive-recover-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO evidence_requests(request_id, approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, state, expires_at, submitted_at, sensitive_content_ref)
		VALUES($1,$2,$3,$4,'unit-recover','["handle-recover"]','["body"]',1024,'sha256:model','submitted',
		now()+interval '15 minutes',now(),$5)`, requestID, approvalID, caseID, assetID, sensitiveRef); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE investigation_cases SET state='queued' WHERE case_id=$1`, caseID); err != nil {
		t.Fatal(err)
	}
	runID, created, err := createCaseInvestigationRun(ctx, st.Pool(), caseID, sensitiveRef, "jarvis")
	if err != nil || !created {
		t.Fatalf("create case run id=%s created=%v err=%v", runID, created, err)
	}
	var workID, turnID, budgetID, stepID string
	if err := st.Pool().QueryRow(ctx, `SELECT w.work_id, w.turn_id, w.budget_id, s.step_id
		FROM work_items w JOIN agent_steps s ON s.turn_id=w.turn_id WHERE w.run_id=$1`, runID).
		Scan(&workID, &turnID, &budgetID, &stepID); err != nil {
		t.Fatal(err)
	}
	for _, update := range []struct {
		query string
		args  []any
	}{
		{`UPDATE runs SET state='running' WHERE run_id=$1`, []any{runID}},
		{`UPDATE work_items SET status='leased', worker_id='worker-recover', lease_id='lease-recover', lease_epoch=1,
			lease_deadline=now()+interval '10 minutes' WHERE work_id=$1`, []any{workID}},
		{`UPDATE run_sagas SET state='running' WHERE run_id=$1`, []any{runID}},
		{`UPDATE agent_turns SET state='running' WHERE turn_id=$1`, []any{turnID}},
		{`UPDATE investigation_cases SET state='investigating' WHERE case_id=$1`, []any{caseID}},
	} {
		if _, err := st.Pool().Exec(ctx, update.query, update.args...); err != nil {
			t.Fatal(err)
		}
	}
	attemptID := "attempt-recover-" + suffix
	generationID := "generation-recover-" + suffix
	reservationID := ""
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		var reserveErr error
		reservationID, reserveErr = reserveRunBudget(ctx, tx, budgetID, "model", attemptID, runBudgetAmount{
			ModelCalls: 1, InputTokens: 8, OutputTokens: 8, CostMicrounits: 1,
		})
		return reserveErr
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO model_generations(generation_id, turn_id, step_id, request_digest,
		request_payload, context_manifest, generation_limits, state, sensitive, approval_id, case_id, sensitive_content_digest)
		VALUES($1,$2,$3,'sha256:request','{}','{}','{}','running',true,$4,$5,'sha256:content')`,
		generationID, turnID, stepID, approvalID, caseID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO model_attempts(attempt_id, generation_id, attempt_sequence, lease_epoch,
		state, request_digest, budget_reservation_id, effect_started_at)
		VALUES($1,$2,1,1,'effect_started','sha256:request',$3,now())`, attemptID, generationID, reservationID); err != nil {
		t.Fatal(err)
	}
	if err := RecoverSensitiveGenerationOutcomes(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	var attemptState, reservationState, budgetState, generationState, workState, runState, sagaState, turnState, caseState string
	if err := st.Pool().QueryRow(ctx, `SELECT a.state, br.state, b.state, g.state, w.status, r.state, rs.state, t.state, c.state
		FROM model_attempts a JOIN model_generations g USING(generation_id)
		JOIN work_items w ON w.turn_id=g.turn_id JOIN runs r USING(run_id) JOIN run_sagas rs USING(run_id)
		JOIN agent_turns t ON t.turn_id=w.turn_id JOIN investigation_cases c ON c.case_id=w.investigation_case_id
		JOIN run_budget_accounts b ON b.budget_id=w.budget_id
		JOIN run_budget_reservations br ON br.reservation_id=a.budget_reservation_id
		WHERE a.attempt_id=$1`, attemptID).Scan(&attemptState, &reservationState, &budgetState, &generationState,
		&workState, &runState, &sagaState, &turnState, &caseState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "outcome_unknown" || reservationState != "outcome_unknown" || budgetState != "outcome_unknown" ||
		generationState != "failed" || workState != "failed" || runState != "outcome_unknown" ||
		sagaState != "outcome_unknown" || turnState != "outcome_unknown" || caseState != "failed" {
		t.Fatalf("recovered states attempt=%s reservation=%s budget=%s generation=%s work=%s run=%s saga=%s turn=%s case=%s",
			attemptState, reservationState, budgetState, generationState, workState, runState, sagaState, turnState, caseState)
	}
}

func TestExpireSensitiveApprovalClosesRequestAndCaseOnce(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID := "sensitive-asset-" + suffix
	caseID := "sensitive-case-" + suffix
	approvalID := "sensitive-approval-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'investigating',80,'test')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO evidence_approvals(approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, state, requested_by, expires_at)
		VALUES($1,$2,$3,'unit-test','["handle-test"]','["body"]',1024,'sha256:old','approved','jarvis-1',now()+interval '15 minutes')`,
		approvalID, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO evidence_requests(request_id, approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, state, expires_at, sensitive_content_ref)
		VALUES($1,$2,$3,$4,'unit-test','["handle-test"]','["body"]',1024,'sha256:old','submitted',now()+interval '15 minutes','sensitive-test')`,
		"evidence-"+suffix, approvalID, caseID, assetID); err != nil {
		t.Fatal(err)
	}

	changed, err := expireSensitiveApproval(ctx, st.Pool(), approvalID, caseID, "模型配置变化")
	if err != nil || !changed {
		t.Fatalf("expireSensitiveApproval() changed=%v err=%v", changed, err)
	}
	var approvalState, requestState, caseState string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM evidence_approvals WHERE approval_id=$1`, approvalID).Scan(&approvalState); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM evidence_requests WHERE approval_id=$1`, approvalID).Scan(&requestState); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&caseState); err != nil {
		t.Fatal(err)
	}
	if approvalState != "expired" || requestState != "expired" || caseState != "evidence_expired" {
		t.Fatalf("states approval=%s request=%s case=%s", approvalState, requestState, caseState)
	}

	changed, err = expireSensitiveApproval(ctx, st.Pool(), approvalID, caseID, "模型配置变化")
	if err != nil || changed {
		t.Fatalf("second expiration changed=%v err=%v", changed, err)
	}
	var activities int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM case_activities
		WHERE case_id=$1 AND kind='state_changed' AND ref_id=$2`, caseID, approvalID).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if activities != 1 {
		t.Fatalf("expiration activities=%d want 1", activities)
	}
}

func TestDecideEvidenceExpiresRequestWhenModelConfigurationChanged(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID := "configuration-asset-" + suffix
	caseID := "configuration-case-" + suffix
	approvalID := "configuration-approval-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'waiting_evidence_approval',80,'test')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO evidence_approvals(approval_id, case_id, asset_id, unit_id,
		evidence_handles, allowed_fields, max_bytes, model_config_digest, requested_by, expires_at)
		VALUES($1,$2,$3,'unit-test','["handle-test"]','["body"]',1024,'sha256:stale','jarvis-1',now()+interval '15 minutes')`,
		approvalID, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	server := &AgentInteractionServer{pool: st.Pool()}
	if _, err := server.decideEvidence(ctx, approvalID, "operator-test", true); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("changed model configuration error=%v", err)
	}
	var approvalState, caseState string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM evidence_approvals WHERE approval_id=$1`, approvalID).Scan(&approvalState); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&caseState); err != nil {
		t.Fatal(err)
	}
	if approvalState != "expired" || caseState != "evidence_expired" {
		t.Fatalf("states approval=%s case=%s", approvalState, caseState)
	}
	var auditCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_entries
		WHERE object_type='evidence_approval' AND object_id=$1 AND action='evidence_approval.expire'`, approvalID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("model configuration expiration audit entries=%d want 1", auditCount)
	}
}
