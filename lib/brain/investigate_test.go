package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	runv1 "yufeng/proto/gen/runv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestEnqueueInvestigationMutexAndReadOnly(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	eid := "evt-inv-" + newTestSuffix()
	cid := "clu-inv-" + newTestSuffix()
	ticket, digest := seedFrozenInvestigationTicket(t, ctx, st.Pool(), eid, "asset-inv", cid)
	id1, created1, err := EnqueueInvestigation(ctx, st.Pool(), ticket)
	if err != nil || !created1 || id1 == "" {
		t.Fatalf("first enqueue: id=%s created=%v err=%v", id1, created1, err)
	}
	id2, created2, err := EnqueueInvestigation(ctx, st.Pool(), ticket)
	if err != nil || created2 || id2 != id1 {
		t.Fatalf("same cluster must reuse run: id=%s created=%v err=%v want %s", id2, created2, err, id1)
	}
	var toolsJSON, bindingsJSON, createdBy string
	var nInstr, nRuns int
	if err := st.Pool().QueryRow(ctx, `SELECT toolset::text, bindings::text, created_by FROM runs WHERE run_id=$1`, id1).
		Scan(&toolsJSON, &bindingsJSON, &createdBy); err != nil {
		t.Fatal(err)
	}
	var tools, bindings []string
	if err := json.Unmarshal([]byte(toolsJSON), &tools); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(bindingsJSON), &bindings); err != nil {
		t.Fatal(err)
	}
	if hasGovernTool(tools) {
		t.Fatalf("investigation tools must be read-only, got %v", tools)
	}
	if strings.Join(tools, ",") != "ticket.get,cluster.get" {
		t.Fatalf("investigation toolset=%v", tools)
	}
	if createdBy != investigateCreatedBy {
		t.Fatalf("created_by=%s", createdBy)
	}
	joined := strings.Join(bindings, ",")
	if !strings.Contains(joined, "asset:asset-inv") || !strings.Contains(joined, "cluster:"+cid) {
		t.Fatalf("bindings=%v", bindings)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions`).Scan(&nInstr); err != nil {
		t.Fatal(err)
	}
	if nInstr != 0 {
		t.Fatalf("investigation must not enqueue jarvis instructions, got %d", nInstr)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM runs WHERE plan_ref=$1`, investigationPlanRef(cid, eid)).Scan(&nRuns); err != nil {
		t.Fatal(err)
	}
	if nRuns != 1 {
		t.Fatalf("want 1 run, got %d", nRuns)
	}
	var eventID, storedDigest string
	var deadline time.Time
	if err := st.Pool().QueryRow(ctx, `SELECT w.investigation_event_id, w.investigation_ticket_digest, r.deadline
		FROM work_items w JOIN runs r USING(run_id) WHERE w.run_id=$1`, id1).Scan(&eventID, &storedDigest, &deadline); err != nil {
		t.Fatal(err)
	}
	if eventID != eid || storedDigest != digest || !deadline.After(time.Now()) {
		t.Fatalf("investigation coordinates event=%s digest=%s deadline=%s", eventID, storedDigest, deadline)
	}
}

func TestInvestigationWorkConsumesFrozenTicketAndPersistsEveryTerminalReceipt(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	assetID := "asset-investigation-terminal"
	clusterID := "cluster-investigation-terminal"
	ticket, digest := seedFrozenInvestigationTicket(t, ctx, st.Pool(), "event-investigation-terminal", assetID, clusterID)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publishTestToolDescriptors(t, ctx, st.Pool(), key, "ticket.get", "cluster.get")
	gateway := NewToolGatewayServer(st.Pool(), key)
	workers, workerID, workerToken := registerInvestigationWorker(t, ctx, st.Pool(), key, assetID, clusterID)

	runID, _, err := EnqueueInvestigation(ctx, st.Pool(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	work := pollInvestigationWork(t, ctx, workers, workerID, workerToken)
	if work.GetRunId() != runID || work.GetInvestigationInput().GetTicketDigest() != digest || work.GetInvestigationInput().GetTicket().GetEventId() != ticket.GetEventId() {
		t.Fatalf("leased input=%#v", work.GetInvestigationInput())
	}
	ticketResult := invokeLeasedInvestigationTool(t, ctx, gateway, workerToken, work, "ticket.get",
		`{"event_id":"`+ticket.GetEventId()+`","ticket_digest":"`+digest+`"}`)
	clusterResult := invokeLeasedInvestigationTool(t, ctx, gateway, workerToken, work, "cluster.get",
		`{"cluster_id":"`+clusterID+`"}`)
	reads := []*workerv1.InvestigationToolRead{
		{ToolName: "ticket.get", ResultDigest: auditPayloadDigest(ticketResult)},
		{ToolName: "cluster.get", ResultDigest: auditPayloadDigest(clusterResult)},
	}
	receipt := &workerv1.InvestigationReceipt{
		EventId: ticket.GetEventId(), TicketDigest: digest, Status: "succeeded", Reads: reads,
		OutputDigest: kernel.InvestigationOutputDigest(reads),
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	bad := protojson.Format(&workerv1.InvestigationReceipt{
		EventId: ticket.GetEventId(), TicketDigest: digest, Status: "succeeded",
		Reads:        []*workerv1.InvestigationToolRead{{ToolName: "govern.propose", ResultDigest: "sha256:" + strings.Repeat("f", 64)}},
		OutputDigest: "sha256:" + strings.Repeat("0", 64),
	})
	badComplete := workerRequestWithToken(workerToken, &workerv1.CompleteWorkRequest{
		WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(),
		ResultRef: "sha256:" + strings.Repeat("0", 64), Receipt: bad,
	})
	if _, err := workers.CompleteWork(ctx, badComplete); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("forged write-tool receipt must fail closed: %v", err)
	}
	var forged int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM investigation_receipts WHERE run_id=$1`, runID).Scan(&forged); err != nil || forged != 0 {
		t.Fatalf("forged receipt persisted count=%d err=%v", forged, err)
	}
	forgedReads := []*workerv1.InvestigationToolRead{
		{ToolName: "ticket.get", ResultDigest: "sha256:" + strings.Repeat("d", 64)},
		{ToolName: "cluster.get", ResultDigest: "sha256:" + strings.Repeat("e", 64)},
	}
	forgedLedgerReceipt := &workerv1.InvestigationReceipt{
		EventId: ticket.GetEventId(), TicketDigest: digest, Status: "succeeded", Reads: forgedReads,
		OutputDigest: kernel.InvestigationOutputDigest(forgedReads),
	}
	forgedLedgerRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(forgedLedgerReceipt)
	if err != nil {
		t.Fatal(err)
	}
	forgedLedgerComplete := workerRequestWithToken(workerToken, &workerv1.CompleteWorkRequest{
		WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(),
		ResultRef: forgedLedgerReceipt.GetOutputDigest(), Receipt: string(forgedLedgerRaw),
	})
	if _, err := workers.CompleteWork(ctx, forgedLedgerComplete); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("forged read digest must fail against audit ledger: %v", err)
	}
	complete := workerRequestWithToken(workerToken, &workerv1.CompleteWorkRequest{
		WorkId: work.GetWorkId(), LeaseId: work.GetLeaseId(), LeaseEpoch: work.GetLeaseEpoch(),
		ResultRef: receipt.GetOutputDigest(), Receipt: string(raw),
	})
	if _, err := workers.CompleteWork(ctx, complete); err != nil {
		t.Fatal(err)
	}
	assertInvestigationReceipt(t, ctx, st.Pool(), runID, "succeeded", digest)

	failedRun, _, err := EnqueueInvestigation(ctx, st.Pool(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	failedWork := pollInvestigationWork(t, ctx, workers, workerID, workerToken)
	fail := workerRequestWithToken(workerToken, &workerv1.FailWorkRequest{
		WorkId: failedWork.GetWorkId(), LeaseId: failedWork.GetLeaseId(), LeaseEpoch: failedWork.GetLeaseEpoch(),
		ErrorCode: "projection_unavailable", Message: "read-only projection unavailable",
	})
	if _, err := workers.FailWork(ctx, fail); err != nil {
		t.Fatal(err)
	}
	assertInvestigationReceipt(t, ctx, st.Pool(), failedRun, "failed", digest)
	var storedError string
	if err := st.Pool().QueryRow(ctx, `SELECT error FROM runs WHERE run_id=$1`, failedRun).Scan(&storedError); err != nil {
		t.Fatal(err)
	}
	if storedError != auditPayloadDigest(fail.Msg.GetMessage()) || strings.Contains(storedError, fail.Msg.GetMessage()) {
		t.Fatalf("investigation failure leaked message: %q", storedError)
	}

	cancelledRun, _, err := EnqueueInvestigation(ctx, st.Pool(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	adminToken := investigationAdminToken(t, ctx, st.Pool(), assetID)
	runs := NewRunServer(st.Pool(), key)
	cancel := connect.NewRequest(&runv1.CancelRunRequest{RunId: cancelledRun})
	cancel.Header().Set("Authorization", "Bearer "+adminToken)
	if _, err := runs.CancelRun(ctx, cancel); err != nil {
		t.Fatal(err)
	}
	assertInvestigationReceipt(t, ctx, st.Pool(), cancelledRun, "cancelled", digest)

	timedOutRun, _, err := EnqueueInvestigation(ctx, st.Pool(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE runs SET deadline=now()-interval '1 second' WHERE run_id=$1`, timedOutRun); err != nil {
		t.Fatal(err)
	}
	if expired, err := expireRunDeadline(ctx, st.Pool(), timedOutRun, time.Now()); err != nil || !expired {
		t.Fatalf("expire=%v err=%v", expired, err)
	}
	assertInvestigationReceipt(t, ctx, st.Pool(), timedOutRun, "timeout", digest)
}

func TestInvestigationWorkRequiresVerifiedPlatformSandbox(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	assetID := "asset-investigation-sandbox-" + newTestSuffix()
	clusterID := "cluster-investigation-sandbox-" + newTestSuffix()
	ticket, _ := seedFrozenInvestigationTicket(t, ctx, st.Pool(), "event-investigation-sandbox-"+newTestSuffix(), assetID, clusterID)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	workers, workerID, _ := registerInvestigationWorker(t, ctx, st.Pool(), key, assetID, clusterID)
	if _, _, err := EnqueueInvestigation(ctx, st.Pool(), ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE workers SET sandbox_capabilities='["landlock","resource_limits"]'::jsonb WHERE worker_id=$1`, workerID); err != nil {
		t.Fatal(err)
	}
	work, err := workers.leaseWork(ctx, workerID)
	if err != nil {
		t.Fatal(err)
	}
	if work != nil {
		t.Fatalf("worker without complete verified sandbox leased investigation work %s", work.GetWorkId())
	}
}

func TestEnqueueInvestigationRejectsTicketThatDiffersFromFrozenRow(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	ticket, _ := seedFrozenInvestigationTicket(t, ctx, st.Pool(), "event-investigation-mismatch", "asset-investigation-mismatch", "")
	ticket.Method = "POST"
	if _, _, err := EnqueueInvestigation(ctx, st.Pool(), ticket); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered ticket must fail closed: %v", err)
	}
}

func TestTicketGetReturnsOnlyMatchingFrozenProjection(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	ticket, digest := seedFrozenInvestigationTicket(t, ctx, st.Pool(), "event-ticket-get", "asset-ticket-get", "")
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewToolGatewayServer(st.Pool(), key)
	gateway.demoTriage = true
	token := signToolToken(t, key, "investigator", []string{"ticket.get"}, []string{assetBinding(ticket.GetAssetId())})
	response, err := invoke(ctx, gateway, token, "ticket.get", `{"event_id":"`+ticket.GetEventId()+`","ticket_digest":"`+digest+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Msg.GetResultJson(), digest) || strings.Contains(response.Msg.GetResultJson(), "query") {
		t.Fatalf("ticket projection=%s", response.Msg.GetResultJson())
	}
	if _, err := invoke(ctx, gateway, token, "ticket.get", `{"event_id":"`+ticket.GetEventId()+`","ticket_digest":"sha256:`+strings.Repeat("0", 64)+`"}`); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong digest must be denied: %v", err)
	}
}

func seedFrozenInvestigationTicket(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID, assetID, clusterID string) (*eventv1.CheckTicket, string) {
	t.Helper()
	ticket := &eventv1.CheckTicket{
		EventId: eventID, AssetId: assetID, Method: "GET", RouteTemplate: "/investigate",
		Forward:  commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE,
		Evidence: &eventv1.EvidenceProjection{Fields: map[string]string{"method": "GET", "span_hash": "ab"}},
	}
	digest, err := kernel.CheckTicketDigest(ticket)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier)
		VALUES($1,$1,'L1') ON CONFLICT(asset_id) DO NOTHING`, assetID); err != nil {
		t.Fatal(err)
	}
	eventRaw, err := protojson.Marshal(&eventv1.Event{Id: eventID, AssetId: assetID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events(event_id, occurred_at, asset_id, kind, verdict, payload, cluster_id)
		VALUES($1,now(),$2,'KIND_TRAFFIC','allow',$3::jsonb,$4)`, eventID, assetID, string(eventRaw), clusterID); err != nil {
		t.Fatal(err)
	}
	seedEventReceiptForTest(t, ctx, pool, eventID)
	if clusterID != "" {
		eventIDs, err := json.Marshal([]string{eventID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO triage_clusters(
			cluster_id, asset_id, route_template, method, identity_key, reason, event_ids, representative)
			VALUES($1,$2,'/investigate','GET',$3,'TRIAGE_REASON_DETECTED_UNMITIGATED',$4::jsonb,$5)`,
			clusterID, assetID, "investigation:"+clusterID, string(eventIDs), eventID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO check_tickets(
		event_id, status, ticket, ticket_digest, forward_policy) VALUES($1,'ready',$2::jsonb,$3,$4)`,
		eventID, string(raw), digest, ticket.GetForward().String()); err != nil {
		t.Fatal(err)
	}
	return ticket, digest
}

func registerInvestigationWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key ed25519.PrivateKey, assetID, clusterID string) (*WorkerServer, string, string) {
	t.Helper()
	workerID := "worker-investigation-" + newTestSuffix()
	bootstrap := "bootstrap-investigation-" + newTestSuffix()
	agents := NewAgentServer(pool, bootstrap, key)
	registered, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: workerID, BootstrapToken: bootstrap, AgentPublicKey: "investigation-worker-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := json.Marshal([]map[string]string{{"kind": "asset", "id": assetID}, {"kind": "cluster", "id": clusterID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]',$3::jsonb,'test')`, "grant-investigation-worker-"+newTestSuffix(), workerID, string(bindings)); err != nil {
		t.Fatal(err)
	}
	workers := NewWorkerServer(pool, key, true)
	request := connect.NewRequest(&workerv1.RegisterWorkerRequest{
		WorkerId: workerID, OperatingSystem: "linux", Architecture: "amd64",
		SandboxCapabilities: []string{"landlock", "seccomp", "resource_limits"},
	})
	request.Header().Set("Authorization", "Bearer "+registered.Msg.GetAccessToken())
	if _, err := workers.RegisterWorker(ctx, request); err != nil {
		t.Fatal(err)
	}
	return workers, workerID, registered.Msg.GetAccessToken()
}

func pollInvestigationWork(t *testing.T, ctx context.Context, workers *WorkerServer, workerID, token string) *workerv1.WorkItem {
	t.Helper()
	request := workerRequestWithToken(token, &workerv1.PollWorkRequest{WorkerId: workerID, LongPollSeconds: 1})
	response, err := workers.PollWork(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetWork() == nil {
		t.Fatal("investigation work was not leased")
	}
	return response.Msg.GetWork()
}

func invokeLeasedInvestigationTool(t *testing.T, ctx context.Context, gateway *ToolGatewayServer, accessToken string,
	work *workerv1.WorkItem, tool, args string) string {
	t.Helper()
	request := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: tool, ArgsJson: args})
	request.Header().Set("Authorization", "Bearer "+accessToken)
	request.Header().Set(CapabilityHeader, "Bearer "+work.GetCapabilityToken())
	response, err := gateway.InvokeTool(ctx, request)
	if err != nil {
		t.Fatalf("invoke %s: %v", tool, err)
	}
	return response.Msg.GetResultJson()
}

func workerRequestWithToken[T any](token string, msg *T) *connect.Request[T] {
	request := connect.NewRequest(msg)
	request.Header().Set("Authorization", "Bearer "+token)
	return request
}

func investigationAdminToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, assetID string) string {
	t.Helper()
	username := "investigation-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, pool, username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `SELECT user_id FROM users WHERE username=$1`, username).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	bindings, _ := json.Marshal([]map[string]string{{"kind": "asset", "id": assetID}})
	if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'user',$2,'["run.create"]',$3::jsonb,'test')`, "grant-investigation-admin-"+newTestSuffix(), userID, string(bindings)); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, pool, OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(pool, time.Hour, false, MinPasswordLength)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: username, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	return login.Msg.GetToken()
}

func assertInvestigationReceipt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID, wantStatus, wantDigest string) {
	t.Helper()
	var status, digest string
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT status, ticket_digest, receipt FROM investigation_receipts WHERE run_id=$1`, runID).
		Scan(&status, &digest, &raw); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || digest != wantDigest || !json.Valid(raw) {
		t.Fatalf("receipt status=%s digest=%s raw=%s", status, digest, raw)
	}
}
