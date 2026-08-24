package brain

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"yufeng/lib/kernel"
	auditv1 "yufeng/proto/gen/auditv1"
	modelv1 "yufeng/proto/gen/modelv1"
)

func TestAgentAuditLedgerReconstructsRunAndDetectsCoordinateTamper(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, _ := seedRunOperator(t, ctx, st.Pool())
	suffix := newTestSuffix()
	runID := "run-audit-" + suffix
	turnID := "turn-audit-" + suffix
	threadID := "thread-audit-" + suffix
	workID := "work-audit-" + suffix
	budgetID := "budget-audit-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES('any','any','L1')`); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, "any"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO runs(run_id, bindings) VALUES($1,'["asset:any"]')`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_threads(thread_id, source_kind, source_ref, agent_id)
		VALUES($1,'run',$2,$2)`, threadID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_turns(turn_id, thread_id, source_version, input_snapshot, budget_id)
		VALUES($1,$2,1,'{}',$3)`, turnID, threadID, budgetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO work_items(
		work_id, run_id, turn_id, worker_id, budget_id, lease_epoch) VALUES($1,$2,$3,'worker-audit',$4,7)`,
		workID, runID, turnID, budgetID); err != nil {
		t.Fatal(err)
	}

	lease := generationLease{SubjectID: runID, HolderID: "worker-audit", BudgetID: budgetID, Lane: "run"}
	request := &modelv1.GenerateRequest{TurnId: turnID, GenerationId: "generation-audit", LeaseEpoch: 7}
	modelInput := "model-input-secret"
	modelOutput := "model-output-secret"
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		if err := appendModelAudit(ctx, tx, request, lease, "model.intent_recorded", modelInput, map[string]any{
			"request_digest": auditPayloadDigest(modelInput), "budget_reservation_id": "model-budget-reservation",
		}); err != nil {
			return err
		}
		return appendModelAudit(ctx, tx, request, lease, "model.settled", modelOutput, map[string]any{
			"response_digest": auditPayloadDigest(modelOutput), "model_calls": 1, "input_tokens": 3, "output_tokens": 2,
		})
	}); err != nil {
		t.Fatal(err)
	}

	claims := kernel.Claims{Subject: runID, AuthorizedParty: "worker-audit", BudgetID: budgetID, LeaseEpoch: 7}
	toolArguments := `{"password":"tool-argument-secret"}`
	toolResult := `{"token":"tool-result-secret"}`
	var invocationID string
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		var err error
		invocationID, err = recordToolIntentTx(ctx, tx, claims, "tool-request", "event.get",
			canonicalJSONDigest(toolArguments), "")
		if err != nil {
			return err
		}
		return markToolEffectStartedTx(ctx, tx, claims, invocationID, 4)
	}); err != nil {
		t.Fatal(err)
	}
	if err := (&ToolGatewayServer{pool: st.Pool()}).settleToolInvocation(ctx, claims, invocationID, "",
		"succeeded", toolResult, "", runBudgetAmount{ToolCalls: 1, ToolResultBytes: int64(len(toolResult))}); err != nil {
		t.Fatal(err)
	}
	receipt := "compensation-receipt-secret"
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		return appendRunSagaAudit(ctx, tx, runID, "compensated", receipt, map[string]any{
			"step_sequence": 1, "receipt_digest": auditPayloadDigest(receipt),
		})
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := st.Pool().Query(ctx, `SELECT sequence, action, details::text, payload_digest, turn_id, lease_epoch, budget_id
		FROM audit_entries WHERE run_id=$1 ORDER BY sequence`, runID)
	if err != nil {
		t.Fatal(err)
	}
	var sequences []int64
	var actions []string
	for rows.Next() {
		var sequence, leaseEpoch int64
		var action, details, payloadDigest, gotTurnID, gotBudgetID string
		if err := rows.Scan(&sequence, &action, &details, &payloadDigest, &gotTurnID, &leaseEpoch, &gotBudgetID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for _, secret := range []string{modelInput, modelOutput, "tool-argument-secret", "tool-result-secret", receipt} {
			if strings.Contains(details, secret) {
				rows.Close()
				t.Fatalf("audit details leaked %q: %s", secret, details)
			}
		}
		if gotTurnID != turnID || leaseEpoch != 7 || gotBudgetID != budgetID {
			rows.Close()
			t.Fatalf("coordinates turn=%s epoch=%d budget=%s", gotTurnID, leaseEpoch, gotBudgetID)
		}
		sequences = append(sequences, sequence)
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if got := strings.Join(actions, ","); got != "model.intent_recorded,model.settled,tool.intent_recorded,tool.effect_started,tool.settled,run.compensated" {
		t.Fatalf("actions=%s", got)
	}
	var byTurn int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE turn_id=$1`, turnID).Scan(&byTurn); err != nil {
		t.Fatal(err)
	}
	if byTurn != len(actions) {
		t.Fatalf("turn reconstruction=%d run reconstruction=%d", byTurn, len(actions))
	}
	list := connect.NewRequest(&auditv1.ListAuditEntriesRequest{RunId: runID, TurnId: turnID, PageSize: 100})
	list.Header().Set("Authorization", "Bearer "+userToken)
	listed, err := NewAuditServer(st.Pool()).ListAuditEntries(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetEntries()) != len(actions) {
		t.Fatalf("filtered audit entries=%d want=%d", len(listed.Msg.GetEntries()), len(actions))
	}
	reconstructed, err := ReconstructRunEvents(ctx, st.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(reconstructed, ","); got != "model.intent_recorded,model.settled,tool.intent_recorded,tool.effect_started,tool.settled,compensated" {
		t.Fatalf("reconstructed=%s", got)
	}
	verified, err := verifyAuditRange(ctx, st.Pool(), sequences[1], sequences[len(sequences)-1])
	if err != nil || !verified.GetValid() {
		t.Fatalf("partial chain valid=%v err=%v", verified.GetValid(), err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE audit_entries SET turn_id='tampered' WHERE sequence=$1`, sequences[2]); err != nil {
		t.Fatal(err)
	}
	verified, err = verifyAuditRange(ctx, st.Pool(), sequences[0], sequences[len(sequences)-1])
	if err != nil {
		t.Fatal(err)
	}
	if verified.GetValid() {
		t.Fatal("coordinate tamper must invalidate audit chain")
	}
}

func TestRunProgressStageAllowsOnlyAuditLabels(t *testing.T) {
	for _, stage := range []string{"hello", "compensate:prepare", "action_effect-started", "tool.invoke"} {
		if !validRunProgressStage(stage) {
			t.Fatalf("valid stage rejected: %q", stage)
		}
	}
	for _, stage := range []string{"", "secret value", "line\nbreak", strings.Repeat("x", 129)} {
		if validRunProgressStage(stage) {
			t.Fatalf("invalid stage accepted: %q", stage)
		}
	}
}
