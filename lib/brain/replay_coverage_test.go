package brain

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestSkipPromoteWithoutReplayReport(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	missingID := "rel-cover-miss-" + newTestSuffix()
	failID := "rel-cover-fail-" + newTestSuffix()
	okID := "rel-cover-ok-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, scope_risk, evidence_class)
		VALUES($1,'shadow','{"kind":"KIND_POLICY"}',86400,'exact','crs_mapped')`, missingID); err != nil {
		t.Fatal(err)
	}
	failArt, err := protojson.Marshal(&artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_POLICY,
		ReplayReport: &artifactv1.ReplayReport{
			Passed: false, MaliciousTotal: 3, MaliciousBlocked: 1, BenignBlocked: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, scope_risk, evidence_class)
		VALUES($1,'shadow',$2::jsonb,86400,'exact','crs_mapped')`, failID, string(failArt)); err != nil {
		t.Fatal(err)
	}
	okArt, err := protojson.Marshal(&artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_POLICY,
		ReplayReport: &artifactv1.ReplayReport{
			Passed: true, MaliciousTotal: 3, MaliciousBlocked: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, scope_risk, evidence_class)
		VALUES($1,'shadow',$2::jsonb,86400,'exact','crs_mapped')`, okID, string(okArt)); err != nil {
		t.Fatal(err)
	}

	skip, err := skipAutoPromote(ctx, st.Pool(), missingID, false)
	if err != nil || !skip {
		t.Fatalf("missing replay report must stay in shadow: skip=%v err=%v", skip, err)
	}
	skip, err = skipAutoPromote(ctx, st.Pool(), failID, false)
	if err != nil || !skip {
		t.Fatalf("failed replay report must stay in shadow: skip=%v err=%v", skip, err)
	}
	skip, err = skipAutoPromote(ctx, st.Pool(), okID, false)
	if err != nil || skip {
		t.Fatalf("passed replay report may auto-promote: skip=%v err=%v", skip, err)
	}

	if _, err := autoPromoteReleases(ctx, st.Pool(), SchedulerConfig{}); err != nil {
		t.Fatal(err)
	}
	var missState, failState string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1`, missingID).Scan(&missState); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1`, failID).Scan(&failState); err != nil {
		t.Fatal(err)
	}
	if missState != "shadow" || failState != "shadow" {
		t.Fatalf("unqualified replay must remain shadow: missing=%s failed=%s", missState, failState)
	}
}
