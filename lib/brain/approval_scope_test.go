package brain

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "yufeng/proto/gen/agentv1"
)

func TestCapacityApprovalRequiresCaseAssetBinding(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}

	hiddenAsset := "approval-hidden-asset-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id,display_name) VALUES($1,$1)`, hiddenAsset); err != nil {
		t.Fatal(err)
	}
	visibleWorker := "approval-visible-worker-" + newTestSuffix()
	hiddenWorker := "approval-hidden-worker-" + newTestSuffix()
	visibleCase := "approval-visible-case-" + newTestSuffix()
	hiddenCase := "approval-hidden-case-" + newTestSuffix()
	visibleChange := "approval-visible-change-" + newTestSuffix()
	hiddenChange := "approval-hidden-change-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO workers(worker_id) VALUES($1),($2)`, visibleWorker, hiddenWorker); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id,module_id,asset_id,state,priority,title)
		VALUES($1,'traffic-interception',$3,'open',80,'visible capacity case'),
		      ($2,'traffic-interception',$4,'open',80,'hidden capacity case')`,
		visibleCase, hiddenCase, h.local, hiddenAsset); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO worker_capacity_changes(
		change_id,case_id,worker_id,requested_by,requested_capacity,previous_capacity,state,expires_at)
		VALUES($1,$3,$5,'jarvis',2,1,'pending',now()+interval '1 hour'),
		      ($2,$4,$6,'jarvis',2,1,'pending',now()+interval '1 hour')`,
		visibleChange, hiddenChange, visibleCase, hiddenCase, visibleWorker, hiddenWorker); err != nil {
		t.Fatal(err)
	}

	server := NewAgentInteractionServer(st.Pool())
	t.Run("cross asset read is denied", func(t *testing.T) {
		_, err := server.GetApproval(ctx, bearerReq(h.adminTok, &agentv1.GetApprovalRequest{ApprovalId: hiddenChange}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("GetApproval across asset bindings must return permission_denied, got %v", err)
		}
	})
	t.Run("cross asset decision is denied without mutation", func(t *testing.T) {
		_, err := server.DecideApproval(ctx, bearerReq(h.adminTok, &agentv1.DecideApprovalRequest{
			ApprovalId: hiddenChange,
			Approved:   true,
			Reason:     "must not cross asset bindings",
		}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("DecideApproval across asset bindings must return permission_denied, got %v", err)
		}
		var state string
		var capacity, audits int
		if err := st.Pool().QueryRow(ctx, `SELECT c.state,w.max_concurrency,
			(SELECT count(*) FROM audit_entries WHERE object_type='worker_capacity_change' AND object_id=$1)
			FROM worker_capacity_changes c JOIN workers w ON w.worker_id=c.worker_id WHERE c.change_id=$1`, hiddenChange).
			Scan(&state, &capacity, &audits); err != nil {
			t.Fatal(err)
		}
		if state != "pending" || capacity != 1 || audits != 0 {
			t.Fatalf("denied decision mutated state=%s capacity=%d audits=%d", state, capacity, audits)
		}
	})
	t.Run("bound asset remains readable and decidable", func(t *testing.T) {
		got, err := server.GetApproval(ctx, bearerReq(h.adminTok, &agentv1.GetApprovalRequest{ApprovalId: visibleChange}))
		if err != nil {
			t.Fatal(err)
		}
		view := got.Msg.GetApproval()
		if view.GetKind() != agentv1.ApprovalKind_APPROVAL_KIND_WORKER_CAPACITY || view.GetAssetId() != h.local ||
			view.GetCaseId() != visibleCase || view.GetWorkerId() != visibleWorker || view.GetRequestedCapacity() != 2 {
			t.Fatalf("visible approval projection=%v", view)
		}
		decided, err := server.DecideApproval(ctx, bearerReq(h.adminTok, &agentv1.DecideApprovalRequest{
			ApprovalId: visibleChange,
			Approved:   true,
			Reason:     "capacity is required",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if decided.Msg.GetState() != "approved" {
			t.Fatalf("decision state=%q", decided.Msg.GetState())
		}
		var capacity int
		var decidedAt *time.Time
		if err := st.Pool().QueryRow(ctx, `SELECT w.max_concurrency,c.decided_at FROM workers w
			JOIN worker_capacity_changes c ON c.worker_id=w.worker_id WHERE c.change_id=$1`, visibleChange).
			Scan(&capacity, &decidedAt); err != nil {
			t.Fatal(err)
		}
		if capacity != 2 || decidedAt == nil {
			t.Fatalf("approved capacity=%d decided_at=%v", capacity, decidedAt)
		}
	})
	t.Run("empty bindings do not grant asset approval access", func(t *testing.T) {
		if _, err := st.Pool().Exec(ctx, `UPDATE grants SET bindings='[]'::jsonb
			WHERE subject_kind='user' AND subject_id=$1`, h.adminID); err != nil {
			t.Fatal(err)
		}
		_, err := server.GetApproval(ctx, bearerReq(h.adminTok, &agentv1.GetApprovalRequest{ApprovalId: visibleChange}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("worker.capacity.approve with empty bindings must not read an asset approval, got %v", err)
		}
	})
}
