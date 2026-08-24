package brain

import (
	"testing"
	"time"
)

func TestExpireUsesHardExpiresNotReview(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	hardID := "rel-hard-" + newTestSuffix()
	reviewID := "rel-review-" + newTestSuffix()
	_, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, signed_at, review_at, hard_expires_at)
		VALUES($1,'shadow','{}',86400,now(), now() + interval '1 hour', now() - interval '1 second')`, hardID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, signed_at, review_at, hard_expires_at)
		VALUES($1,'shadow','{}',86400,now(), now() - interval '1 second', now() + interval '1 hour')`, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expireReleases(ctx, st.Pool(), SchedulerConfig{}); err != nil {
		t.Fatal(err)
	}
	var hardState, reviewState string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1`, hardID).Scan(&hardState); err != nil {
		t.Fatal(err)
	}
	if hardState != "retired" {
		t.Fatalf("hard expire must retire, state=%s", hardState)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1`, reviewID).Scan(&reviewState); err != nil {
		t.Fatal(err)
	}
	if reviewState != "shadow" {
		t.Fatalf("review must not unload, state=%s", reviewState)
	}
	qn, err := enqueueDueReviews(ctx, st.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if qn < 1 {
		t.Fatal("review must enqueue")
	}
	var topic string
	if err := st.Pool().QueryRow(ctx, `SELECT topic FROM outbox WHERE dedupe_key=$1`, "review:"+reviewID).Scan(&topic); err != nil {
		t.Fatal(err)
	}
	if topic != "yufeng.review.due" {
		t.Fatalf("topic=%s", topic)
	}
	_ = time.Second
}

func TestExpireIgnoresBareTTL(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	id := "rel-ttl-" + newTestSuffix()
	_, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, signed_at, hard_expires_at)
		VALUES($1,'shadow','{}',1, now() - interval '1 hour', NULL)`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expireReleases(ctx, st.Pool(), SchedulerConfig{}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := st.Pool().QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "shadow" {
		t.Fatalf("bare ttl must not auto-retire, state=%s", state)
	}
}

func TestSkipAutoPromoteBlocksShapeAndRule(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	shapeID := "rel-shape-" + newTestSuffix()
	ruleID := "rel-rule-" + newTestSuffix()
	okID := "rel-ok-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, scope_risk, evidence_class)
		VALUES($1,'shadow','{"kind":"KIND_SHAPE"}',86400,'exact','crs_mapped')`, shapeID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, scope_risk, evidence_class)
		VALUES($1,'shadow','{"kind":"KIND_RULE"}',86400,'exact','crs_mapped')`, ruleID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, scope_risk, evidence_class)
		VALUES($1,'shadow','{"kind":"KIND_POLICY","replayReport":{"passed":true,"maliciousTotal":1,"maliciousBlocked":1}}',86400,'exact','crs_mapped')`, okID); err != nil {
		t.Fatal(err)
	}
	skip, err := skipAutoPromote(ctx, st.Pool(), shapeID, false)
	if err != nil || !skip {
		t.Fatalf("shape must stay in shadow: skip=%v err=%v", skip, err)
	}
	skip, err = skipAutoPromote(ctx, st.Pool(), ruleID, false)
	if err != nil || !skip {
		t.Fatalf("KIND_RULE must stay in shadow: skip=%v err=%v", skip, err)
	}
	skip, err = skipAutoPromote(ctx, st.Pool(), ruleID, true)
	if err != nil || skip {
		t.Fatalf("demo KIND_RULE may auto-promote: skip=%v err=%v", skip, err)
	}
	skip, err = skipAutoPromote(ctx, st.Pool(), okID, false)
	if err != nil || skip {
		t.Fatalf("qualified policy may promote: skip=%v err=%v", skip, err)
	}
}
