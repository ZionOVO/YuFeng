package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentv1 "yufeng/proto/gen/agentv1"
	modelv1 "yufeng/proto/gen/modelv1"
	runv1 "yufeng/proto/gen/runv1"
)

func TestTurnCheckpointReleasesLeaseAndWakesFromSeparateInputSequence(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	agents, access, agentID, turnID := seedLeasedSessionTurn(t, ctx, st.Pool())
	leased := pollOneTurn(t, ctx, agents, access, agentID)
	if leased.GetTurnId() != turnID || leased.GetExpectedItemSequence() != 2 {
		t.Fatalf("leased turn=%q next=%d", leased.GetTurnId(), leased.GetExpectedItemSequence())
	}
	get := connect.NewRequest(&agentv1.GetTurnRequest{TurnId: turnID})
	get.Header().Set("Authorization", "Bearer "+access)
	get.Header().Set(CapabilityHeader, "Bearer "+leased.GetCapabilityToken())
	projected, err := agents.GetTurn(ctx, get)
	if err != nil || projected.Msg.GetTurn().GetNextItemSequence() != 2 {
		t.Fatalf("get turn: %v %+v", err, projected)
	}
	items := connect.NewRequest(&agentv1.ListTurnItemsRequest{TurnId: turnID})
	items.Header().Set("Authorization", "Bearer "+access)
	items.Header().Set(CapabilityHeader, "Bearer "+leased.GetCapabilityToken())
	listed, err := agents.ListTurnItems(ctx, items)
	if err != nil || len(listed.Msg.GetItems()) != 1 || listed.Msg.GetItems()[0].GetKind() != agentv1.AgentItemKind_AGENT_ITEM_KIND_INPUT_REFERENCE {
		t.Fatalf("list turn items: %v %+v", err, listed)
	}
	yield := connect.NewRequest(&agentv1.YieldTurnRequest{
		InstructionId: leased.GetInstructionId(), TurnId: turnID, LeaseId: leased.GetLeaseId(),
		LeaseEpoch: leased.GetLeaseEpoch(), ExpectedItemSequence: leased.GetExpectedItemSequence(),
		WaitState:      agentv1.AgentTurnState_AGENT_TURN_STATE_WAITING_INPUT,
		CheckpointJson: `{"phase":"awaiting-user","safe":true}`,
	})
	yield.Header().Set("Authorization", "Bearer "+access)
	yield.Header().Set(CapabilityHeader, "Bearer "+leased.GetCapabilityToken())
	paused, err := agents.YieldTurn(ctx, yield)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Msg.GetTurn().GetState() != agentv1.AgentTurnState_AGENT_TURN_STATE_WAITING_INPUT ||
		paused.Msg.GetTurn().GetNextItemSequence() != 3 {
		t.Fatalf("paused=%+v", paused.Msg.GetTurn())
	}
	var status, leaseID string
	if err := st.Pool().QueryRow(ctx, `SELECT status, lease_id FROM agent_instructions WHERE instruction_id=$1`,
		leased.GetInstructionId()).Scan(&status, &leaseID); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" || leaseID != "" {
		t.Fatalf("yielded instruction status=%q lease=%q", status, leaseID)
	}
	err = withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		sequence, err := appendTurnInput(ctx, tx, turnID, "user_answer", "session-answer:1")
		if err != nil {
			return err
		}
		if sequence != 1 {
			return fmt.Errorf("input sequence=%d want 1", sequence)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered := pollOneTurn(t, ctx, agents, access, agentID)
	if recovered.GetLeaseEpoch() != leased.GetLeaseEpoch()+1 || recovered.GetExpectedItemSequence() != 3 {
		t.Fatalf("recovered epoch=%d next=%d", recovered.GetLeaseEpoch(), recovered.GetExpectedItemSequence())
	}
	if !strings.Contains(recovered.GetCheckpointJson(), "awaiting-user") {
		t.Fatalf("checkpoint=%s", recovered.GetCheckpointJson())
	}
	var nextInput, nextItem int64
	if err := st.Pool().QueryRow(ctx, `SELECT next_input_sequence, next_item_sequence FROM agent_turns WHERE turn_id=$1`, turnID).
		Scan(&nextInput, &nextItem); err != nil {
		t.Fatal(err)
	}
	if nextInput != 2 || nextItem != 3 {
		t.Fatalf("sequences input=%d item=%d", nextInput, nextItem)
	}
}

func TestGenerateRecoversEffectStartedAttemptUnderNewLeaseEpoch(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "jarvis-recover-" + newTestSuffix()
	boot := "boot-recover-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, key)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "pub-recover",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, turnID, err := ensureAgentTurn(ctx, st.Pool(), turnSeed{
		SourceKind: threadSourceSession, SourceRef: "ses-recover", SubjectID: agentID, SourceVersion: 1,
		SourceCursor: map[string]any{"messageSequence": 1}, InputSnapshot: map[string]any{"contentRef": "session:recover:1"},
		BudgetID: "session:recover", ContentRef: "session:recover:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.EnqueueInstruction(ctx, agentID, instructionSession, turnID, sessionInstructionTools, []string{"ses-recover"}); err != nil {
		t.Fatal(err)
	}
	firstLease := pollOneTurn(t, ctx, agents, registered.Msg.GetAccessToken(), agentID)
	firstReq := generateForInstruction(firstLease)
	digest, requestRaw, manifestRaw, limitsRaw, err := generationRequestBytes(firstReq)
	if err != nil {
		t.Fatal(err)
	}
	started, err := (&OnboardingServer{pool: st.Pool()}).beginGeneration(ctx, firstReq, generationLease{
		SubjectID: agentID, HolderID: agentID, BudgetID: firstLease.GetBudgetId(), Lane: "agent",
	}, digest, requestRaw, manifestRaw, limitsRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE model_attempts SET state='effect_started', effect_started_at=now()
		WHERE attempt_id=$1`, started.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE agent_instructions SET lease_expires_at=now()-interval '1 second'
		WHERE instruction_id=$1`, firstLease.GetInstructionId()); err != nil {
		t.Fatal(err)
	}
	recovered := pollOneTurn(t, ctx, agents, registered.Msg.GetAccessToken(), agentID)
	if recovered.GetLeaseEpoch() != firstLease.GetLeaseEpoch()+1 || recovered.GetResumeGenerationId() != firstReq.GetGenerationId() || recovered.GetExpectedItemSequence() != 3 {
		t.Fatalf("recovered=%+v", recovered)
	}
	retryReq := generateForInstruction(recovered)
	retryReq.GenerationId = recovered.GetResumeGenerationId()
	ob := NewOnboardingServer(st.Pool(), agentID)
	ob.signingKey = key
	ob.capabilityPub = key.Public().(ed25519.PublicKey)
	seedCompletedSlot(t, ctx, onboardHarness{local: "asset-recover"}, ob, "https://model.example/v1", "secret")
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		return `{"done":true}`, nil
	}
	got, err := ob.Generate(ctx, dualGenerateRequest(registered.Msg.GetAccessToken(), recovered.GetCapabilityToken(), retryReq))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetAcceptedAttemptId() == "" || got.Msg.GetAcceptedAttemptId() == started.AttemptID {
		t.Fatalf("accepted attempt=%q old=%q", got.Msg.GetAcceptedAttemptId(), started.AttemptID)
	}
	rows, err := st.Pool().Query(ctx, `SELECT state FROM model_attempts WHERE generation_id=$1 ORDER BY attempt_sequence`, firstReq.GetGenerationId())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var states []string
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			t.Fatal(err)
		}
		states = append(states, state)
	}
	if strings.Join(states, ",") != "outcome_unknown,settled" {
		t.Fatalf("attempt states=%v", states)
	}
}

func TestGenerateAcceptsOneResponseAndReplaysWithoutSecondAttempt(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "jarvis-gen-" + newTestSuffix()
	boot := "boot-gen-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, key)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "pub-gen",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, turnID, err := ensureAgentTurn(ctx, st.Pool(), turnSeed{
		SourceKind: threadSourceSession, SourceRef: "ses-gen", SubjectID: agentID, SourceVersion: 1,
		SourceCursor: map[string]any{"messageSequence": 1}, InputSnapshot: map[string]any{"contentRef": "session:ses-gen:1"},
		BudgetID: "session:ses-gen", ContentRef: "session:ses-gen:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.EnqueueInstruction(ctx, agentID, instructionSession, turnID, sessionInstructionTools, []string{"ses-gen"}); err != nil {
		t.Fatal(err)
	}
	leased := pollOneTurn(t, ctx, agents, registered.Msg.GetAccessToken(), agentID)
	ob := NewOnboardingServer(st.Pool(), agentID)
	ob.signingKey = key
	ob.capabilityPub = key.Public().(ed25519.PublicKey)
	seedCompletedSlot(t, ctx, onboardHarness{local: "asset-gen"}, ob, "https://model.example/v1", "secret")
	calls := 0
	ob.completeFn = func(context.Context, string, string, string, []chatMessage) (string, error) {
		calls++
		return `{"tool":"session.reply","args":{"session_id":"ses-gen","content":"ok"}}`, nil
	}
	req := generateForInstruction(leased)
	first, err := ob.Generate(ctx, dualGenerateRequest(registered.Msg.GetAccessToken(), leased.GetCapabilityToken(), req))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ob.Generate(ctx, dualGenerateRequest(registered.Msg.GetAccessToken(), leased.GetCapabilityToken(), req))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.Msg.GetAcceptedAttemptId() == "" || second.Msg.GetAcceptedAttemptId() != first.Msg.GetAcceptedAttemptId() {
		t.Fatalf("calls=%d first=%+v second=%+v", calls, first.Msg, second.Msg)
	}
	if len(first.Msg.GetOutputItems()) != 1 ||
		first.Msg.GetOutputItems()[0].GetKind() != modelv1.GenerateOutputKind_GENERATE_OUTPUT_KIND_TOOL_CALL ||
		first.Msg.GetOutputItems()[0].GetCallId() == "" {
		t.Fatalf("output=%+v", first.Msg.GetOutputItems())
	}
	var attempts, items int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM model_attempts WHERE generation_id=$1`, req.GetGenerationId()).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_items WHERE turn_id=$1`, turnID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || items != 4 || first.Msg.GetNextItemSequence() != 5 {
		t.Fatalf("attempts=%d items=%d next=%d", attempts, items, first.Msg.GetNextItemSequence())
	}
}

func TestSessionTriageAndRunCreatePinnedTurns(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	agentID := "jarvis-sources-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES($1,'x','orchestrator','pub')`, agentID); err != nil {
		t.Fatal(err)
	}
	assetID := "asset-source-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	clusterID := seedProposalCluster(t, ctx, st.Pool(), assetID, "/items", "GET", 1, nil)
	triageTurn, err := ensureTriageTurn(ctx, st.Pool(), agentID, clusterID)
	if err != nil {
		t.Fatal(err)
	}
	userToken, runKey := seedRunOperator(t, ctx, st.Pool())
	runs := NewRunServer(st.Pool(), runKey)
	create := connect.NewRequest(&runv1.CreateRunRequest{
		PlanRef: "plan-cognitive", Toolset: []string{"model.generate"}, Bindings: []string{"asset:any"}, Budget: "3", Ttl: "15s",
	})
	create.Header().Set("Authorization", "Bearer "+userToken)
	run, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	if triageTurn == "" || run.Msg.GetTurnId() == "" {
		t.Fatalf("triage=%q run=%q", triageTurn, run.Msg.GetTurnId())
	}
	var runSource, workID, planDigest string
	if err := st.Pool().QueryRow(ctx, `SELECT th.source_ref, t.source_cursor->>'workId', t.source_cursor->>'planDigest'
		FROM agent_turns t JOIN agent_threads th ON th.thread_id=t.thread_id WHERE t.turn_id=$1`, run.Msg.GetTurnId()).
		Scan(&runSource, &workID, &planDigest); err != nil {
		t.Fatal(err)
	}
	if runSource != run.Msg.GetRunId() || workID == "" || !strings.HasPrefix(planDigest, "sha256:") {
		t.Fatalf("run source=%q work=%q digest=%q", runSource, workID, planDigest)
	}
}

func seedLeasedSessionTurn(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (*AgentServer, string, string, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "jarvis-checkpoint-" + newTestSuffix()
	boot := "boot-checkpoint-" + newTestSuffix()
	agents := NewAgentServer(pool, boot, key)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "pub-checkpoint",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, turnID, err := ensureAgentTurn(ctx, pool, turnSeed{
		SourceKind: threadSourceSession, SourceRef: "ses-checkpoint", SubjectID: agentID, SourceVersion: 1,
		SourceCursor: map[string]any{"messageSequence": 1}, InputSnapshot: map[string]any{"contentRef": "session:checkpoint:1"},
		BudgetID: "session:checkpoint", ContentRef: "session:checkpoint:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.EnqueueInstruction(ctx, agentID, instructionSession, turnID, sessionInstructionTools, []string{"ses-checkpoint"}); err != nil {
		t.Fatal(err)
	}
	return agents, registered.Msg.GetAccessToken(), agentID, turnID
}

func pollOneTurn(t *testing.T, ctx context.Context, agents *AgentServer, access, agentID string) *agentv1.AgentInstruction {
	t.Helper()
	req := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: agentID, LongPollSeconds: 1})
	req.Header().Set("Authorization", "Bearer "+access)
	got, err := agents.PollInstructions(ctx, req)
	if err != nil || len(got.Msg.GetInstructions()) != 1 {
		t.Fatalf("poll: %v %+v", err, got)
	}
	return got.Msg.GetInstructions()[0]
}

func generateForInstruction(ins *agentv1.AgentInstruction) *modelv1.GenerateRequest {
	return &modelv1.GenerateRequest{
		ThreadId: ins.GetThreadId(), TurnId: ins.GetTurnId(), StepId: ins.GetStepId(),
		GenerationId: "gen-test-" + newTestSuffix(), ExpectedItemSequence: ins.GetExpectedItemSequence(),
		ContextManifest:  &modelv1.ContextManifest{SystemPromptVersion: "test/v1", ModelSlotId: "default"},
		InputItems:       []*modelv1.GenerateInputItem{{ItemId: "input-1", Role: "user", Content: "reply", ContentDigest: "sha256:test", TrustLevel: "user"}},
		GenerationLimits: &modelv1.GenerationLimits{MaxOutputTokens: 64, JsonMode: true},
		LeaseId:          ins.GetLeaseId(), LeaseEpoch: ins.GetLeaseEpoch(),
	}
}

func dualGenerateRequest(access, capability string, msg *modelv1.GenerateRequest) *connect.Request[modelv1.GenerateRequest] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+access)
	req.Header().Set(CapabilityHeader, "Bearer "+capability)
	return req
}
