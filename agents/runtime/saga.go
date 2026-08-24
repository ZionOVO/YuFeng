package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// ReplayPolicy 约束崩溃恢复时能否再次跨越同一副作用边界。
type ReplayPolicy string

const (
	ReplaySafe       ReplayPolicy = "safe"
	ReplayIdempotent ReplayPolicy = "idempotent"
	ReplayNever      ReplayPolicy = "never_replay"
)

// SagaPhase 是持久补偿事务的一次状态转换。
type SagaPhase string

const (
	PhaseActionIntent       SagaPhase = "action_intent_recorded"
	PhaseActionEffect       SagaPhase = "action_effect_started"
	PhaseActionSucceeded    SagaPhase = "action_succeeded"
	PhaseActionFailed       SagaPhase = "action_failed"
	PhaseCompensationIntent SagaPhase = "compensation_intent_recorded"
	PhaseCompensationEffect SagaPhase = "compensation_effect_started"
	PhaseCompensated        SagaPhase = "compensated"
	PhaseCompensationFailed SagaPhase = "compensation_failed"
	PhaseOutcomeUnknown     SagaPhase = "outcome_unknown"
)

const sagaSchemaVersion = "run-saga/v1"

// ErrOutcomeUnknown 表示不可重放动作已跨越副作用边界但没有权威结算。
var ErrOutcomeUnknown = errors.New("outcome_unknown")

// SagaStepPlan 固定一条命令及其恢复策略。
type SagaStepPlan struct {
	Sequence           int32        `json:"sequence"`
	StepKey            string       `json:"step_key"`
	ActionReplay       ReplayPolicy `json:"action_replay"`
	HasCompensation    bool         `json:"has_compensation"`
	CompensationReplay ReplayPolicy `json:"compensation_replay"`
}

// SagaPlan 是执行前必须持久化的有序命令计划。
type SagaPlan struct {
	SchemaVersion string         `json:"schema_version"`
	Steps         []SagaStepPlan `json:"steps"`
	PlanDigest    string         `json:"plan_digest"`
}

// SagaReceipt 描述一条已持久化的动作或补偿边界。
type SagaReceipt struct {
	Sequence    int32     `json:"sequence"`
	StepKey     string    `json:"step_key"`
	Phase       SagaPhase `json:"phase"`
	GuardDigest string    `json:"guard_digest,omitempty"`
	ReceiptRef  string    `json:"receipt_ref,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// SagaStepSnapshot 是一个步骤的权威恢复投影。
type SagaStepSnapshot struct {
	Plan                   SagaStepPlan `json:"plan"`
	ActionPhase            SagaPhase    `json:"action_phase,omitempty"`
	CompensationPhase      SagaPhase    `json:"compensation_phase,omitempty"`
	GuardDigest            string       `json:"guard_digest,omitempty"`
	ActionReceiptRef       string       `json:"action_receipt_ref,omitempty"`
	CompensationReceiptRef string       `json:"compensation_receipt_ref,omitempty"`
	Error                  string       `json:"error,omitempty"`
	ActionEffectStarted    bool         `json:"action_effect_started"`
}

// SagaSnapshot 是中台返回的持久补偿事务恢复点。
type SagaSnapshot struct {
	State           string             `json:"state"`
	PlanDigest      string             `json:"plan_digest"`
	CancelRequested bool               `json:"cancel_requested"`
	Steps           []SagaStepSnapshot `json:"steps"`
	Cause           string             `json:"cause,omitempty"`
}

// SagaProgress 是本地监督代理转发给中台的类型化进度。
type SagaProgress struct {
	Plan    *SagaPlan    `json:"plan,omitempty"`
	Receipt *SagaReceipt `json:"receipt,omitempty"`
}

// SagaJournal 在副作用前后同步持久化补偿事务状态。
type SagaJournal interface {
	BindSaga(SagaPlan) (SagaSnapshot, error)
	RecordSaga(SagaReceipt) (SagaSnapshot, error)
}

// PlanForSteps 生成执行器与中台共同校验的确定性计划摘要。
func PlanForSteps(steps []Step) (SagaPlan, error) {
	return buildSagaPlan(steps)
}

// ExecuteRecoverable 执行以持久快照为真相的补偿事务。
func ExecuteRecoverable(ctx context.Context, steps []Step, sandbox bool, journal SagaJournal, rec *RunRecord, limits ...ResourceLimit) error {
	if journal == nil {
		return fmt.Errorf("failed_precondition: saga journal is required")
	}
	if rec == nil {
		rec = &RunRecord{}
	}
	plan, err := buildSagaPlan(steps)
	if err != nil {
		return err
	}
	snapshot, err := journal.BindSaga(plan)
	if err != nil {
		return fmt.Errorf("bind saga plan: %w", err)
	}
	if snapshot.PlanDigest != plan.PlanDigest {
		return fmt.Errorf("failed_precondition: saga plan digest mismatch")
	}
	if snapshot.State == "outcome_unknown" {
		return ErrOutcomeUnknown
	}
	if snapshot.CancelRequested {
		return compensateRecoverable(ctx, steps, journal, snapshot, len(steps)-1, context.Canceled, rec)
	}

	var lim ResourceLimit
	if len(limits) > 0 {
		lim = limits[0]
	}
	for i, step := range steps {
		state, err := sagaStepAt(snapshot, i, step.Name)
		if err != nil {
			return err
		}
		switch state.ActionPhase {
		case PhaseActionSucceeded:
			continue
		case PhaseActionFailed:
			last := i
			if !state.ActionEffectStarted {
				last = i - 1
			}
			return compensateRecoverable(ctx, steps, journal, snapshot, last, errors.New(firstNonEmpty(state.Error, "step failed")), rec)
		case PhaseOutcomeUnknown:
			return ErrOutcomeUnknown
		case PhaseActionEffect:
			if state.Plan.ActionReplay == ReplayNever {
				return markOutcomeUnknown(journal, state.Plan, "action settlement missing after effect started")
			}
		}

		guardDigest := stepGuardDigest(step, sandbox, lim)
		if state.GuardDigest != "" && state.GuardDigest != guardDigest {
			return fmt.Errorf("failed_precondition: guard digest changed for step %s", step.Name)
		}
		if ctx.Err() != nil {
			return compensateRecoverable(ctx, steps, journal, snapshot, i-1, ctx.Err(), rec)
		}
		if state.ActionPhase == "" {
			snapshot, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseActionIntent, GuardDigest: guardDigest})
			if err != nil {
				return fmt.Errorf("record action intent: %w", err)
			}
			if snapshot.CancelRequested {
				return compensateRecoverable(ctx, steps, journal, snapshot, i-1, context.Canceled, rec)
			}
		}

		guardErr := validateStepGuard(step, sandbox, lim)
		if guardErr != nil {
			rec.Events = append(rec.Events, "reject:"+step.Name)
			snapshot, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseActionFailed, GuardDigest: guardDigest, Error: guardErr.Error()})
			if err != nil {
				return fmt.Errorf("record guard failure: %w", err)
			}
			return compensateRecoverable(ctx, steps, journal, snapshot, i-1, guardErr, rec)
		}
		if state.ActionPhase != PhaseActionEffect {
			snapshot, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseActionEffect, GuardDigest: guardDigest})
			if err != nil {
				return fmt.Errorf("record action effect boundary: %w", err)
			}
		}
		rec.Events = append(rec.Events, "start:"+step.Name)
		runErr := error(nil)
		if step.Fail {
			runErr = fmt.Errorf("step %s failed", step.Name)
		} else if step.Run != nil {
			runErr = step.Run(ctx)
		}
		if runErr != nil {
			rec.Events = append(rec.Events, "fail:"+step.Name)
			snapshot, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseActionFailed, GuardDigest: guardDigest, Error: runErr.Error()})
			if err != nil {
				return fmt.Errorf("record action failure: %w", err)
			}
			return compensateRecoverable(ctx, steps, journal, snapshot, i, runErr, rec)
		}
		rec.Events = append(rec.Events, "ok:"+step.Name)
		snapshot, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseActionSucceeded, GuardDigest: guardDigest, ReceiptRef: "ok"})
		if err != nil {
			return fmt.Errorf("record action settlement: %w", err)
		}
		if snapshot.CancelRequested {
			return compensateRecoverable(ctx, steps, journal, snapshot, i, context.Canceled, rec)
		}
	}
	return nil
}

func compensateRecoverable(ctx context.Context, steps []Step, journal SagaJournal, snapshot SagaSnapshot, last int, cause error, rec *RunRecord) error {
	compCtx := context.WithoutCancel(ctx)
	for i := last; i >= 0; i-- {
		step := steps[i]
		state, err := sagaStepAt(snapshot, i, step.Name)
		if err != nil {
			return err
		}
		if step.Compensate == nil || !state.ActionEffectStarted {
			continue
		}
		if state.ActionPhase == PhaseActionEffect && state.Plan.ActionReplay == ReplayNever {
			return markOutcomeUnknown(journal, state.Plan, "action settlement missing after effect started")
		}
		switch state.CompensationPhase {
		case PhaseCompensated:
			continue
		case PhaseOutcomeUnknown:
			return ErrOutcomeUnknown
		case PhaseCompensationEffect, PhaseCompensationFailed:
			if state.Plan.CompensationReplay == ReplayNever {
				return markOutcomeUnknown(journal, state.Plan, "compensation settlement missing or failed after effect started")
			}
		}
		if state.CompensationPhase == "" || state.CompensationPhase == PhaseCompensationFailed {
			_, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseCompensationIntent})
			if err != nil {
				return fmt.Errorf("record compensation intent: %w", err)
			}
		}
		if state.CompensationPhase != PhaseCompensationEffect {
			_, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseCompensationEffect})
			if err != nil {
				return fmt.Errorf("record compensation effect boundary: %w", err)
			}
		}
		rec.Events = append(rec.Events, "compensate:"+step.Name)
		if err := step.Compensate(compCtx); err != nil {
			_, recordErr := journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseCompensationFailed, Error: err.Error()})
			if recordErr != nil {
				return fmt.Errorf("%w: record compensation failure: %v", cause, recordErr)
			}
			if state.Plan.CompensationReplay == ReplayNever {
				return markOutcomeUnknown(journal, state.Plan, err.Error())
			}
			return fmt.Errorf("%w: compensate %s: %v", cause, step.Name, err)
		}
		snapshot, err = journal.RecordSaga(SagaReceipt{Sequence: int32(i + 1), StepKey: step.Name, Phase: PhaseCompensated, ReceiptRef: "ok"})
		if err != nil {
			return fmt.Errorf("record compensation settlement: %w", err)
		}
	}
	return cause
}

func buildSagaPlan(steps []Step) (SagaPlan, error) {
	plan := SagaPlan{SchemaVersion: sagaSchemaVersion, Steps: make([]SagaStepPlan, 0, len(steps))}
	seen := map[string]bool{}
	for i, step := range steps {
		if step.Name == "" || seen[step.Name] {
			return SagaPlan{}, fmt.Errorf("invalid saga step key")
		}
		seen[step.Name] = true
		actionReplay := normalizeReplay(step.Replay)
		compensationReplay := normalizeReplay(step.CompensationReplay)
		plan.Steps = append(plan.Steps, SagaStepPlan{
			Sequence: int32(i + 1), StepKey: step.Name, ActionReplay: actionReplay,
			HasCompensation: step.Compensate != nil, CompensationReplay: compensationReplay,
		})
	}
	raw, err := json.Marshal(struct {
		SchemaVersion string         `json:"schema_version"`
		Steps         []SagaStepPlan `json:"steps"`
	}{plan.SchemaVersion, plan.Steps})
	if err != nil {
		return SagaPlan{}, err
	}
	sum := sha256.Sum256(raw)
	plan.PlanDigest = "sha256:" + hex.EncodeToString(sum[:])
	return plan, nil
}

func normalizeReplay(policy ReplayPolicy) ReplayPolicy {
	switch policy {
	case ReplaySafe, ReplayIdempotent, ReplayNever:
		return policy
	default:
		return ReplayNever
	}
}

func sagaStepAt(snapshot SagaSnapshot, index int, name string) (SagaStepSnapshot, error) {
	if index < 0 || index >= len(snapshot.Steps) {
		return SagaStepSnapshot{}, fmt.Errorf("failed_precondition: saga snapshot is incomplete")
	}
	step := snapshot.Steps[index]
	if step.Plan.Sequence != int32(index+1) || step.Plan.StepKey != name {
		return SagaStepSnapshot{}, fmt.Errorf("failed_precondition: saga snapshot step mismatch")
	}
	return step, nil
}

func stepGuardDigest(step Step, sandbox bool, lim ResourceLimit) string {
	raw, _ := json.Marshal(struct {
		Dangerous bool          `json:"dangerous"`
		Fail      bool          `json:"fail"`
		MemBytes  uint64        `json:"mem_bytes"`
		Sandbox   bool          `json:"sandbox"`
		Limit     ResourceLimit `json:"limit"`
	}{step.Dangerous, step.Fail, step.MemBytes, sandbox, lim})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateStepGuard(step Step, sandbox bool, lim ResourceLimit) error {
	if step.Dangerous && !sandbox {
		return fmt.Errorf("dangerous step %s: failed_precondition", step.Name)
	}
	if ExceedsLimit(step.MemBytes, lim.MemoryBytes) {
		return fmt.Errorf("resource_exhausted")
	}
	if err := step.Budget.Consume(); err != nil {
		return err
	}
	return nil
}

func markOutcomeUnknown(journal SagaJournal, step SagaStepPlan, reason string) error {
	_, err := journal.RecordSaga(SagaReceipt{Sequence: step.Sequence, StepKey: step.StepKey, Phase: PhaseOutcomeUnknown, Error: reason})
	if err != nil {
		return fmt.Errorf("record outcome unknown: %w", err)
	}
	return ErrOutcomeUnknown
}
