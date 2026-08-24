package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type testSagaJournal struct {
	mu       sync.Mutex
	snapshot SagaSnapshot
	receipts []SagaReceipt
}

func (j *testSagaJournal) BindSaga(plan SagaPlan) (SagaSnapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.snapshot.PlanDigest != "" && j.snapshot.PlanDigest != plan.PlanDigest {
		return SagaSnapshot{}, errors.New("plan mismatch")
	}
	if j.snapshot.PlanDigest == "" {
		j.snapshot = SagaSnapshot{State: "running", PlanDigest: plan.PlanDigest, Steps: make([]SagaStepSnapshot, 0, len(plan.Steps))}
		for _, step := range plan.Steps {
			j.snapshot.Steps = append(j.snapshot.Steps, SagaStepSnapshot{Plan: step})
		}
	}
	return cloneTestSnapshot(j.snapshot), nil
}

func (j *testSagaJournal) RecordSaga(receipt SagaReceipt) (SagaSnapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if receipt.Sequence <= 0 || int(receipt.Sequence) > len(j.snapshot.Steps) {
		return SagaSnapshot{}, errors.New("step out of range")
	}
	step := &j.snapshot.Steps[receipt.Sequence-1]
	if step.Plan.StepKey != receipt.StepKey {
		return SagaSnapshot{}, errors.New("step mismatch")
	}
	j.receipts = append(j.receipts, receipt)
	if receipt.GuardDigest != "" {
		step.GuardDigest = receipt.GuardDigest
	}
	if receipt.Error != "" {
		step.Error = receipt.Error
		j.snapshot.Cause = receipt.Error
	}
	switch receipt.Phase {
	case PhaseActionIntent, PhaseActionEffect, PhaseActionSucceeded, PhaseActionFailed:
		step.ActionPhase = receipt.Phase
		if receipt.Phase == PhaseActionEffect {
			step.ActionEffectStarted = true
		}
	case PhaseCompensationIntent, PhaseCompensationEffect, PhaseCompensated, PhaseCompensationFailed:
		step.CompensationPhase = receipt.Phase
	case PhaseOutcomeUnknown:
		if step.CompensationPhase == PhaseCompensationEffect || step.CompensationPhase == PhaseCompensationFailed {
			step.CompensationPhase = receipt.Phase
		} else {
			step.ActionPhase = receipt.Phase
		}
		j.snapshot.State = "outcome_unknown"
	}
	if j.snapshot.State != "outcome_unknown" {
		j.refreshState()
	}
	return cloneTestSnapshot(j.snapshot), nil
}

func (j *testSagaJournal) refreshState() {
	allSucceeded := true
	failed := false
	needsCompensation := false
	compensated := true
	for _, step := range j.snapshot.Steps {
		if step.ActionPhase != PhaseActionSucceeded {
			allSucceeded = false
		}
		if step.ActionPhase == PhaseActionFailed {
			failed = true
		}
		if step.Plan.HasCompensation && step.ActionEffectStarted {
			needsCompensation = true
			if step.CompensationPhase != PhaseCompensated {
				compensated = false
			}
		}
	}
	switch {
	case j.snapshot.CancelRequested && (!needsCompensation || compensated):
		j.snapshot.State = "compensated"
	case j.snapshot.CancelRequested:
		j.snapshot.State = "cancelling"
	case failed && (!needsCompensation || compensated):
		j.snapshot.State = "compensated"
	case failed:
		j.snapshot.State = "compensating"
	case allSucceeded:
		j.snapshot.State = "ready"
	default:
		j.snapshot.State = "running"
	}
}

func cloneTestSnapshot(in SagaSnapshot) SagaSnapshot {
	out := in
	out.Steps = append([]SagaStepSnapshot(nil), in.Steps...)
	return out
}

func TestExecuteRecoverablePersistsIntentAndReverseCompensation(t *testing.T) {
	journal := &testSagaJournal{}
	var effects []string
	steps := []Step{
		{Name: "prepare", Replay: ReplaySafe, CompensationReplay: ReplayIdempotent,
			Run:        func(context.Context) error { effects = append(effects, "prepare"); return nil },
			Compensate: func(context.Context) error { effects = append(effects, "undo-prepare"); return nil }},
		{Name: "apply", Replay: ReplayIdempotent, CompensationReplay: ReplayIdempotent,
			Run:        func(context.Context) error { effects = append(effects, "apply"); return errors.New("apply failed") },
			Compensate: func(context.Context) error { effects = append(effects, "undo-apply"); return nil }},
	}
	err := ExecuteRecoverable(context.Background(), steps, false, journal, &RunRecord{})
	if err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("got %v", err)
	}
	if got := strings.Join(effects, ","); got != "prepare,apply,undo-apply,undo-prepare" {
		t.Fatalf("effects=%s", got)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	gotPhases := make([]string, 0, len(journal.receipts))
	for i, receipt := range journal.receipts {
		gotPhases = append(gotPhases, string(receipt.Phase)+":"+receipt.StepKey)
		if receipt.Phase == PhaseActionEffect || receipt.Phase == PhaseCompensationEffect {
			if i == 0 || (journal.receipts[i-1].Phase != PhaseActionIntent && journal.receipts[i-1].Phase != PhaseCompensationIntent) {
				t.Fatalf("effect boundary lacks preceding intent: %+v", journal.receipts)
			}
		}
	}
	wantPhases := strings.Join([]string{
		"action_intent_recorded:prepare", "action_effect_started:prepare", "action_succeeded:prepare",
		"action_intent_recorded:apply", "action_effect_started:apply", "action_failed:apply",
		"compensation_intent_recorded:apply", "compensation_effect_started:apply", "compensated:apply",
		"compensation_intent_recorded:prepare", "compensation_effect_started:prepare", "compensated:prepare",
	}, ",")
	if strings.Join(gotPhases, ",") != wantPhases {
		t.Fatalf("receipts=%v", gotPhases)
	}
}

func TestExecuteRecoverableCancelUsesCompensationBranch(t *testing.T) {
	journal := &testSagaJournal{}
	plan, err := buildSagaPlan([]Step{{Name: "prepare", Replay: ReplaySafe, CompensationReplay: ReplayIdempotent, Compensate: func(context.Context) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := journal.BindSaga(plan)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Steps[0].ActionPhase = PhaseActionSucceeded
	snapshot.Steps[0].ActionEffectStarted = true
	snapshot.CancelRequested = true
	journal.snapshot = snapshot
	compensated := 0
	err = ExecuteRecoverable(context.Background(), []Step{{Name: "prepare", Replay: ReplaySafe, CompensationReplay: ReplayIdempotent,
		Compensate: func(context.Context) error { compensated++; return nil }}}, false, journal, &RunRecord{})
	if !errors.Is(err, context.Canceled) || compensated != 1 {
		t.Fatalf("err=%v compensated=%d", err, compensated)
	}
}

func TestExecuteRecoverableNeverReplaysUnknownAction(t *testing.T) {
	runs := 0
	steps := []Step{{Name: "apply", Replay: ReplayNever, Run: func(context.Context) error { runs++; return nil }}}
	plan, err := buildSagaPlan(steps)
	if err != nil {
		t.Fatal(err)
	}
	journal := &testSagaJournal{snapshot: SagaSnapshot{State: "running", PlanDigest: plan.PlanDigest,
		Steps: []SagaStepSnapshot{{Plan: plan.Steps[0], ActionPhase: PhaseActionEffect, ActionEffectStarted: true}}}}
	err = ExecuteRecoverable(context.Background(), steps, false, journal, &RunRecord{})
	if !errors.Is(err, ErrOutcomeUnknown) || runs != 0 || journal.snapshot.State != "outcome_unknown" {
		t.Fatalf("err=%v runs=%d state=%s", err, runs, journal.snapshot.State)
	}
}

func TestExecuteRecoverableResumesIdempotentCompensation(t *testing.T) {
	compensations := 0
	steps := []Step{{Name: "apply", Replay: ReplayIdempotent, CompensationReplay: ReplayIdempotent,
		Compensate: func(context.Context) error { compensations++; return nil }}}
	plan, err := buildSagaPlan(steps)
	if err != nil {
		t.Fatal(err)
	}
	journal := &testSagaJournal{snapshot: SagaSnapshot{State: "compensating", PlanDigest: plan.PlanDigest,
		Steps: []SagaStepSnapshot{{Plan: plan.Steps[0], ActionPhase: PhaseActionFailed, CompensationPhase: PhaseCompensationEffect, Error: "failed", ActionEffectStarted: true}}}}
	err = ExecuteRecoverable(context.Background(), steps, false, journal, &RunRecord{})
	if err == nil || compensations != 1 || journal.snapshot.Steps[0].CompensationPhase != PhaseCompensated {
		t.Fatalf("err=%v compensations=%d snapshot=%+v", err, compensations, journal.snapshot)
	}
}

func TestExecuteRecoverableDoesNotRepeatNeverReplayCompensation(t *testing.T) {
	compensations := 0
	steps := []Step{{Name: "apply", Replay: ReplayIdempotent, CompensationReplay: ReplayNever,
		Compensate: func(context.Context) error { compensations++; return nil }}}
	plan, err := buildSagaPlan(steps)
	if err != nil {
		t.Fatal(err)
	}
	journal := &testSagaJournal{snapshot: SagaSnapshot{State: "compensating", PlanDigest: plan.PlanDigest,
		Steps: []SagaStepSnapshot{{Plan: plan.Steps[0], ActionPhase: PhaseActionFailed, CompensationPhase: PhaseCompensationEffect, Error: "failed", ActionEffectStarted: true}}}}
	err = ExecuteRecoverable(context.Background(), steps, false, journal, &RunRecord{})
	if !errors.Is(err, ErrOutcomeUnknown) || compensations != 0 || journal.snapshot.State != "outcome_unknown" {
		t.Fatalf("err=%v compensations=%d state=%s", err, compensations, journal.snapshot.State)
	}
}

func TestExecuteRecoverableGuardFailureDoesNotCompensateUnstartedStep(t *testing.T) {
	journal := &testSagaJournal{}
	var compensated []string
	err := ExecuteRecoverable(context.Background(), []Step{
		{Name: "prepare", Replay: ReplaySafe, CompensationReplay: ReplayIdempotent,
			Compensate: func(context.Context) error { compensated = append(compensated, "prepare"); return nil }},
		{Name: "dangerous", Dangerous: true, Replay: ReplayNever, CompensationReplay: ReplayIdempotent,
			Compensate: func(context.Context) error { compensated = append(compensated, "dangerous"); return nil }},
	}, false, journal, &RunRecord{})
	if err == nil || strings.Join(compensated, ",") != "prepare" || journal.snapshot.State != "compensated" {
		t.Fatalf("err=%v compensated=%v state=%s", err, compensated, journal.snapshot.State)
	}
}
