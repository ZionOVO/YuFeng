package brain

import (
	"testing"

	workerv1 "yufeng/proto/gen/workerv1"
)

func TestListWorkersFiltersCurrentBindingsBeforePagination(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	hiddenAsset := "worker-hidden-asset-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id,display_name) VALUES($1,$1)`, hiddenAsset); err != nil {
		t.Fatal(err)
	}

	visibleNewest := "worker-visible-newest-" + newTestSuffix()
	hiddenMiddle := "worker-hidden-middle-" + newTestSuffix()
	visibleOldest := "worker-visible-oldest-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO workers(
		worker_id,worker_kind,identity_domain,version,bindings,operating_system,architecture,sandbox_capabilities,max_concurrency,updated_at)
		VALUES
		($1,'RUN_SUPERVISOR','workload','visible-newest',jsonb_build_array('asset:' || $4::text),'linux','amd64','["landlock","seccomp","resource_limits"]',1,now()+interval '2 minutes'),
		($2,'RUN_SUPERVISOR','workload','hidden-middle',jsonb_build_array('asset:' || $4::text),'linux','amd64','[]',1,now()+interval '1 minute'),
		($3,'RUN_SUPERVISOR','workload','visible-oldest',jsonb_build_array('asset:' || $5::text),'linux','amd64','[]',1,now())`,
		visibleNewest, hiddenMiddle, visibleOldest, h.local, hiddenAsset); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id,subject_kind,subject_id,tools,bindings,created_by)
		VALUES
		($1,'worker',$2,'[]',jsonb_build_array(jsonb_build_object('kind','asset','id',$8::text)),$7),
		($3,'worker',$4,'[]',jsonb_build_array(jsonb_build_object('kind','asset','id',$9::text)),$7),
		($5,'worker',$6,'[]',jsonb_build_array(jsonb_build_object('kind','asset','id',$8::text)),$7)`,
		"grant-"+newTestSuffix(), visibleNewest, "grant-"+newTestSuffix(), hiddenMiddle,
		"grant-"+newTestSuffix(), visibleOldest, h.adminID, h.local, hiddenAsset); err != nil {
		t.Fatal(err)
	}

	server := NewWorkerServer(st.Pool(), mustKey(t), false)
	first, err := server.ListWorkers(ctx, bearerReq(h.adminTok, &workerv1.ListWorkersRequest{PageSize: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Msg.GetWorkers(); len(got) != 1 || got[0].GetWorkerId() != visibleNewest {
		t.Fatalf("first visible worker page = %v, want %s", got, visibleNewest)
	}
	if first.Msg.GetNextPageToken() == "" {
		t.Fatal("first visible worker page must advertise the remaining visible worker")
	}

	second, err := server.ListWorkers(ctx, bearerReq(h.adminTok, &workerv1.ListWorkersRequest{
		PageSize: 1, PageToken: first.Msg.GetNextPageToken(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Msg.GetWorkers(); len(got) != 1 || got[0].GetWorkerId() != visibleOldest {
		t.Fatalf("second visible worker page = %v, want %s", got, visibleOldest)
	}
	if second.Msg.GetNextPageToken() != "" {
		t.Fatal("hidden workers must not create another page token")
	}
}
