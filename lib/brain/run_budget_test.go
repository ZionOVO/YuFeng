package brain

import (
	"context"
	"crypto/ed25519"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	agentv1 "yufeng/proto/gen/agentv1"
	authv1 "yufeng/proto/gen/authv1"
	modelv1 "yufeng/proto/gen/modelv1"
	runv1 "yufeng/proto/gen/runv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestRunBudgetPersistsAcrossRenewalAndReclaim(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, key := seedRunOperator(t, ctx, st.Pool())
	runs := NewRunServer(st.Pool(), key)
	create := connect.NewRequest(&runv1.CreateRunRequest{
		PlanRef: "plan-budget", Toolset: []string{"model.generate"}, Bindings: []string{"asset:any"},
		Budget: "2", Ttl: "30s",
	})
	create.Header().Set("Authorization", "Bearer "+userToken)
	created, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	workerID := "worker-budget-" + newTestSuffix()
	workerToken := registerBudgetRunWorker(t, ctx, st.Pool(), key, workerID)
	workers := NewWorkerServer(st.Pool(), key, true)
	first := pollBudgetWork(t, ctx, workers, workerID, workerToken)
	if first.GetBudgetSnapshot().GetLimits().GetMaxSteps() != 2 ||
		first.GetBudgetSnapshot().GetUsage().GetStepsReserved() != 1 || first.GetExecutionDeadline() == nil {
		t.Fatalf("first budget=%+v", first.GetBudgetSnapshot())
	}
	extend := connect.NewRequest(&workerv1.ExtendLeaseRequest{
		WorkId: first.GetWorkId(), LeaseId: first.GetLeaseId(), LeaseEpoch: first.GetLeaseEpoch(),
	})
	extend.Header().Set("Authorization", "Bearer "+workerToken)
	if _, err := workers.ExtendLease(ctx, extend); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE work_items SET lease_deadline=now() WHERE work_id=$1`, first.GetWorkId()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	second := pollBudgetWork(t, ctx, workers, workerID, workerToken)
	if second.GetLeaseEpoch() != first.GetLeaseEpoch()+1 ||
		second.GetBudgetSnapshot().GetUsage().GetStepsReserved() != 1 {
		t.Fatalf("first epoch=%d second=%+v", first.GetLeaseEpoch(), second)
	}
	settleSuccessfulSaga(t, workers, workerToken, second)
	complete := connect.NewRequest(&workerv1.CompleteWorkRequest{
		WorkId: second.GetWorkId(), LeaseId: second.GetLeaseId(), LeaseEpoch: second.GetLeaseEpoch(),
		ResultRef: "result", Receipt: `{"status":"ok"}`,
	})
	complete.Header().Set("Authorization", "Bearer "+workerToken)
	if _, err := workers.CompleteWork(ctx, complete); err != nil {
		t.Fatal(err)
	}
	get := connect.NewRequest(&runv1.GetRunRequest{RunId: created.Msg.GetRunId()})
	get.Header().Set("Authorization", "Bearer "+userToken)
	got, err := runs.GetRun(ctx, get)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := got.Msg.GetRun().GetBudgetSnapshot()
	if got.Msg.GetRun().GetState() != "succeeded" || snapshot.GetState() != "completed" ||
		snapshot.GetUsage().GetStepsUsed() != 1 || snapshot.GetUsage().GetStepsReserved() != 0 {
		t.Fatalf("run=%+v", got.Msg.GetRun())
	}
}

func TestRunBudgetRefusesSecondReservationAndDeadlineBecomesTerminal(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, key := seedRunOperator(t, ctx, st.Pool())
	runs := NewRunServer(st.Pool(), key)
	create := connect.NewRequest(&runv1.CreateRunRequest{
		PlanRef: "plan-deadline", Toolset: []string{"model.generate"}, Bindings: []string{"asset:any"},
		Budget: "1", Ttl: "150ms",
	})
	create.Header().Set("Authorization", "Bearer "+userToken)
	created, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	var budgetID string
	if err := st.Pool().QueryRow(ctx, `SELECT budget_id FROM run_budget_accounts WHERE run_id=$1`, created.Msg.GetRunId()).Scan(&budgetID); err != nil {
		t.Fatal(err)
	}
	firstID := ""
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		var reserveErr error
		firstID, reserveErr = reserveRunBudget(ctx, tx, budgetID, "tool", "call-1", runBudgetAmount{
			ToolCalls: 1, ToolResultBytes: 1,
		})
		return reserveErr
	}); err != nil {
		t.Fatal(err)
	}
	err = withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		_, reserveErr := reserveRunBudget(ctx, tx, budgetID, "tool", "call-2", runBudgetAmount{
			ToolCalls: 1, ToolResultBytes: 1,
		})
		return reserveErr
	})
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("second reservation want resource_exhausted got %v", err)
	}
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		return settleRunBudget(ctx, tx, firstID, "settled", runBudgetAmount{ToolCalls: 1, ToolResultBytes: 1})
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	get := connect.NewRequest(&runv1.GetRunRequest{RunId: created.Msg.GetRunId()})
	get.Header().Set("Authorization", "Bearer "+userToken)
	got, err := runs.GetRun(ctx, get)
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetRun().GetState() != "failed" || got.Msg.GetRun().GetError() != runDeadlineError ||
		got.Msg.GetRun().GetBudgetSnapshot().GetState() != "expired" {
		t.Fatalf("run=%+v", got.Msg.GetRun())
	}
}

func TestRunBudgetReservationIsAtomic(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, key := seedRunOperator(t, ctx, st.Pool())
	runs := NewRunServer(st.Pool(), key)
	create := connect.NewRequest(&runv1.CreateRunRequest{
		PlanRef: "plan-atomic-budget", Toolset: []string{"model.generate"}, Bindings: []string{"asset:any"},
		Budget: "1", Ttl: "30s",
	})
	create.Header().Set("Authorization", "Bearer "+userToken)
	created, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	var budgetID string
	if err := st.Pool().QueryRow(ctx, `SELECT budget_id FROM run_budget_accounts WHERE run_id=$1`, created.Msg.GetRunId()).Scan(&budgetID); err != nil {
		t.Fatal(err)
	}

	const contenders = 8
	results := make(chan error, contenders)
	var ready sync.WaitGroup
	ready.Add(contenders)
	start := make(chan struct{})
	for index := range contenders {
		go func() {
			ready.Done()
			<-start
			results <- withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
				_, reserveErr := reserveRunBudget(ctx, tx, budgetID, "tool", "atomic-"+string(rune('a'+index)), runBudgetAmount{
					ToolCalls: 1, ToolResultBytes: 1,
				})
				return reserveErr
			})
		}()
	}
	ready.Wait()
	close(start)
	succeeded, exhausted := 0, 0
	for range contenders {
		err := <-results
		switch connect.CodeOf(err) {
		case connect.CodeUnknown:
			if err != nil {
				t.Fatal(err)
			}
			succeeded++
		case connect.CodeResourceExhausted:
			exhausted++
		default:
			t.Fatalf("reservation error=%v", err)
		}
	}
	if succeeded != 1 || exhausted != contenders-1 {
		t.Fatalf("succeeded=%d exhausted=%d", succeeded, exhausted)
	}
}

func TestRunWorkerGenerateSettlesPersistentBudgetOnce(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, assetID, _ := seedUnitAsset(t, ctx, st, "run-budget-model")
	userToken, key := seedRunOperator(t, ctx, st.Pool())
	if _, err := st.Pool().Exec(ctx, `UPDATE grants SET bindings=jsonb_build_array(jsonb_build_object('kind','asset','id',$1::text))
		WHERE subject_kind='user' AND tools ? 'run.create'`, assetID); err != nil {
		t.Fatal(err)
	}
	adminToken := grantExistingRunAdminForWorker(t, ctx, st.Pool(), assetID)
	runs := NewRunServer(st.Pool(), key)
	create := connect.NewRequest(&runv1.CreateRunRequest{
		PlanRef: "plan-budget-model", Toolset: []string{"model.generate"}, Bindings: []string{"asset:" + assetID},
		Budget: "1", Ttl: "30s",
	})
	create.Header().Set("Authorization", "Bearer "+userToken)
	created, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}

	workers := NewWorkerServer(st.Pool(), key, false)
	workerID := "run-supervisor-" + newTestSuffix()
	certHash := strings.Repeat("e", 64)
	access := registerProductionRunWorker(t, ctx, workers, adminToken, workerID, certHash, assetID)
	poll := connect.NewRequest(&workerv1.PollWorkRequest{WorkerId: workerID, LongPollSeconds: 1})
	poll.Header().Set("Authorization", "Bearer "+access)
	leased, err := workers.PollWork(workerCertContext(ctx, certHash), poll)
	if err != nil || leased.Msg.GetWork() == nil {
		t.Fatalf("poll run work: %v %+v", err, leased)
	}
	work := leased.Msg.GetWork()

	ob := NewOnboardingServer(st.Pool(), assetID)
	ob.signingKey = key
	ob.capabilityPub = key.Public().(ed25519.PublicKey)
	seedCompletedSlot(t, ctx, onboardHarness{local: assetID}, ob, "https://model.example/v1", "secret")
	calls := 0
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		calls++
		return `{"done":true}`, nil
	}
	generate := &modelv1.GenerateRequest{
		ThreadId: work.GetThreadId(), TurnId: work.GetTurnId(), StepId: work.GetStepId(),
		GenerationId: "gen-run-budget-" + newTestSuffix(), ExpectedItemSequence: work.GetExpectedItemSequence(),
		ContextManifest:  &modelv1.ContextManifest{SystemPromptVersion: "test/v1", ModelSlotId: "default"},
		InputItems:       []*modelv1.GenerateInputItem{{ItemId: "input-1", Role: "user", Content: "inspect", ContentDigest: "sha256:test", TrustLevel: "user"}},
		GenerationLimits: &modelv1.GenerationLimits{MaxOutputTokens: 64, JsonMode: true},
		LeaseId:          work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(),
	}
	request := func() *connect.Request[modelv1.GenerateRequest] {
		return dualGenerateRequest(access, work.GetCapabilityToken(), generate)
	}
	first, err := ob.Generate(workerCertContext(ctx, certHash), request())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ob.Generate(workerCertContext(ctx, certHash), request())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.Msg.GetAcceptedAttemptId() == "" || second.Msg.GetAcceptedAttemptId() != first.Msg.GetAcceptedAttemptId() {
		t.Fatalf("calls=%d first=%+v second=%+v", calls, first.Msg, second.Msg)
	}
	var modelAuditActions []string
	rows, err := st.Pool().Query(ctx, `SELECT action, details::text FROM audit_entries
		WHERE run_id=$1 AND action LIKE 'model.%' ORDER BY sequence`, created.Msg.GetRunId())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var action, details string
		if err := rows.Scan(&action, &details); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if strings.Contains(details, "inspect") || strings.Contains(details, `{"done":true}`) {
			rows.Close()
			t.Fatalf("model audit leaked content: %s", details)
		}
		modelAuditActions = append(modelAuditActions, action)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if got := strings.Join(modelAuditActions, ","); got != "model.intent_recorded,model.effect_started,model.settled" {
		t.Fatalf("model audit actions=%s", got)
	}
	exhausted := proto.Clone(generate).(*modelv1.GenerateRequest)
	exhausted.GenerationId = "gen-run-budget-exhausted-" + newTestSuffix()
	exhausted.ExpectedItemSequence = first.Msg.GetNextItemSequence()
	if _, err := ob.Generate(workerCertContext(ctx, certHash), dualGenerateRequest(access, work.GetCapabilityToken(), exhausted)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("second logical generation want resource_exhausted got %v", err)
	}
	if calls != 1 {
		t.Fatalf("exhausted generation reached provider, calls=%d", calls)
	}

	settleSuccessfulSaga(t, workers, access, work, workerCertContext(ctx, certHash))
	complete := connect.NewRequest(&workerv1.CompleteWorkRequest{
		WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(),
		ResultRef: "result", Receipt: `{"status":"ok"}`,
	})
	complete.Header().Set("Authorization", "Bearer "+access)
	if _, err := workers.CompleteWork(workerCertContext(ctx, certHash), complete); err != nil {
		t.Fatal(err)
	}
	get := connect.NewRequest(&runv1.GetRunRequest{RunId: created.Msg.GetRunId()})
	get.Header().Set("Authorization", "Bearer "+userToken)
	got, err := runs.GetRun(ctx, get)
	if err != nil {
		t.Fatal(err)
	}
	usage := got.Msg.GetRun().GetBudgetSnapshot().GetUsage()
	if got.Msg.GetRun().GetState() != "succeeded" || usage.GetStepsUsed() != 1 ||
		usage.GetModelCallsUsed() != 1 || usage.GetModelCallsReserved() != 0 || usage.GetInputTokensUsed() == 0 {
		t.Fatalf("run=%+v", got.Msg.GetRun())
	}
}

func registerBudgetRunWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key ed25519.PrivateKey, workerID string) string {
	t.Helper()
	boot := "boot-budget-" + newTestSuffix()
	agents := NewAgentServer(pool, boot, key)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: workerID, BootstrapToken: boot, AgentPublicKey: "pub-budget",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]','[{"kind":"asset","id":"any"}]','test')`, "grant-budget-"+newTestSuffix(), workerID); err != nil {
		t.Fatal(err)
	}
	workers := NewWorkerServer(pool, key, true)
	register := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: workerID})
	register.Header().Set("Authorization", "Bearer "+registered.Msg.GetAccessToken())
	if _, err := workers.RegisterWorker(ctx, register); err != nil {
		t.Fatal(err)
	}
	return registered.Msg.GetAccessToken()
}

func pollBudgetWork(t *testing.T, ctx context.Context, workers *WorkerServer, workerID, token string) *workerv1.WorkItem {
	t.Helper()
	poll := connect.NewRequest(&workerv1.PollWorkRequest{WorkerId: workerID, LongPollSeconds: 1})
	poll.Header().Set("Authorization", "Bearer "+token)
	leased, err := workers.PollWork(ctx, poll)
	if err != nil {
		t.Fatal(err)
	}
	if leased.Msg.GetWork() == nil {
		t.Fatal("work was not leased")
	}
	return leased.Msg.GetWork()
}

func registerProductionRunWorker(t *testing.T, ctx context.Context, workers *WorkerServer,
	adminToken, workerID, certHash, assetID string) string {
	t.Helper()
	publicKey := "ed25519:" + workerID
	bootstrapRequest := connect.NewRequest(&workerv1.CreateWorkerBootstrapRequest{
		WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
		WorkerPublicKey: publicKey, ClientCertSha256: certHash, Bindings: []string{"asset:" + assetID},
	})
	bootstrapRequest.Header().Set("Authorization", "Bearer "+adminToken)
	bootstrap, err := workers.CreateWorkerBootstrap(ctx, bootstrapRequest)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := workers.RegisterWorkerIdentity(workerCertContext(ctx, certHash), connect.NewRequest(&workerv1.RegisterWorkerIdentityRequest{
		WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
		BootstrapToken: bootstrap.Msg.GetBootstrapToken(), WorkerPublicKey: publicKey,
	}))
	if err != nil {
		t.Fatal(err)
	}
	register := connect.NewRequest(&workerv1.RegisterWorkerRequest{
		WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
	})
	register.Header().Set("Authorization", "Bearer "+identity.Msg.GetAccessToken())
	if _, err := workers.RegisterWorker(workerCertContext(ctx, certHash), register); err != nil {
		t.Fatal(err)
	}
	return identity.Msg.GetAccessToken()
}

func grantExistingRunAdminForWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, assetID string) string {
	t.Helper()
	var userID, username string
	if err := pool.QueryRow(ctx, `SELECT user_id, username FROM users WHERE role='admin' ORDER BY created_at LIMIT 1`).
		Scan(&userID, &username); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, pool, userID, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, pool, OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(pool, time.Hour, false, MinPasswordLength)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: username, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	return login.Msg.GetToken()
}
