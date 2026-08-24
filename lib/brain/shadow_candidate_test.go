package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	modelv1 "yufeng/proto/gen/modelv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestShadowCandidateCoordinatorCreatesRealShadowRelease(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID, caseID := "shadow-asset-"+suffix, "shadow-case-"+suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id,display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	finding := &modelv1.TrafficFinding{
		Disposition: modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MISS,
		Confidence:  0.93, EvidenceRefs: []string{"evidence-1"}, AttackClass: "input_validation",
		RouteTemplate: "/api/items", Selectors: []string{"query.page"}, Rationale: "代表样本违反正向请求形状",
		OptionalShapeDraft: &artifactv1.ShapeSource{
			Methods: []string{"GET"}, RouteTemplate: "/api/items",
			Constraints: []*artifactv1.ShapeConstraint{{Selector: "query.page", MinLen: 1, MaxLen: 8, Charset: "digit"}},
		},
	}
	findingRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	candidateRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&telemetryv1.ReviewCandidate{CandidateId: "candidate-1"})
	if err != nil {
		t.Fatal(err)
	}
	representativesRaw, err := json.Marshal([]json.RawMessage{candidateRaw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(
		case_id,module_id,asset_id,state,priority,title,finding,representatives)
		VALUES($1,'traffic-interception',$2,'finding_ready',90,'shadow case',$3::jsonb,$4::jsonb)`,
		caseID, assetID, findingRaw, representativesRaw); err != nil {
		t.Fatal(err)
	}
	findingDigest, err := typedTrafficFindingDigest(finding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO shadow_candidate_jobs(case_id,finding_digest)
		VALUES($1,$2)`, caseID, findingDigest); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := ProcessShadowCandidateJobs(ctx, st.Pool(), key, nil); err != nil {
		t.Fatal(err)
	}
	var diagnosticState, diagnosticError string
	if err := st.Pool().QueryRow(ctx, `SELECT state,last_error FROM shadow_candidate_jobs WHERE case_id=$1`, caseID).
		Scan(&diagnosticState, &diagnosticError); err != nil {
		t.Fatal(err)
	}
	if diagnosticError != "" {
		t.Fatalf("shadow coordinator state=%s error=%s", diagnosticState, diagnosticError)
	}
	var jobState, caseState, releaseID, releaseState string
	if err := st.Pool().QueryRow(ctx, `SELECT j.state,c.state,j.release_id,r.state
		FROM shadow_candidate_jobs j JOIN investigation_cases c USING(case_id)
		JOIN releases r ON r.release_id=j.release_id WHERE j.case_id=$1`, caseID).
		Scan(&jobState, &caseState, &releaseID, &releaseState); err != nil {
		t.Fatal(err)
	}
	if jobState != "shadow" || caseState != "shadow_observing" || releaseID == "" || releaseState != "shadow" {
		t.Fatalf("job=%s case=%s release=%s state=%s", jobState, caseState, releaseID, releaseState)
	}
	if err := ProcessShadowCandidateJobs(ctx, st.Pool(), key, nil); err != nil {
		t.Fatalf("completed Shadow job must remain idempotent: %v", err)
	}
	var releaseCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM releases WHERE created_by=$1`, trafficShadowCoordinatorID+":"+caseID).Scan(&releaseCount); err != nil {
		t.Fatal(err)
	}
	if releaseCount != 1 {
		t.Fatalf("idempotent coordinator created %d releases", releaseCount)
	}
}
