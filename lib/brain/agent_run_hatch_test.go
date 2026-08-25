package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/agents/runtime"

	agentv1 "yufeng/proto/gen/agentv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	runv1 "yufeng/proto/gen/runv1"
	userv1 "yufeng/proto/gen/userv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

var brainYufengRunBuild struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func TestCreateRunHatchesYufengRun(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	tok, priv := seedRunOperator(t, ctx, st.Pool())
	if _, err := st.Pool().Exec(ctx, `UPDATE work_items SET status='failed' WHERE status IN ('pending','leased')`); err != nil {
		t.Fatal(err)
	}
	runs := NewRunServer(st.Pool(), priv)
	create := connect.NewRequest(&runv1.CreateRunRequest{Role: "worker", PlanRef: "plan-hatch", Toolset: []string{"ping"}, Bindings: []string{"asset:any"}, Budget: "3", Ttl: "15s"})
	create.Header().Set("Authorization", "Bearer "+tok)
	created, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	if created.Msg.RunId == "" || created.Msg.State != "pending" {
		t.Fatalf("create: %+v", created.Msg)
	}
	waitRunViaWorker(t, ctx, st.Pool(), runs, tok, created.Msg.RunId, "succeeded", buildYufengRun(t), priv)
	kinds, err := ReconstructRunEvents(ctx, st.Pool(), created.Msg.RunId)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) == 0 {
		t.Fatal("audit ledger must reconstruct after hatch")
	}
}

func TestCreateRunWorkerFailCompensates(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	tok, priv := seedRunOperator(t, ctx, st.Pool())
	if _, err := st.Pool().Exec(ctx, `UPDATE work_items SET status='failed' WHERE status IN ('pending','leased')`); err != nil {
		t.Fatal(err)
	}
	runs := NewRunServer(st.Pool(), priv)
	create := connect.NewRequest(&runv1.CreateRunRequest{Role: "worker", PlanRef: "plan-fail", Toolset: []string{"ping"}, Bindings: []string{"asset:any"}, Budget: "3", Ttl: "15s"})
	create.Header().Set("Authorization", "Bearer "+tok)
	created, err := runs.CreateRun(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	waitRunViaWorker(t, ctx, st.Pool(), runs, tok, created.Msg.RunId, "failed", buildFailingYufengRun(t), priv)
}

func seedRunOperator(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, ed25519.PrivateKey) {
	t.Helper()
	admin := "run-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, pool, admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(pool, time.Hour, false, 8)
	users := NewUserServer(pool, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	opName := "run-op-" + newTestSuffix()
	mk := connect.NewRequest(&userv1.CreateUserRequest{Username: opName, Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR})
	mk.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	setTestIdempotency(mk)
	op, err := users.CreateUser(ctx, mk)
	if err != nil {
		t.Fatal(err)
	}
	opLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: opName, Password: "Operator123"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'user',$2,'["console.read","run.create"]','[{"kind":"asset","id":"any"}]','admin')`,
		"g-run-"+newTestSuffix(), op.Msg.User.UserId); err != nil {
		t.Fatal(err)
	}
	return opLogin.Msg.Token, priv
}

type hatchWorkClient struct {
	ws       *WorkerServer
	token    string
	workerID string
	failures chan string
}

func (c hatchWorkClient) Poll(ctx context.Context) (*workerv1.WorkItem, error) {
	req := connect.NewRequest(&workerv1.PollWorkRequest{WorkerId: c.workerID, LongPollSeconds: 1})
	req.Header().Set("Authorization", "Bearer "+c.token)
	resp, err := c.ws.PollWork(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetWork(), nil
}

func (c hatchWorkClient) Complete(ctx context.Context, workID, leaseID string, leaseEpoch int64, result, receipt string) error {
	req := connect.NewRequest(&workerv1.CompleteWorkRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch, ResultRef: result, Receipt: receipt})
	req.Header().Set("Authorization", "Bearer "+c.token)
	_, err := c.ws.CompleteWork(ctx, req)
	return err
}

func (c hatchWorkClient) Fail(ctx context.Context, workID, leaseID string, leaseEpoch int64, code, message string) error {
	if c.failures != nil {
		select {
		case c.failures <- code + ": " + message:
		default:
		}
	}
	req := connect.NewRequest(&workerv1.FailWorkRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch, ErrorCode: code, Message: message})
	req.Header().Set("Authorization", "Bearer "+c.token)
	_, err := c.ws.FailWork(ctx, req)
	return err
}

func (c hatchWorkClient) Extend(ctx context.Context, workID, leaseID string, leaseEpoch int64) (runtime.LeaseExtension, error) {
	req := connect.NewRequest(&workerv1.ExtendLeaseRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch})
	req.Header().Set("Authorization", "Bearer "+c.token)
	resp, err := c.ws.ExtendLease(ctx, req)
	if err != nil {
		return runtime.LeaseExtension{}, err
	}
	return runtime.LeaseExtension{CapabilityToken: resp.Msg.GetCapabilityToken(), CancelRequested: resp.Msg.GetCancelRequested()}, nil
}

func (c hatchWorkClient) Progress(ctx context.Context, workID, leaseID string, leaseEpoch int64, stage, payload string) error {
	req := connect.NewRequest(&workerv1.ReportProgressRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch, Stage: stage, PayloadRef: payload})
	req.Header().Set("Authorization", "Bearer "+c.token)
	_, err := c.ws.ReportProgress(ctx, req)
	return err
}

func (c hatchWorkClient) Saga(ctx context.Context, workID, leaseID string, leaseEpoch int64, progress runtime.SagaProgress) (runtime.SagaSnapshot, error) {
	req := connect.NewRequest(&workerv1.ReportProgressRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch})
	if progress.Plan != nil {
		req.Msg.SagaPlan = runtime.SagaPlanToProto(*progress.Plan)
	}
	if progress.Receipt != nil {
		req.Msg.SagaReceipt = runtime.SagaReceiptToProto(*progress.Receipt)
	}
	req.Header().Set("Authorization", "Bearer "+c.token)
	resp, err := c.ws.ReportProgress(ctx, req)
	if err != nil {
		return runtime.SagaSnapshot{}, err
	}
	return runtime.SagaSnapshotFromProto(resp.Msg.GetSagaSnapshot()), nil
}

func waitRunViaWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runs *RunServer, userTok, runID, want, bin string, priv ed25519.PrivateKey) {
	t.Helper()
	boot := "boot-hatch-" + newTestSuffix()
	worker := "worker-hatch-" + newTestSuffix()
	agents := NewAgentServer(pool, boot, priv)
	reg, err := agents.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{AgentId: worker, BootstrapToken: boot, AgentPublicKey: "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
		VALUES($1,'agent',$2,'[]'::jsonb,'[{"kind":"asset","id":"any"}]'::jsonb,'test')`,
		"g-hatch-"+newTestSuffix(), worker); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerServer(pool, priv, true)
	regReq := connect.NewRequest(&workerv1.RegisterWorkerRequest{WorkerId: worker, Bindings: []string{"asset:any"}})
	regReq.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := ws.RegisterWorker(ctx, regReq); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	failures := make(chan string, 1)
	go func() {
		_ = runtime.RunWorker(wctx, hatchWorkClient{ws: ws, token: reg.Msg.AccessToken, workerID: worker, failures: failures}, nil, nil, bin)
	}()
	deadline := time.Now().Add(20 * time.Second)
	lastFailure := ""
	for time.Now().Before(deadline) {
		select {
		case lastFailure = <-failures:
		default:
		}
		get := connect.NewRequest(&runv1.GetRunRequest{RunId: runID})
		get.Header().Set("Authorization", "Bearer "+userTok)
		got, err := runs.GetRun(ctx, get)
		if err != nil {
			t.Fatal(err)
		}
		if got.Msg.Run.State == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s want state %s via RunWorker; worker failure=%q", runID, want, lastFailure)
}

func buildYufengRun(t *testing.T) string {
	t.Helper()
	brainYufengRunBuild.once.Do(func() {
		_, file, _, ok := goruntime.Caller(0)
		if !ok {
			brainYufengRunBuild.err = errors.New("resolve yufeng-run test caller")
			return
		}
		brainYufengRunBuild.dir, brainYufengRunBuild.err = os.MkdirTemp("", "yufeng-brain-run-*")
		if brainYufengRunBuild.err != nil {
			return
		}
		root := filepath.Join(filepath.Dir(file), "../..")
		name := "yufeng-run"
		if goruntime.GOOS == "windows" {
			name += ".exe"
		}
		brainYufengRunBuild.path = filepath.Join(brainYufengRunBuild.dir, name)
		cmd := exec.Command("go", "build", "-o", brainYufengRunBuild.path, "./cmd/yufeng-run")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if raw, err := cmd.CombinedOutput(); err != nil {
			brainYufengRunBuild.err = fmt.Errorf("build yufeng-run: %w: %s", err, raw)
		}
	})
	if brainYufengRunBuild.err != nil {
		t.Fatal(brainYufengRunBuild.err)
	}
	return brainYufengRunBuild.path
}

func buildFailingYufengRun(t *testing.T) string {
	t.Helper()
	real := buildYufengRun(t)
	wrap := filepath.Join(t.TempDir(), "yufeng-run-fail")
	script := "#!/bin/sh\nexec \"" + real + "\" -fail \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrap
}
