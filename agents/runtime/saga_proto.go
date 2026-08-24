package runtime

import workerv1 "yufeng/proto/gen/workerv1"

// SagaSnapshotFromProto 把网络恢复投影收敛成本地执行类型。
func SagaSnapshotFromProto(in *workerv1.RunSagaSnapshot) SagaSnapshot {
	if in == nil {
		return SagaSnapshot{}
	}
	out := SagaSnapshot{
		State: in.GetState(), PlanDigest: in.GetPlanDigest(), CancelRequested: in.GetCancelRequested(), Cause: in.GetCause(),
		Steps: make([]SagaStepSnapshot, 0, len(in.GetSteps())),
	}
	for _, step := range in.GetSteps() {
		if step == nil || step.GetPlan() == nil {
			continue
		}
		out.Steps = append(out.Steps, SagaStepSnapshot{
			Plan: SagaStepPlan{
				Sequence: step.GetPlan().GetSequence(), StepKey: step.GetPlan().GetStepKey(),
				ActionReplay:       replayFromProto(step.GetPlan().GetActionReplay()),
				HasCompensation:    step.GetPlan().GetHasCompensation(),
				CompensationReplay: replayFromProto(step.GetPlan().GetCompensationReplay()),
			},
			ActionPhase: phaseFromProto(step.GetActionPhase()), CompensationPhase: phaseFromProto(step.GetCompensationPhase()),
			GuardDigest: step.GetGuardDigest(), ActionReceiptRef: step.GetActionReceiptRef(),
			CompensationReceiptRef: step.GetCompensationReceiptRef(), Error: step.GetError(),
			ActionEffectStarted: step.GetActionEffectStarted(),
		})
	}
	return out
}

// SagaPlanToProto 把本地固定计划编码为类型化网络契约。
func SagaPlanToProto(in SagaPlan) *workerv1.RunSagaPlan {
	out := &workerv1.RunSagaPlan{SchemaVersion: in.SchemaVersion, PlanDigest: in.PlanDigest, Steps: make([]*workerv1.RunSagaStepPlan, 0, len(in.Steps))}
	for _, step := range in.Steps {
		out.Steps = append(out.Steps, &workerv1.RunSagaStepPlan{
			Sequence: step.Sequence, StepKey: step.StepKey, ActionReplay: replayToProto(step.ActionReplay),
			HasCompensation: step.HasCompensation, CompensationReplay: replayToProto(step.CompensationReplay),
		})
	}
	return out
}

// SagaReceiptToProto 把本地步骤回执编码为类型化网络契约。
func SagaReceiptToProto(in SagaReceipt) *workerv1.RunSagaReceipt {
	return &workerv1.RunSagaReceipt{
		Sequence: in.Sequence, StepKey: in.StepKey, Phase: phaseToProto(in.Phase), GuardDigest: in.GuardDigest,
		ReceiptRef: in.ReceiptRef, Error: in.Error,
	}
}

func replayFromProto(in workerv1.RunStepReplayPolicy) ReplayPolicy {
	switch in {
	case workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_SAFE:
		return ReplaySafe
	case workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_IDEMPOTENT:
		return ReplayIdempotent
	case workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_NEVER_REPLAY:
		return ReplayNever
	default:
		return ""
	}
}

func replayToProto(in ReplayPolicy) workerv1.RunStepReplayPolicy {
	switch in {
	case ReplaySafe:
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_SAFE
	case ReplayIdempotent:
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_IDEMPOTENT
	case ReplayNever:
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_NEVER_REPLAY
	default:
		return workerv1.RunStepReplayPolicy_RUN_STEP_REPLAY_POLICY_UNSPECIFIED
	}
}

func phaseFromProto(in workerv1.RunStepPhase) SagaPhase {
	switch in {
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_INTENT_RECORDED:
		return PhaseActionIntent
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_EFFECT_STARTED:
		return PhaseActionEffect
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_SUCCEEDED:
		return PhaseActionSucceeded
	case workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_FAILED:
		return PhaseActionFailed
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_INTENT_RECORDED:
		return PhaseCompensationIntent
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_EFFECT_STARTED:
		return PhaseCompensationEffect
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATED:
		return PhaseCompensated
	case workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_FAILED:
		return PhaseCompensationFailed
	case workerv1.RunStepPhase_RUN_STEP_PHASE_OUTCOME_UNKNOWN:
		return PhaseOutcomeUnknown
	default:
		return ""
	}
}

func phaseToProto(in SagaPhase) workerv1.RunStepPhase {
	switch in {
	case PhaseActionIntent:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_INTENT_RECORDED
	case PhaseActionEffect:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_EFFECT_STARTED
	case PhaseActionSucceeded:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_SUCCEEDED
	case PhaseActionFailed:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_ACTION_FAILED
	case PhaseCompensationIntent:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_INTENT_RECORDED
	case PhaseCompensationEffect:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_EFFECT_STARTED
	case PhaseCompensated:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATED
	case PhaseCompensationFailed:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_FAILED
	case PhaseOutcomeUnknown:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_OUTCOME_UNKNOWN
	default:
		return workerv1.RunStepPhase_RUN_STEP_PHASE_UNSPECIFIED
	}
}
