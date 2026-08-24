package brain

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/agents/runtime"

	agentv1 "yufeng/proto/gen/agentv1"
	runv1 "yufeng/proto/gen/runv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestCancelRunWaitsForPersistedCompensation(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, privateKey := seedRunOperator(t, ctx, st.Pool())
	runs, worker, workerToken := createSagaRunHarness(t, ctx, st.Pool(), userToken, privateKey)
	runID, work := createAndLeaseSagaRun(t, ctx, runs, worker, userToken, workerToken, "cancel-compensation")

	steps := []runtime.Step{
		{Name: "prepare", Replay: runtime.ReplaySafe, CompensationReplay: runtime.ReplayIdempotent, Compensate: func(context.Context) error { return nil }},
		{Name: "apply", Replay: runtime.ReplayIdempotent, CompensationReplay: runtime.ReplayIdempotent, Compensate: func(context.Context) error { return nil }},
	}
	journal := workerSagaJournal{server: worker, token: workerToken, work: work}
	plan, err := runtime.PlanForSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.BindSaga(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.RecordSaga(runtime.SagaReceipt{Sequence: 1, StepKey: "prepare", Phase: runtime.PhaseActionSucceeded}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("out-of-order settlement must fail, got %v", err)
	}
	for i, name := range []string{"prepare", "apply"} {
		sequence := int32(i + 1)
		for _, phase := range []runtime.SagaPhase{runtime.PhaseActionIntent, runtime.PhaseActionEffect, runtime.PhaseActionSucceeded} {
			if _, err := journal.RecordSaga(runtime.SagaReceipt{Sequence: sequence, StepKey: name, Phase: phase, GuardDigest: "sha256:guard", ReceiptRef: "ok"}); err != nil {
				t.Fatal(err)
			}
		}
	}

	cancel := connect.NewRequest(&runv1.CancelRunRequest{RunId: runID})
	cancel.Header().Set("Authorization", "Bearer "+userToken)
	cancelled, err := runs.CancelRun(ctx, cancel)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Msg.GetRun().GetState() != "cancelling" {
		t.Fatalf("state=%s", cancelled.Msg.GetRun().GetState())
	}
	extend := connect.NewRequest(&workerv1.ExtendLeaseRequest{WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch()})
	extend.Header().Set("Authorization", "Bearer "+workerToken)
	extended, err := worker.ExtendLease(ctx, extend)
	if err != nil {
		t.Fatal(err)
	}
	if !extended.Msg.GetCancelRequested() {
		t.Fatal("lease extension must carry persisted cancellation")
	}
	early := connect.NewRequest(&workerv1.FailWorkRequest{WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(), ErrorCode: "cancelled", Message: "cancelled before compensation"})
	early.Header().Set("Authorization", "Bearer "+workerToken)
	if _, err := worker.FailWork(ctx, early); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("terminal cancellation before compensation must fail, got %v", err)
	}
	for i := len(steps) - 1; i >= 0; i-- {
		for _, phase := range []runtime.SagaPhase{runtime.PhaseCompensationIntent, runtime.PhaseCompensationEffect, runtime.PhaseCompensated} {
			if _, err := journal.RecordSaga(runtime.SagaReceipt{Sequence: int32(i + 1), StepKey: steps[i].Name, Phase: phase, ReceiptRef: "ok"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	fail := connect.NewRequest(&workerv1.FailWorkRequest{WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(), ErrorCode: "cancelled", Message: "cancelled after compensation"})
	fail.Header().Set("Authorization", "Bearer "+workerToken)
	if _, err := worker.FailWork(ctx, fail); err != nil {
		t.Fatal(err)
	}
	assertRunState(t, ctx, runs, userToken, runID, "cancelled")
}

func TestRunCanFailBeforeSagaPlanBinds(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, privateKey := seedRunOperator(t, ctx, st.Pool())
	runs, worker, workerToken := createSagaRunHarness(t, ctx, st.Pool(), userToken, privateKey)
	runID, work := createAndLeaseSagaRun(t, ctx, runs, worker, userToken, workerToken, "startup-failure")

	req := connect.NewRequest(&workerv1.FailWorkRequest{
		WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(),
		ErrorCode: "run_failed", Message: "process failed before plan bind",
	})
	req.Header().Set("Authorization", "Bearer "+workerToken)
	if _, err := worker.FailWork(ctx, req); err != nil {
		t.Fatal(err)
	}
	assertRunState(t, ctx, runs, userToken, runID, "failed")
	var sagaState, planDigest string
	if err := st.Pool().QueryRow(ctx, `SELECT state, plan_digest FROM run_sagas WHERE run_id=$1`, runID).Scan(&sagaState, &planDigest); err != nil {
		t.Fatal(err)
	}
	if sagaState != "compensated" || planDigest != "" {
		t.Fatalf("saga state=%q plan_digest=%q", sagaState, planDigest)
	}
}

func TestReclaimedRunDoesNotRepeatNeverReplayCompensation(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, privateKey := seedRunOperator(t, ctx, st.Pool())
	runs, worker, workerToken := createSagaRunHarness(t, ctx, st.Pool(), userToken, privateKey)
	runID, first := createAndLeaseSagaRun(t, ctx, runs, worker, userToken, workerToken, "crash-compensation")
	compensations := 0
	steps := []runtime.Step{{Name: "apply", Replay: runtime.ReplayIdempotent, CompensationReplay: runtime.ReplayNever,
		Compensate: func(context.Context) error { compensations++; return nil }}}
	journal := workerSagaJournal{server: worker, token: workerToken, work: first}
	plan, err := runtime.PlanForSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.BindSaga(plan); err != nil {
		t.Fatal(err)
	}
	for _, receipt := range []runtime.SagaReceipt{
		{Sequence: 1, StepKey: "apply", Phase: runtime.PhaseActionIntent, GuardDigest: "sha256:guard"},
		{Sequence: 1, StepKey: "apply", Phase: runtime.PhaseActionEffect, GuardDigest: "sha256:guard"},
		{Sequence: 1, StepKey: "apply", Phase: runtime.PhaseActionFailed, GuardDigest: "sha256:guard", Error: "apply failed"},
		{Sequence: 1, StepKey: "apply", Phase: runtime.PhaseCompensationIntent},
		{Sequence: 1, StepKey: "apply", Phase: runtime.PhaseCompensationEffect},
	} {
		if _, err := journal.RecordSaga(receipt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE work_items SET lease_deadline=now()-interval '1 second' WHERE work_id=$1`, first.GetWorkId()); err != nil {
		t.Fatal(err)
	}
	second := pollSagaWork(t, ctx, worker, workerToken)
	if second.GetLeaseEpoch() <= first.GetLeaseEpoch() || second.GetSagaSnapshot().GetSteps()[0].GetCompensationPhase() != workerv1.RunStepPhase_RUN_STEP_PHASE_COMPENSATION_EFFECT_STARTED {
		t.Fatalf("reclaimed work lost recovery fence: first=%d second=%d snapshot=%+v", first.GetLeaseEpoch(), second.GetLeaseEpoch(), second.GetSagaSnapshot())
	}
	journal.work = second
	err = runtime.ExecuteRecoverable(ctx, steps, false, journal, &runtime.RunRecord{})
	if !errors.Is(err, runtime.ErrOutcomeUnknown) || compensations != 0 {
		t.Fatalf("err=%v compensations=%d", err, compensations)
	}
	fail := connect.NewRequest(&workerv1.FailWorkRequest{WorkId: second.GetWorkId(), LeaseId: second.GetLeaseId(), LeaseEpoch: second.GetLeaseEpoch(), ErrorCode: "outcome_unknown", Message: "compensation outcome unknown"})
	fail.Header().Set("Authorization", "Bearer "+workerToken)
	if _, err := worker.FailWork(ctx, fail); err != nil {
		t.Fatal(err)
	}
	assertRunState(t, ctx, runs, userToken, runID, "outcome_unknown")
}

type workerSagaJournal struct {
	server *WorkerServer
	token  string
	work   *workerv1.WorkItem
	ctx    context.Context
}

func (j workerSagaJournal) BindSaga(plan runtime.SagaPlan) (runtime.SagaSnapshot, error) {
	return j.report(runtime.SagaProgress{Plan: &plan})
}

func (j workerSagaJournal) RecordSaga(receipt runtime.SagaReceipt) (runtime.SagaSnapshot, error) {
	return j.report(runtime.SagaProgress{Receipt: &receipt})
}

func (j workerSagaJournal) report(progress runtime.SagaProgress) (runtime.SagaSnapshot, error) {
	req := connect.NewRequest(&workerv1.ReportProgressRequest{WorkId: j.work.GetWorkId(), LeaseId: j.work.GetLeaseId(), LeaseEpoch: j.work.GetLeaseEpoch()})
	if progress.Plan != nil {
		req.Msg.SagaPlan = runtime.SagaPlanToProto(*progress.Plan)
	}
	if progress.Receipt != nil {
		req.Msg.SagaReceipt = runtime.SagaReceiptToProto(*progress.Receipt)
	}
	req.Header().Set("Authorization", "Bearer "+j.token)
	ctx := j.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := j.server.ReportProgress(ctx, req)
	if err != nil {
		return runtime.SagaSnapshot{}, err
	}
	return runtime.SagaSnapshotFromProto(resp.Msg.GetSagaSnapshot()), nil
}

func createSagaRunHarness(t *testing.T, ctx context.Context, pool *pgxpool.Pool, _ string, privateKey ed25519.PrivateKey) (*RunServer, *WorkerServer, string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE work_items SET status='failed' WHERE status IN ('pending','leased')`); err != nil {
		t.Fatal(err)
	}
	workerID := "worker-saga-" + newTestSuffix()
	bootstrap := "boot-saga-" + newTestSuffix()
	agents := NewAgentServer(pool, bootstrap, privateKey)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: workerID, BootstrapToken: bootstrap, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]'::jsonb,'[{"kind":"asset","id":"any"}]'::jsonb,'test')`, "g-saga-"+newTestSuffix(), workerID); err != nil {
		t.Fatal(err)
	}
	worker := NewWorkerServer(pool, privateKey, true)
	reg := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: workerID})
	reg.Header().Set("Authorization", "Bearer "+registered.Msg.GetAccessToken())
	if _, err := worker.RegisterWorker(ctx, reg); err != nil {
		t.Fatal(err)
	}
	return NewRunServer(pool, privateKey), worker, registered.Msg.GetAccessToken()
}

func createAndLeaseSagaRun(t *testing.T, ctx context.Context, runs *RunServer, worker *WorkerServer, userToken, workerToken, planRef string) (string, *workerv1.WorkItem) {
	t.Helper()
	create := connect.NewRequest(&runv1.CreateRunRequest{PlanRef: planRef, Toolset: []string{"ping"}, Bindings: []string{"asset:any"}, Budget: "8", Ttl: "30s"})
	create.Header().Set("Authorization", "Bearer "+userToken)
	created, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	return created.Msg.GetRunId(), pollSagaWork(t, ctx, worker, workerToken)
}

func pollSagaWork(t *testing.T, ctx context.Context, worker *WorkerServer, workerToken string) *workerv1.WorkItem {
	t.Helper()
	poll := connect.NewRequest(&workerv1.PollWorkRequest{LongPollSeconds: 1})
	poll.Header().Set("Authorization", "Bearer "+workerToken)
	resp, err := worker.PollWork(ctx, poll)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetWork() == nil {
		t.Fatal("expected leased work")
	}
	return resp.Msg.GetWork()
}

func assertRunState(t *testing.T, ctx context.Context, runs *RunServer, token, runID, want string) {
	t.Helper()
	get := connect.NewRequest(&runv1.GetRunRequest{RunId: runID})
	get.Header().Set("Authorization", "Bearer "+token)
	resp, err := runs.GetRun(ctx, get)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetRun().GetState() != want {
		t.Fatalf("state=%s want=%s", resp.Msg.GetRun().GetState(), want)
	}
}

func settleSuccessfulSaga(t *testing.T, worker *WorkerServer, workerToken string, work *workerv1.WorkItem, contexts ...context.Context) {
	t.Helper()
	steps := []runtime.Step{{Name: "verify", Replay: runtime.ReplaySafe}}
	plan, err := runtime.PlanForSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	journal := workerSagaJournal{server: worker, token: workerToken, work: work}
	if len(contexts) > 0 {
		journal.ctx = contexts[0]
	}
	if _, err := journal.BindSaga(plan); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []runtime.SagaPhase{runtime.PhaseActionIntent, runtime.PhaseActionEffect, runtime.PhaseActionSucceeded} {
		if _, err := journal.RecordSaga(runtime.SagaReceipt{Sequence: 1, StepKey: "verify", Phase: phase, GuardDigest: "sha256:guard", ReceiptRef: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
}
