package brain

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	modelsidev1 "yufeng/proto/gen/modelsidev1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestCaseProjectionIgnoresLegacyModelResultsInReviewCandidates(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, assetID, _ := seedUnitAsset(t, ctx, st, "case-model-result")
	at := timestamppb.New(time.Now().UTC().Truncate(time.Second))
	legacy, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&modelsidev1.ModelResult{
		ResultId: "result-legacy", RequestId: "request-legacy", UnitId: unitID, AssetId: assetID,
		GenerationId: "generation-legacy", GenerationSeq: 1,
		Kind: modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT, Score: 0.98,
		Method: "POST", Route: "/login", OccurredAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&telemetryv1.ReviewCandidate{
		CandidateId: "candidate-real", UnitId: unitID, AssetId: assetID,
		Method: "GET", RouteTemplate: "/catalog", RiskScore: 0.72, OccurredAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	representatives, err := json.Marshal([]json.RawMessage{legacy, candidate})
	if err != nil {
		t.Fatal(err)
	}
	caseID := "case-model-result-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(
		case_id,module_id,asset_id,state,priority,title,representatives)
		VALUES($1,'traffic-interception',$2,'open',98,'model result compatibility',$3::jsonb)`,
		caseID, assetID, representatives); err != nil {
		t.Fatal(err)
	}

	item, err := scanInvestigationCase(st.Pool().QueryRow(ctx, `SELECT `+investigationCaseSelectColumns+`
		FROM investigation_cases WHERE case_id=$1`, caseID))
	if err != nil {
		t.Fatal(err)
	}
	if len(item.GetRepresentatives()) != 1 || item.GetRepresentatives()[0].GetCandidateId() != "candidate-real" {
		t.Fatalf("representatives=%+v", item.GetRepresentatives())
	}
}
