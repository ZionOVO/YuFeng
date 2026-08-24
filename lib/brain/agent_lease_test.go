package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestWorkLeaseOwnerAndExpired(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-lease-" + newTestSuffix()
	worker := "worker-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: worker, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerServer(st.Pool(), priv, true)
	runID := "run-lease-" + newTestSuffix()
	workID := "work-lease-" + newTestSuffix()
	budgetID := "work:" + workID
	const leaseEpoch int64 = 1
	if _, err := st.Pool().Exec(ctx, `INSERT INTO runs(run_id, state, role, plan_ref, toolset, budget, ttl, bindings, created_by)
		VALUES($1,'running','worker','plan','[]','3','30s','[]','u')`, runID); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	claims := kernel.Claims{Subject: runID, AuthorizedParty: worker, Role: "worker", Audience: "tools", TokenID: "work-" + workID,
		BudgetID: budgetID, LeaseEpoch: leaseEpoch,
		ExpiresAt: until.Unix(), IssuedAt: time.Now().Unix(), Tools: []string{"ping"}, MaxCalls: 8}
	tok, err := kernel.SignCapabilityToken(claims, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO work_items(work_id, run_id, worker_id, lease_id, lease_epoch, budget_id, status, lease_deadline, capability_token)
		VALUES($1,$2,$3,'lease-ok',$4,$5,'leased', now() + interval '10 minutes', $6)`, workID, runID, worker, leaseEpoch, budgetID, tok); err != nil {
		t.Fatal(err)
	}
	if err := registerCapabilityToken(ctx, st.Pool(), claims.TokenID, budgetID, "lease-ok", leaseEpoch, until); err != nil {
		t.Fatal(err)
	}
	work := &workerv1.WorkItem{WorkId: workID, RunId: runID, LeaseId: "lease-ok", LeaseEpoch: leaseEpoch, CapabilityToken: tok}

	wrong := connect.NewRequest(&workerv1.CompleteWorkRequest{WorkId: work.WorkId, LeaseId: "nope", ResultRef: "x"})
	wrong.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.CompleteWork(ctx, wrong); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("wrong lease want failed_precondition got %v", err)
	}

	ext := connect.NewRequest(&workerv1.ExtendLeaseRequest{WorkId: work.WorkId, LeaseId: work.LeaseId, LeaseEpoch: work.LeaseEpoch})
	ext.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	extResp, err := ws.ExtendLease(ctx, ext)
	if err != nil {
		t.Fatal(err)
	}
	if extResp.Msg.CapabilityToken == "" || extResp.Msg.CapabilityToken == work.CapabilityToken {
		t.Fatal("extend must issue a new capability token")
	}
	if extResp.Msg.LeaseId != work.LeaseId || extResp.Msg.LeaseEpoch != work.LeaseEpoch || extResp.Msg.BudgetId != budgetID {
		t.Fatalf("same-epoch extend must keep lease_id/epoch/budget_id, got id=%s epoch=%d budget=%s",
			extResp.Msg.LeaseId, extResp.Msg.LeaseEpoch, extResp.Msg.BudgetId)
	}
	oldClaims, err := kernel.VerifyCapabilityToken(work.CapabilityToken, priv.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var revoked bool
	err = st.Pool().QueryRow(ctx, `SELECT revoked FROM capability_token_instances WHERE jti=$1`, oldClaims.TokenID).Scan(&revoked)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if revoked {
		t.Fatal("same-epoch extend must not revoke the previous capability token")
	}
	if err := requireLiveCapability(ctx, st.Pool(), oldClaims, work.CapabilityToken); err != nil {
		t.Fatalf("old same-epoch token must remain live until original exp: %v", err)
	}

	settleSuccessfulSaga(t, ws, reg.Msg.AccessToken, work)
	done := connect.NewRequest(&workerv1.CompleteWorkRequest{WorkId: work.WorkId, LeaseId: work.LeaseId, LeaseEpoch: work.LeaseEpoch, ResultRef: "ok", Receipt: `{"ok":true}`})
	done.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.CompleteWork(ctx, done); err != nil {
		t.Fatal(err)
	}
	kinds, err := ReconstructRunEvents(ctx, st.Pool(), work.RunId)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) == 0 {
		t.Fatal("audit ledger must reconstruct")
	}
	if err := verifyKinds(kinds); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredLeaseCannotComplete(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-exp-" + newTestSuffix()
	worker := "worker-exp-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: worker, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-exp-" + newTestSuffix()
	workID := "work-exp-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO runs(run_id, state, role, toolset, bindings, created_by) VALUES($1,'running','worker','[]','["asset:any"]','u')`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO work_items(work_id, run_id, worker_id, lease_id, status, lease_deadline)
		VALUES($1,$2,$3,'lease-old','leased', now() - interval '1 minute')`, workID, runID, worker); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerServer(st.Pool(), priv, true)
	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]','[{"kind":"asset","id":"any"}]','system')`, "g-reclaim-"+newTestSuffix(), worker); err != nil {
		t.Fatal(err)
	}
	register := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: worker})
	register.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.RegisterWorker(ctx, register); err != nil {
		t.Fatal(err)
	}
	exists, profile, err := loadWorkerProfile(ctx, st.Pool(), worker)
	if err != nil || !exists || !bindingsSubset([]string{"asset:any"}, profile) {
		t.Fatalf("live worker profile does not cover work: exists=%v profile=%v err=%v", exists, profile, err)
	}
	req := connect.NewRequest(&workerv1.CompleteWorkRequest{WorkId: workID, LeaseId: "lease-old", ResultRef: "ok"})
	req.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.CompleteWork(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expired lease want failed_precondition got %v", err)
	}
}

func TestExpiredLeaseCanBeReclaimed(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-reclaim-" + newTestSuffix()
	worker := "worker-reclaim-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: worker, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-reclaim-" + newTestSuffix()
	workID := "work-reclaim-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO runs(run_id, state, role, toolset, bindings, created_by) VALUES($1,'running','worker','[]','["asset:any"]','u')`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO work_items(work_id, run_id, worker_id, lease_id, status, lease_deadline)
		VALUES($1,$2,'old-worker','old-lease','leased', now() - interval '2 minutes')`, workID, runID); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerServer(st.Pool(), priv, true)
	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]','[{"kind":"asset","id":"any"}]','system')`, "g-reclaim-"+newTestSuffix(), worker); err != nil {
		t.Fatal(err)
	}
	register := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: worker})
	register.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.RegisterWorker(ctx, register); err != nil {
		t.Fatal(err)
	}
	got, err := ws.leaseWork(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.WorkId != workID {
		t.Fatal("expired lease was not reclaimable")
	}
}

func TestAckInstructionRequiresHolder(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-ack-" + newTestSuffix()
	agentID := "agent-ack-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueInstruction(ctx, agentID, "SESSION_MESSAGE", "ses-1", []string{"session.reply"}, []string{"ses-1"}); err != nil {
		t.Fatal(err)
	}
	poll := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: agentID, LongPollSeconds: 1})
	poll.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	ins, err := s.PollInstructions(ctx, poll)
	if err != nil || len(ins.Msg.Instructions) == 0 {
		t.Fatalf("poll %v", err)
	}
	item := ins.Msg.Instructions[0]
	ack := connect.NewRequest(&agentv1.AckInstructionRequest{InstructionId: item.InstructionId, LeaseId: "wrong", Status: "acked"})
	ack.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := s.AckInstruction(ctx, ack); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("want failed_precondition got %v", err)
	}
	ok := connect.NewRequest(&agentv1.AckInstructionRequest{InstructionId: item.InstructionId, LeaseId: item.LeaseId, LeaseEpoch: item.LeaseEpoch, Status: "acked"})
	ok.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := s.AckInstruction(ctx, ok); err != nil {
		t.Fatal(err)
	}
}

func TestCaseReviewInstructionRetriesThenFailsClosed(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	suffix := newTestSuffix()
	assetID := "asset-case-retry-" + suffix
	caseID := "case-retry-" + suffix
	profileID := "profile-retry-" + suffix
	agentID := "agent-retry-" + suffix
	bootstrap := "bootstrap-retry-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'open',90,'retry case')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO managed_agent_profiles(agent_id, display_name, tools, bindings, created_by)
		VALUES($1,'审查 Agent','["case.get","case.request_evidence","run.create"]',
		jsonb_build_array(jsonb_build_object('kind','asset','id',$2::text)),'operator')`, profileID, assetID); err != nil {
		t.Fatal(err)
	}
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		_, assignErr := assignCaseAgentProfile(ctx, tx, caseID)
		return assignErr
	}); err != nil {
		t.Fatal(err)
	}
	server := NewAgentServer(st.Pool(), bootstrap, priv)
	registered, err := server.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: bootstrap, AgentPublicKey: "registered-public-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnqueueInstruction(ctx, agentID, instructionCaseReview, caseID, caseInstructionTools(frozenAgentProfile{}), []string{assetBinding(assetID), "case:" + caseID}); err != nil {
		t.Fatal(err)
	}

	var instructionID string
	for attempt := 0; attempt <= caseReviewMaxRetries; attempt++ {
		poll := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: agentID, LongPollSeconds: 1})
		poll.Header().Set("Authorization", "Bearer "+registered.Msg.GetAccessToken())
		leased, err := server.PollInstructions(ctx, poll)
		if err != nil || len(leased.Msg.GetInstructions()) != 1 {
			t.Fatalf("attempt %d poll instructions=%d err=%v", attempt, len(leased.Msg.GetInstructions()), err)
		}
		item := leased.Msg.GetInstructions()[0]
		if attempt == 0 {
			instructionID = item.GetInstructionId()
		} else if item.GetInstructionId() != instructionID || item.GetLeaseEpoch() != int64(attempt+1) {
			t.Fatalf("retry must keep instruction and advance epoch: id=%s epoch=%d", item.GetInstructionId(), item.GetLeaseEpoch())
		}
		claims, err := kernel.VerifyCapabilityToken(item.GetCapabilityToken(), priv.Public().(ed25519.PublicKey), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ack := connect.NewRequest(&agentv1.AckInstructionRequest{
			InstructionId: item.GetInstructionId(), LeaseId: item.GetLeaseId(), LeaseEpoch: item.GetLeaseEpoch(),
			Status: "failed", Error: "sensitive-request-body-should-not-persist",
		})
		ack.Header().Set("Authorization", "Bearer "+registered.Msg.GetAccessToken())
		if _, err := server.AckInstruction(ctx, ack); err != nil {
			t.Fatalf("attempt %d ack: %v", attempt, err)
		}
		var revoked bool
		if err := st.Pool().QueryRow(ctx, `SELECT revoked FROM capability_token_instances WHERE jti=$1`, claims.TokenID).Scan(&revoked); err != nil || !revoked {
			t.Fatalf("attempt %d capability revoked=%v err=%v", attempt, revoked, err)
		}
		if attempt < caseReviewMaxRetries {
			var status, ackError string
			var retryCount int
			if err := st.Pool().QueryRow(ctx, `SELECT status, retry_count, ack_error FROM agent_instructions WHERE instruction_id=$1`, instructionID).
				Scan(&status, &retryCount, &ackError); err != nil {
				t.Fatal(err)
			}
			if status != "pending" || retryCount != attempt+1 || ackError == "sensitive-request-body-should-not-persist" {
				t.Fatalf("attempt %d status=%s retries=%d error=%q", attempt, status, retryCount, ackError)
			}
			if _, err := st.Pool().Exec(ctx, `UPDATE agent_instructions SET next_attempt_at=now()-interval '1 second' WHERE instruction_id=$1`, instructionID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var instructionState, caseState, ackError string
	var activities int
	if err := st.Pool().QueryRow(ctx, `SELECT status, ack_error FROM agent_instructions WHERE instruction_id=$1`, instructionID).
		Scan(&instructionState, &ackError); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&caseState); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM case_activities WHERE case_id=$1 AND ref_id=$2`,
		caseID, "case-review-failed:"+instructionID).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if instructionState != "failed" || caseState != "failed" || activities != 1 || ackError == "sensitive-request-body-should-not-persist" {
		t.Fatalf("instruction=%s case=%s activities=%d error=%q", instructionState, caseState, activities, ackError)
	}
}

func verifyKinds(kinds []string) error {
	if len(kinds) == 0 {
		return errString("empty")
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }
