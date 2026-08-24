package brain

import (
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"yufeng/lib/kernel"

	runv1 "yufeng/proto/gen/runv1"
)

func TestRunReadsEnforceBindingsBeforePagination(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	token, priv := seedRunOperator(t, ctx, st.Pool())
	server := NewRunServer(st.Pool(), priv)
	suffix := newTestSuffix()
	visibleOld := "run-visible-old-" + suffix
	hidden := "run-hidden-" + suffix
	visibleNew := "run-visible-new-" + suffix
	base := time.Now().Add(-time.Hour)
	for i, item := range []struct {
		id      string
		binding string
	}{{visibleOld, "asset:any"}, {hidden, "asset:other"}, {visibleNew, "asset:any"}} {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO runs(run_id, state, role, bindings, created_by, created_at)
			VALUES($1,'pending','worker',$2::jsonb,'test',$3)`, item.id, fmt.Sprintf("[%q]", item.binding), base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	hiddenGet := connect.NewRequest(&runv1.GetRunRequest{RunId: hidden})
	hiddenGet.Header().Set("Authorization", "Bearer "+token)
	if _, err := server.GetRun(ctx, hiddenGet); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("out-of-scope run want permission_denied got %v", err)
	}

	firstReq := connect.NewRequest(&runv1.ListRunsRequest{PageSize: 1})
	firstReq.Header().Set("Authorization", "Bearer "+token)
	first, err := server.ListRuns(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Runs) != 1 || first.Msg.Runs[0].RunId != visibleNew || first.Msg.NextPageToken == "" {
		t.Fatalf("unexpected first page: %+v", first.Msg)
	}
	secondReq := connect.NewRequest(&runv1.ListRunsRequest{PageSize: 1, PageToken: first.Msg.NextPageToken})
	secondReq.Header().Set("Authorization", "Bearer "+token)
	second, err := server.ListRuns(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Msg.Runs) != 1 || second.Msg.Runs[0].RunId != visibleOld || second.Msg.NextPageToken != "" {
		t.Fatalf("unexpected second page: %+v", second.Msg)
	}
}

func TestRunEventsPaginateAndCancelIsAtomic(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	token, priv := seedRunOperator(t, ctx, st.Pool())
	server := NewRunServer(st.Pool(), priv)
	runID := "run-cancel-" + newTestSuffix()
	workID := "work-cancel-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO runs(run_id, state, role, bindings, created_by)
		VALUES($1,'pending','worker','["asset:any"]','test')`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO work_items(work_id, run_id) VALUES($1,$2)`, workID, runID); err != nil {
		t.Fatal(err)
	}
	tokenID := "jti-cancel-" + newTestSuffix()
	budgetID := "budget-cancel-" + newTestSuffix()
	leaseID := "lease-cancel-" + newTestSuffix()
	expires := time.Now().Add(time.Hour)
	capability, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: runID, AuthorizedParty: "worker", Audience: "tools", TokenID: tokenID,
		BudgetID: budgetID, LeaseEpoch: 1, ExpiresAt: expires.Unix(), IssuedAt: time.Now().Unix(),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerCapabilityToken(ctx, st.Pool(), tokenID, budgetID, leaseID, 1, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE work_items SET status='leased', worker_id='worker', lease_id=$1,
		lease_deadline=$2, capability_token=$3, budget_id=$4, lease_epoch=1 WHERE work_id=$5`,
		leaseID, expires, capability, budgetID, workID); err != nil {
		t.Fatal(err)
	}
	if err := withTx(ctx, st.Pool(), func(tx pgx.Tx) error {
		if err := appendRunEvent(ctx, tx, runID, "one", ""); err != nil {
			return err
		}
		return appendRunEvent(ctx, tx, runID, "two", "")
	}); err != nil {
		t.Fatal(err)
	}

	firstReq := connect.NewRequest(&runv1.ListRunEventsRequest{RunId: runID, PageSize: 1})
	firstReq.Header().Set("Authorization", "Bearer "+token)
	first, err := server.ListRunEvents(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Events) != 1 || first.Msg.NextPageToken == "" {
		t.Fatalf("unexpected event first page: %+v", first.Msg)
	}
	secondReq := connect.NewRequest(&runv1.ListRunEventsRequest{RunId: runID, PageSize: 1, PageToken: first.Msg.NextPageToken})
	secondReq.Header().Set("Authorization", "Bearer "+token)
	second, err := server.ListRunEvents(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Msg.Events) != 1 || second.Msg.NextPageToken != "" {
		t.Fatalf("unexpected event second page: %+v", second.Msg)
	}

	cancelReq := connect.NewRequest(&runv1.CancelRunRequest{RunId: runID})
	cancelReq.Header().Set("Authorization", "Bearer "+token)
	cancelled, err := server.CancelRun(ctx, cancelReq)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Msg.GetRun().GetState() != "cancelled" {
		t.Fatalf("cancel state=%q", cancelled.Msg.GetRun().GetState())
	}
	var runState, workState string
	if err := st.Pool().QueryRow(ctx, `SELECT r.state, w.status FROM runs r JOIN work_items w USING(run_id) WHERE r.run_id=$1`, runID).Scan(&runState, &workState); err != nil {
		t.Fatal(err)
	}
	if runState != "cancelled" || workState != "cancelled" {
		t.Fatalf("run=%s work=%s", runState, workState)
	}
	var cancelEvents int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE run_id=$1 AND action='run.cancel'`, runID).Scan(&cancelEvents); err != nil {
		t.Fatal(err)
	}
	if cancelEvents != 1 {
		t.Fatalf("cancel events=%d", cancelEvents)
	}
	var revoked bool
	if err := st.Pool().QueryRow(ctx, `SELECT revoked FROM capability_token_instances WHERE jti=$1`, tokenID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("cancel must revoke the leased work capability")
	}
	if _, err := server.CancelRun(ctx, cancelReq); err != nil {
		t.Fatalf("repeated cancel must be idempotent: %v", err)
	}
}
