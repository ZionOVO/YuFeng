package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	workerv1 "yufeng/proto/gen/workerv1"
)

const runSagaSchemaVersion = "run-saga/v1"

func bindRunSaga(ctx context.Context, tx pgx.Tx, runID string, plan *workerv1.RunSagaPlan) (*workerv1.RunSagaSnapshot, error) {
	if plan == nil || plan.GetSchemaVersion() != runSagaSchemaVersion || len(plan.GetSteps()) == 0 || len(plan.GetSteps()) > 128 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("saga plan is invalid"))
	}
	seen := map[string]bool{}
	for i, step := range plan.GetSteps() {
		if step == nil || step.GetSequence() != int32(i+1) || strings.TrimSpace(step.GetStepKey()) == "" || len(step.GetStepKey()) > 128 || seen[step.GetStepKey()] {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("saga plan steps must have contiguous sequence and unique keys"))
		}
		seen[step.GetStepKey()] = true
		if replayName(step.GetActionReplay()) == "" || replayName(step.GetCompensationReplay()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("saga plan replay policy is required"))
		}
	}
	digest, err := runSagaPlanDigest(plan)
	if err != nil {
		return nil, err
	}
	if plan.GetPlanDigest() != digest {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("saga plan digest mismatch"))
	}
	var stored string
	if err := tx.QueryRow(ctx, `SELECT plan_digest FROM run_sagas WHERE run_id=$1 FOR UPDATE`, runID).Scan(&stored); err != nil {
		return nil, err
	}
	if stored != "" && stored != digest {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("saga plan is already bound to a different digest"))
	}
	if stored == "" {
		for _, step := range plan.GetSteps() {
			if _, err := tx.Exec(ctx, `INSERT INTO run_saga_steps(
				run_id, step_sequence, step_key, action_replay, has_compensation, compensation_replay)
				VALUES($1,$2,$3,$4,$5,$6)`, runID, step.GetSequence(), step.GetStepKey(), replayName(step.GetActionReplay()),
				step.GetHasCompensation(), replayName(step.GetCompensationReplay())); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE run_sagas SET plan_digest=$2, state='running', updated_at=now() WHERE run_id=$1`, runID, digest); err != nil {
			return nil, err
		}
		if err := appendRunSagaAudit(ctx, tx, runID, "saga_plan_bound", digest, map[string]any{
			"plan_digest": digest, "step_count": len(plan.GetSteps()),
		}); err != nil {
			return nil, err
		}
	}
	return loadRunSagaSnapshot(ctx, tx, runID)
}

func recordRunSaga(ctx context.Context, tx pgx.Tx, runID string, receipt *workerv1.RunSagaReceipt) (*workerv1.RunSagaSnapshot, error) {
	if receipt == nil || receipt.GetSequence() <= 0 || strings.TrimSpace(receipt.GetStepKey()) == "" || receipt.GetPhase() == workerv1.RunStepPhase_RUN_STEP_PHASE_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("saga receipt is invalid"))
	}
	if len(receipt.GetGuardDigest()) > 128 || len(receipt.GetReceiptRef()) > 2048 || len(receipt.GetError()) > 2048 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("saga receipt field is too large"))
	}
	var sagaState, planDigest string
	var cancelRequested bool
	if err := tx.QueryRow(ctx, `SELECT state, plan_digest, cancel_requested FROM run_sagas WHERE run_id=$1 FOR UPDATE`, runID).
		Scan(&sagaState, &planDigest, &cancelRequested); err != nil {
		return nil, err
	}
	if planDigest == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("saga plan is not bound"))
	}
	if sagaState == "outcome_unknown" {
		return loadRunSagaSnapshot(ctx, tx, runID)
	}
	var stepKey, actionReplay, compensationReplay, actionPhase, compensationPhase, guardDigest string
	var hasCompensation, actionEffectStarted bool
	if err := tx.QueryRow(ctx, `SELECT step_key, action_replay, has_compensation, compensation_replay,
		action_phase, compensation_phase, guard_digest, action_effect_started FROM run_saga_steps
		WHERE run_id=$1 AND step_sequence=$2 FOR UPDATE`, runID, receipt.GetSequence()).
		Scan(&stepKey, &actionReplay, &hasCompensation, &compensationReplay, &actionPhase, &compensationPhase, &guardDigest, &actionEffectStarted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("saga step is not in the bound plan"))
		}
		return nil, err
	}
	if stepKey != receipt.GetStepKey() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("saga step key does not match sequence"))
	}
	if guardDigest != "" && receipt.GetGuardDigest() != "" && guardDigest != receipt.GetGuardDigest() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("saga guard digest changed"))
	}

	phase := receipt.GetPhase()
	if currentPhaseMatches(phase, actionPhase, compensationPhase) {
		return loadRunSagaSnapshot(ctx, tx, runID)
	}
	nextAction, nextCompensation := actionPhase, compensationPhase
	switch phase {
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_INTENT_RECORDED:
		if actionPhase != "pending" {
			return nil, invalidSagaTransition()
		}
		nextAction = "intent_recorded"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_EFFECT_STARTED:
		if actionPhase != "intent_recorded" && (actionPhase != "effect_started" || actionReplay == "never_replay") {
			return nil, invalidSagaTransition()
		}
		nextAction = "effect_started"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_SUCCEEDED:
		if actionPhase != "effect_started" {
			return nil, invalidSagaTransition()
		}
		nextAction = "succeeded"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_FAILED:
		if actionPhase != "intent_recorded" && actionPhase != "effect_started" {
			return nil, invalidSagaTransition()
		}
		nextAction = "failed"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_INTENT_RECORDED:
		if !hasCompensation || !actionEffectStarted || (actionPhase != "effect_started" && actionPhase != "succeeded" && actionPhase != "failed") {
			return nil, invalidSagaTransition()
		}
		if compensationPhase != "pending" && (compensationPhase != "failed" || compensationReplay == "never_replay") {
			return nil, invalidSagaTransition()
		}
		nextCompensation = "intent_recorded"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_EFFECT_STARTED:
		if compensationPhase != "intent_recorded" && (compensationPhase != "effect_started" || compensationReplay == "never_replay") {
			return nil, invalidSagaTransition()
		}
		nextCompensation = "effect_started"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATED:
		if compensationPhase != "effect_started" {
			return nil, invalidSagaTransition()
		}
		nextCompensation = "compensated"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_FAILED:
		if compensationPhase != "effect_started" {
			return nil, invalidSagaTransition()
		}
		nextCompensation = "failed"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_OUTCOME_UNKNOWN:
		if (actionPhase != "effect_started" || actionReplay != "never_replay") &&
			(compensationPhase != "effect_started" && compensationPhase != "failed" || compensationReplay != "never_replay") {
			return nil, invalidSagaTransition()
		}
		if compensationPhase == "effect_started" || compensationPhase == "failed" {
			nextCompensation = "outcome_unknown"
		} else {
			nextAction = "outcome_unknown"
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported saga phase"))
	}

	storedGuard := guardDigest
	if storedGuard == "" {
		storedGuard = receipt.GetGuardDigest()
	}
	actionReceipt, compensationReceipt := "", ""
	if phase == workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_SUCCEEDED {
		actionReceipt = receipt.GetReceiptRef()
	}
	if phase == workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATED {
		compensationReceipt = receipt.GetReceiptRef()
	}
	if _, err := tx.Exec(ctx, `UPDATE run_saga_steps SET action_phase=$3, compensation_phase=$4,
		guard_digest=$5,
		action_receipt_ref=CASE WHEN $6<>'' THEN $6 ELSE action_receipt_ref END,
		compensation_receipt_ref=CASE WHEN $7<>'' THEN $7 ELSE compensation_receipt_ref END,
		error=CASE WHEN $8<>'' THEN $8 ELSE error END,
		action_effect_started=action_effect_started OR $9, updated_at=now()
		WHERE run_id=$1 AND step_sequence=$2`, runID, receipt.GetSequence(), nextAction, nextCompensation,
		storedGuard, actionReceipt, compensationReceipt, receipt.GetError(), actionEffectStarted || phase == workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_EFFECT_STARTED); err != nil {
		return nil, err
	}
	if phase == workerv1.RunStepPhase_RUN_STEP_PHASE_OUTCOME_UNKNOWN {
		if _, err := tx.Exec(ctx, `UPDATE run_sagas SET state='outcome_unknown', cause=$2, updated_at=now() WHERE run_id=$1`, runID, receipt.GetError()); err != nil {
			return nil, err
		}
	} else if err := refreshRunSagaState(ctx, tx, runID, cancelRequested, receipt.GetError()); err != nil {
		return nil, err
	}
	receiptDigest := auditPayloadDigest(receipt.GetReceiptRef())
	errorDigest := auditPayloadDigest(receipt.GetError())
	payloadDigest := receiptDigest
	if payloadDigest == "" {
		payloadDigest = errorDigest
	}
	if payloadDigest == "" {
		payloadDigest = auditPayloadDigest(receipt.GetGuardDigest())
	}
	if err := appendRunSagaAudit(ctx, tx, runID, strings.ToLower(phase.String()), payloadDigest, map[string]any{
		"step_sequence": receipt.GetSequence(), "step_key_digest": auditPayloadDigest(stepKey), "phase": phase.String(),
		"guard_digest": auditPayloadDigest(receipt.GetGuardDigest()), "receipt_digest": receiptDigest, "error_digest": errorDigest,
	}); err != nil {
		return nil, err
	}
	return loadRunSagaSnapshot(ctx, tx, runID)
}

func appendRunSagaAudit(ctx context.Context, tx pgx.Tx, runID, kind, payloadDigest string, details map[string]any) error {
	coordinates, workerID, err := runAuditCoordinates(ctx, tx, runID)
	if err != nil {
		return err
	}
	if workerID == "" {
		workerID = "brain"
	}
	coordinates.PayloadDigest = auditPayloadDigest(payloadDigest)
	return appendAgentAuditTx(ctx, tx, "worker", workerID, "run."+kind, "run", runID, coordinates, details)
}

func loadRunSagaSnapshot(ctx context.Context, db dbTX, runID string) (*workerv1.RunSagaSnapshot, error) {
	var out workerv1.RunSagaSnapshot
	if err := db.QueryRow(ctx, `SELECT state, plan_digest, cancel_requested, cause FROM run_sagas WHERE run_id=$1`, runID).
		Scan(&out.State, &out.PlanDigest, &out.CancelRequested, &out.Cause); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT step_sequence, step_key, action_replay, has_compensation, compensation_replay,
		action_phase, compensation_phase, guard_digest, action_receipt_ref, compensation_receipt_ref, error, action_effect_started
		FROM run_saga_steps WHERE run_id=$1 ORDER BY step_sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int32
		var stepKey, actionReplay, compensationReplay, actionPhase, compensationPhase string
		var guardDigest, actionReceipt, compensationReceipt, stepErr string
		var hasCompensation, actionEffectStarted bool
		if err := rows.Scan(&sequence, &stepKey, &actionReplay, &hasCompensation, &compensationReplay,
			&actionPhase, &compensationPhase, &guardDigest, &actionReceipt, &compensationReceipt, &stepErr, &actionEffectStarted); err != nil {
			return nil, err
		}
		out.Steps = append(out.Steps, &workerv1.RunSagaStepSnapshot{
			Plan: &workerv1.RunSagaStepPlan{Sequence: sequence, StepKey: stepKey, ActionReplay: replayProto(actionReplay),
				HasCompensation: hasCompensation, CompensationReplay: replayProto(compensationReplay)},
			ActionPhase: phaseProto(actionPhase, false), CompensationPhase: phaseProto(compensationPhase, true),
			GuardDigest: guardDigest, ActionReceiptRef: actionReceipt, CompensationReceiptRef: compensationReceipt, Error: stepErr,
			ActionEffectStarted: actionEffectStarted,
		})
	}
	return &out, rows.Err()
}

func refreshRunSagaState(ctx context.Context, tx pgx.Tx, runID string, cancelRequested bool, cause string) error {
	rows, err := tx.Query(ctx, `SELECT has_compensation, action_phase, compensation_phase, action_effect_started FROM run_saga_steps
		WHERE run_id=$1 ORDER BY step_sequence`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	allSucceeded := true
	failed := false
	needsCompensation := false
	compensationComplete := true
	for rows.Next() {
		var hasCompensation, actionEffectStarted bool
		var actionPhase, compensationPhase string
		if err := rows.Scan(&hasCompensation, &actionPhase, &compensationPhase, &actionEffectStarted); err != nil {
			return err
		}
		if actionPhase != "succeeded" {
			allSucceeded = false
		}
		if actionPhase == "failed" {
			failed = true
		}
		if hasCompensation && actionEffectStarted {
			needsCompensation = true
			if compensationPhase != "compensated" {
				compensationComplete = false
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	state := "running"
	switch {
	case cancelRequested && (!needsCompensation || compensationComplete):
		state = "compensated"
	case cancelRequested:
		state = "cancelling"
	case failed && (!needsCompensation || compensationComplete):
		state = "compensated"
	case failed:
		state = "compensating"
	case allSucceeded:
		state = "ready"
	}
	_, err = tx.Exec(ctx, `UPDATE run_sagas SET state=$2,
		cause=CASE WHEN $3<>'' THEN $3 ELSE cause END, updated_at=now() WHERE run_id=$1`, runID, state, cause)
	return err
}

func runSagaPlanDigest(plan *workerv1.RunSagaPlan) (string, error) {
	type digestStep struct {
		Sequence           int32  `json:"sequence"`
		StepKey            string `json:"step_key"`
		ActionReplay       string `json:"action_replay"`
		HasCompensation    bool   `json:"has_compensation"`
		CompensationReplay string `json:"compensation_replay"`
	}
	steps := make([]digestStep, 0, len(plan.GetSteps()))
	for _, step := range plan.GetSteps() {
		steps = append(steps, digestStep{step.GetSequence(), step.GetStepKey(), replayName(step.GetActionReplay()), step.GetHasCompensation(), replayName(step.GetCompensationReplay())})
	}
	raw, err := json.Marshal(struct {
		SchemaVersion string       `json:"schema_version"`
		Steps         []digestStep `json:"steps"`
	}{plan.GetSchemaVersion(), steps})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func replayName(in workerv1.RunStepReplayPolicy) string {
	switch in {
	case workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_SAFE:
		return "safe"
	case workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_IDEMPOTENT:
		return "idempotent"
	case workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_NEVER_REPLAY:
		return "never_replay"
	default:
		return ""
	}
}

func replayProto(in string) workerv1.RunStepReplayPolicy {
	switch in {
	case "safe":
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_SAFE
	case "idempotent":
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_IDEMPOTENT
	case "never_replay":
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_NEVER_REPLAY
	default:
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_UNSPECIFIED
	}
}

func phaseProto(in string, compensation bool) workerv1.RunStepPhase {
	if compensation {
		switch in {
		case "intent_recorded":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_INTENT_RECORDED
		case "effect_started":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_EFFECT_STARTED
		case "compensated":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATED
		case "failed":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_FAILED
		case "outcome_unknown":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_OUTCOME_UNKNOWN
		}
	} else {
		switch in {
		case "intent_recorded":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_INTENT_RECORDED
		case "effect_started":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_EFFECT_STARTED
		case "succeeded":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_SUCCEEDED
		case "failed":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_FAILED
		case "outcome_unknown":
			return workerv1.RunStepPhase_RUN_STEP_PHASE_OUTCOME_UNKNOWN
		}
	}
	return workerv1.RunStepPhase_RUN_STEP_PHASE_UNSPECIFIED
}

func currentPhaseMatches(phase workerv1.RunStepPhase, action, compensation string) bool {
	switch phase {
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_INTENT_RECORDED:
		return action == "intent_recorded"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_EFFECT_STARTED:
		return action == "effect_started"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_SUCCEEDED:
		return action == "succeeded"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_FAILED:
		return action == "failed"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_INTENT_RECORDED:
		return compensation == "intent_recorded"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_EFFECT_STARTED:
		return compensation == "effect_started"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATED:
		return compensation == "compensated"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_FAILED:
		return compensation == "failed"
	case workerv1.RunStepPhase_RUN_STEP_PHASE_OUTCOME_UNKNOWN:
		return action == "outcome_unknown" || compensation == "outcome_unknown"
	default:
		return false
	}
}

func invalidSagaTransition() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("invalid saga state transition"))
}

func ensureRunSagaTerminal(ctx context.Context, tx pgx.Tx, runID string, complete bool) (string, bool, error) {
	var state, planDigest string
	var cancelRequested bool
	if err := tx.QueryRow(ctx, `SELECT state, plan_digest, cancel_requested FROM run_sagas WHERE run_id=$1 FOR UPDATE`, runID).Scan(&state, &planDigest, &cancelRequested); err != nil {
		return "", false, err
	}
	if complete {
		if state != "ready" || cancelRequested {
			return state, cancelRequested, connect.NewError(connect.CodeFailedPrecondition, errors.New("saga is not ready to complete"))
		}
		return state, cancelRequested, nil
	}
	if state == "pending" && planDigest == "" {
		if _, err := tx.Exec(ctx, `UPDATE run_sagas SET state='compensated', updated_at=now() WHERE run_id=$1`, runID); err != nil {
			return "", false, err
		}
		return "compensated", cancelRequested, nil
	}
	if state != "compensated" && state != "outcome_unknown" {
		return state, cancelRequested, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("saga is not settled: %s", state))
	}
	return state, cancelRequested, nil
}
