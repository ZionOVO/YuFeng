package brain

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "yufeng/proto/gen/agentv1"
	runv1 "yufeng/proto/gen/runv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestCreateRunClipsCallerProposal(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	tok, priv := seedRunOperator(t, ctx, st.Pool())
	runs := NewRunServer(st.Pool(), priv)
	req := connect.NewRequest(&runv1.CreateRunRequest{
		Role: "admin", PlanRef: "plan-x",
		Toolset:  []string{"ping", "govern.promote_enforce"},
		Bindings: []string{"asset:any", "asset:other"},
		Budget:   "9999", Ttl: "24h", CreatedBy: "attacker",
	})
	req.Header().Set("Authorization", "Bearer "+tok)
	if _, err := runs.CreateRun(ctx, req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("over-scope bindings want permission_denied got %v", err)
	}

	ok := connect.NewRequest(&runv1.CreateRunRequest{
		Role: "admin", PlanRef: "plan-x",
		Toolset:  []string{"ping", "govern.promote_enforce"},
		Bindings: []string{"asset:any"},
		Budget:   "9999", Ttl: "24h", CreatedBy: "attacker",
	})
	ok.Header().Set("Authorization", "Bearer "+tok)
	created, err := runs.CreateRun(ctx, ok)
	if err != nil {
		t.Fatal(err)
	}
	var role, budget, createdBy string
	var toolset []byte
	if err := st.Pool().QueryRow(ctx, `SELECT role, toolset, budget, created_by FROM runs WHERE run_id=$1`,
		created.Msg.RunId).Scan(&role, &toolset, &budget, &createdBy); err != nil {
		t.Fatal(err)
	}
	if role != "worker" {
		t.Fatalf("role=%s", role)
	}
	if !strings.Contains(string(toolset), "ping") || strings.Contains(string(toolset), "govern.promote_enforce") {
		t.Fatalf("toolset=%s", toolset)
	}
	if budget != "100" {
		t.Fatalf("budget=%s", budget)
	}
	if createdBy == "attacker" {
		t.Fatal("created_by must be authenticated identity")
	}
}

func TestPollWorkRequiresWorkerProfileSubset(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	tok, priv := seedRunOperator(t, ctx, st.Pool())
	runs := NewRunServer(st.Pool(), priv)
	create := connect.NewRequest(&runv1.CreateRunRequest{Role: "worker", Toolset: []string{"ping"}, Bindings: []string{"asset:any"}, Budget: "3", Ttl: "15s"})
	create.Header().Set("Authorization", "Bearer "+tok)
	if _, err := runs.CreateRun(ctx, create); err != nil {
		t.Fatal(err)
	}
	boot := "boot-prof-" + newTestSuffix()
	worker := "worker-prof-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: worker, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerServer(st.Pool(), priv, true)
	got, err := ws.leaseWork(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("unregistered worker must not lease")
	}
	regW := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: worker, Bindings: []string{"asset:any"}})
	regW.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.RegisterWorker(ctx, regW); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := st.Pool().QueryRow(ctx, `SELECT bindings FROM workers WHERE worker_id=$1`, worker).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "asset:any") {
		t.Fatalf("register must ignore self-reported bindings: %s", stored)
	}
	empty, err := ws.leaseWork(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	if empty != nil {
		t.Fatal("empty registered profile must not lease")
	}
	grantID := "g-prof-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]','[{"kind":"asset","id":"any"}]','system')`, grantID, worker); err != nil {
		t.Fatal(err)
	}
	leased, err := ws.leaseWork(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	if leased == nil {
		t.Fatal("covering worker profile must lease")
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE work_items SET status='pending', worker_id='', lease_id='', lease_deadline=NULL WHERE work_id=$1`, leased.GetWorkId()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE grants SET bindings='[{"kind":"asset","id":"other"}]'::jsonb WHERE grant_id=$1`, grantID); err != nil {
		t.Fatal(err)
	}
	got, err = ws.leaseWork(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("non-covering worker profile must not lease")
	}
}

func TestRunWorkerConcurrencyUsesApprovedServerCapacityAndExpiresIt(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	userToken, key := seedRunOperator(t, ctx, st.Pool())
	runs := NewRunServer(st.Pool(), key)
	createRun := func() string {
		t.Helper()
		request := connect.NewRequest(&runv1.CreateRunRequest{
			Role: "worker", Toolset: []string{"ping"}, Bindings: []string{"asset:any"}, Budget: "3", Ttl: "30s",
		})
		request.Header().Set("Authorization", "Bearer "+userToken)
		created, err := runs.CreateRun(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		return created.Msg.GetRunId()
	}
	createRun()
	createRun()
	workerID := "worker-capacity-" + newTestSuffix()
	registerBudgetRunWorker(t, ctx, st.Pool(), key, workerID)
	workers := NewWorkerServer(st.Pool(), key, true)
	first, err := workers.leaseWork(ctx, workerID)
	if err != nil || first == nil {
		t.Fatalf("first lease=%v err=%v", first, err)
	}
	if second, err := workers.leaseWork(ctx, workerID); err != nil || second != nil {
		t.Fatalf("capacity one must queue second work: second=%v err=%v", second, err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE workers SET max_concurrency=2 WHERE worker_id=$1`, workerID); err != nil {
		t.Fatal(err)
	}
	second, err := workers.leaseWork(ctx, workerID)
	if err != nil || second == nil {
		t.Fatalf("approved capacity two must lease second work: second=%v err=%v", second, err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE work_items SET status='completed', lease_deadline=NULL WHERE work_id=ANY($1)`,
		[]string{first.GetWorkId(), second.GetWorkId()}); err != nil {
		t.Fatal(err)
	}
	createRun()
	createRun()
	third, err := workers.leaseWork(ctx, workerID)
	if err != nil || third == nil {
		t.Fatalf("third lease=%v err=%v", third, err)
	}
	assetID := "asset-capacity-" + newTestSuffix()
	caseID := "case-capacity-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'open',80,'capacity case')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	changeID := "capacity-expired-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO worker_capacity_changes(change_id, case_id, worker_id, requested_by,
		requested_capacity, previous_capacity, state, expires_at, decided_by, decided_at)
		VALUES($1,$2,$3,'jarvis',2,1,'approved',now()-interval '1 second','operator',now())`,
		changeID, caseID, workerID); err != nil {
		t.Fatal(err)
	}
	if fourth, err := workers.leaseWork(ctx, workerID); err != nil || fourth != nil {
		t.Fatalf("expired capacity must restore one and queue fourth work: fourth=%v err=%v", fourth, err)
	}
	var capacity int
	var state string
	if err := st.Pool().QueryRow(ctx, `SELECT w.max_concurrency, c.state FROM workers w
		JOIN worker_capacity_changes c ON c.worker_id=w.worker_id WHERE w.worker_id=$1 AND c.change_id=$2`,
		workerID, changeID).Scan(&capacity, &state); err != nil {
		t.Fatal(err)
	}
	if capacity != 1 || state != "expired" {
		t.Fatalf("capacity=%d change=%s want 1/expired", capacity, state)
	}
}

func TestRegisterWorkerCopiesAgentGrantBindings(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv := seedRunOperator(t, ctx, st.Pool())
	boot := "boot-ag-" + newTestSuffix()
	worker := "worker-ag-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: worker, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]','[{"kind":"asset","id":"any"}]','system')`, "g-ag-"+newTestSuffix(), worker); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerServer(st.Pool(), priv, true)
	req := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: worker, Bindings: []string{"asset:other"}})
	req.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.RegisterWorker(ctx, req); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := st.Pool().QueryRow(ctx, `SELECT bindings FROM workers WHERE worker_id=$1`, worker).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), "asset:any") || strings.Contains(string(stored), "asset:other") {
		t.Fatalf("bindings=%s", stored)
	}
}

func TestRegisterWorkerRejectsForeignID(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	tok, priv := seedRunOperator(t, ctx, st.Pool())
	_ = tok
	boot := "boot-wid-" + newTestSuffix()
	agentID := "agent-wid-" + newTestSuffix()
	agents := NewAgentServer(st.Pool(), boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerServer(st.Pool(), priv, true)
	req := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: "other-worker", Bindings: []string{"asset:any"}})
	req.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.RegisterWorker(ctx, req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("foreign worker_id want permission_denied got %v", err)
	}
}
