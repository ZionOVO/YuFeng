package brain

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestShadowObservingCaseAcceptsTypedInvestigationCompletion(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID := "shadow-receipt-asset-" + suffix
	caseID := "shadow-receipt-case-" + suffix
	threadID := "shadow-receipt-thread-" + suffix
	turnID := "shadow-receipt-turn-" + suffix
	stepID := "shadow-receipt-step-" + suffix
	runID := "shadow-receipt-run-" + suffix
	workID := "shadow-receipt-work-" + suffix
	generationID := "shadow-receipt-generation-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'shadow_observing',90,'shadow receipt case')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_threads(thread_id, source_kind, source_ref, agent_id)
		VALUES($1,'run',$2,$2)`, threadID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_turns(turn_id, thread_id, source_version, input_snapshot)
		VALUES($1,$2,1,'{}')`, turnID, threadID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_steps(step_id, turn_id, step_sequence) VALUES($1,$2,1)`, stepID, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO runs(run_id, state, role, toolset, bindings, created_by)
		VALUES($1,'running','traffic-investigator','["model.generate"]',$2::jsonb,$3)`,
		runID, `["asset:`+assetID+`","case:`+caseID+`"]`, "case:"+caseID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO work_items(work_id, run_id, turn_id, investigation_case_id, status)
		VALUES($1,$2,$3,$4,'leased')`, workID, runID, turnID, caseID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO model_generations(generation_id, turn_id, step_id, request_digest,
		request_payload, context_manifest, generation_limits, state, sensitive, case_id)
		VALUES($1,$2,$3,'sha256:request','{}','{}','{}','completed',true,$4)`, generationID, turnID, stepID, caseID); err != nil {
		t.Fatal(err)
	}
	resultDigest := "sha256:traffic-finding"
	err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		investigation, validateErr := validateInvestigationCompletion(ctx, tx, workID, resultDigest,
			`{"status":"ok","traffic_finding_digest":"`+resultDigest+`"}`)
		if validateErr != nil {
			return validateErr
		}
		if !investigation {
			t.Fatal("case work must be recognized as investigation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
