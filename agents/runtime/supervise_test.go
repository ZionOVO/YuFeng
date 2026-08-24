package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

type fakeWork struct {
	mu           sync.Mutex
	extends      int
	failExtend   bool
	failProgress bool
	progress     []string
	saga         testSagaJournal
}

func (f *fakeWork) Poll(context.Context) (*workerv1.WorkItem, error) { return nil, nil }

func (f *fakeWork) Complete(context.Context, string, string, int64, string, string) error {
	return nil
}

func (f *fakeWork) Fail(context.Context, string, string, int64, string, string) error { return nil }

func (f *fakeWork) Extend(context.Context, string, string, int64) (LeaseExtension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extends++
	if f.failExtend {
		return LeaseExtension{}, fmt.Errorf("lease lost")
	}
	return LeaseExtension{CapabilityToken: "cap-rotated"}, nil
}

func (f *fakeWork) Progress(_ context.Context, _, _ string, _ int64, stage, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failProgress {
		return fmt.Errorf("progress unavailable")
	}
	f.progress = append(f.progress, stage)
	return nil
}

func (f *fakeWork) Saga(_ context.Context, _, _ string, _ int64, progress SagaProgress) (SagaSnapshot, error) {
	if progress.Plan != nil {
		return f.saga.BindSaga(*progress.Plan)
	}
	if progress.Receipt != nil {
		return f.saga.RecordSaga(*progress.Receipt)
	}
	return SagaSnapshot{}, fmt.Errorf("missing saga progress")
}

type captureTools struct {
	mu         sync.Mutex
	access     string
	accesses   []string
	capability string
	calls      int
	names      []string
}

func (t *captureTools) Invoke(_ context.Context, accessToken, capabilityToken, name, _ string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.access = accessToken
	t.accesses = append(t.accesses, accessToken)
	t.capability = capabilityToken
	t.calls++
	t.names = append(t.names, name)
	return `{"ok":true}`, nil
}

func TestSuperviseLostLeaseKillsChild(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	lost := make(chan struct{})
	done := make(chan HatchResult, 1)
	go func() {
		done <- Supervise(context.Background(), SuperviseConfig{
			Bin: "sleep", Args: []string{"30"}, TTL: time.Minute,
			WorkID: "work-lost", RunID: "run-lost", LeaseLost: lost,
		})
	}()
	time.Sleep(100 * time.Millisecond)
	close(lost)
	res := <-done
	time.Sleep(50 * time.Millisecond)
	if ProcessAlive(res.PID) {
		_ = KillProcessGroup(res.PID)
		t.Fatalf("child pid %d remained after lost lease", res.PID)
	}
}

func TestSuperviseExtendContinues(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	fw := &fakeWork{}
	res := Supervise(context.Background(), SuperviseConfig{
		Bin: "sleep", Args: []string{"0.15"}, TTL: time.Second,
		WorkID: "work-ext", RunID: "run-ext", Client: fw, ExtendEvery: 20 * time.Millisecond,
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "terminal broker receipt") {
		t.Fatalf("plain child must not look complete: %v", res.Err)
	}
	fw.mu.Lock()
	n := fw.extends
	fw.mu.Unlock()
	if n < 1 {
		t.Fatal("successful extend must run while child continues")
	}
}

func TestSuperviseLostOnExtendFailure(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	fw := &fakeWork{failExtend: true}
	res := Supervise(context.Background(), SuperviseConfig{
		Bin: "sleep", Args: []string{"30"}, TTL: time.Minute,
		WorkID: "work-ext-fail", RunID: "run-ext-fail", Client: fw, ExtendEvery: 50 * time.Millisecond,
	})
	time.Sleep(50 * time.Millisecond)
	if ProcessAlive(res.PID) {
		_ = KillProcessGroup(res.PID)
		t.Fatalf("child pid %d remained after extend failure", res.PID)
	}
}

func TestSuperviseBudgetBeforeSpawn(t *testing.T) {
	res := Supervise(context.Background(), SuperviseConfig{
		Bin: "sleep", Args: []string{"1"}, Budget: &CallBudget{Remaining: 0},
	})
	if res.Err == nil || res.Err.Error() != "resource_exhausted" {
		t.Fatalf("got %v", res.Err)
	}
	if res.PID != 0 {
		t.Fatal("must not spawn when budget exhausted")
	}
}

func TestSuperviseYufengRunOmitsCapability(t *testing.T) {
	bin := buildYufengRun(t)
	work := &fakeWork{}
	res := Supervise(context.Background(), SuperviseConfig{
		Bin: bin, WorkID: "work-cap", RunID: "run-cap", TTL: 8 * time.Second,
		CapabilityToken: "must-not-enter-child", Client: work,
	})
	if res.Err != nil {
		t.Fatalf("supervise yufeng-run: %v", res.Err)
	}
	for _, key := range res.EnvKeys {
		if isSecretEnv(key) {
			t.Fatalf("child env leaked %s", key)
		}
	}
	work.mu.Lock()
	seq := append([]string(nil), work.progress...)
	work.mu.Unlock()
	if len(seq) == 0 {
		t.Fatal("audit must reconstruct run sequence")
	}
	if !strings.Contains(strings.Join(seq, ","), "hello") {
		t.Fatalf("sequence=%v", seq)
	}
	if res.TerminalKind != "done" || res.TerminalPayload != "ok" {
		t.Fatalf("terminal=%s payload=%s", res.TerminalKind, res.TerminalPayload)
	}
}

func TestSuperviseInvestigationPassesFrozenInputThroughBrokerOnly(t *testing.T) {
	bin := buildYufengRun(t)
	work := &fakeWork{}
	tools := &captureTools{}
	ticket := &eventv1.CheckTicket{
		EventId: "event-supervised-investigation", AssetId: "asset-supervised-investigation",
		Forward:  commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE,
		Evidence: &eventv1.EvidenceProjection{Fields: map[string]string{"method": "GET"}},
	}
	digest, err := kernel.CheckTicketDigest(ticket)
	if err != nil {
		t.Fatal(err)
	}
	input := WorkInput{Purpose: "investigation", Ticket: ticket, TicketDigest: digest}
	res := Supervise(context.Background(), SuperviseConfig{
		Bin: bin, WorkID: "work-investigation", RunID: "run-investigation", TTL: 8 * time.Second,
		CapabilityToken: "investigation-capability", Client: work, Tools: tools, Input: input,
	})
	if res.Err != nil {
		t.Fatalf("supervise investigation: %v; terminal=%s payload=%s", res.Err, res.TerminalKind, res.TerminalPayload)
	}
	var receipt workerv1.InvestigationReceipt
	if err := protojson.Unmarshal([]byte(res.TerminalPayload), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInvestigationReceipt(input, &receipt); err != nil {
		t.Fatal(err)
	}
	tools.mu.Lock()
	names := append([]string(nil), tools.names...)
	tools.mu.Unlock()
	if strings.Join(names, ",") != "ticket.get" {
		t.Fatalf("investigation tools=%v", names)
	}
	for _, key := range res.EnvKeys {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "yufeng_") && (strings.Contains(lower, "ticket") || strings.Contains(lower, "event")) {
			t.Fatalf("investigation input leaked through environment key %s", key)
		}
	}
}

func TestBrokerUsesRotatedCapability(t *testing.T) {
	tools := &captureTools{}
	hub := &brokerHub{
		nonce:           "nonce",
		cfg:             &SuperviseConfig{Tools: tools, CapabilityToken: "stale"},
		capabilityToken: "stale",
	}
	hub.setCapability("rotated")
	reply := hub.handle(wireMsg{Op: "invoke", Nonce: "nonce", Tool: "event.get", Args: `{}`})
	if !reply.OK {
		t.Fatalf("invoke: %+v", reply)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.capability != "rotated" || tools.calls != 1 {
		t.Fatalf("capability=%q calls=%d", tools.capability, tools.calls)
	}
}

func TestBrokerReadsCurrentAccessTokenForEveryRequest(t *testing.T) {
	tools := &captureTools{}
	session := &AccessSession{Access: "access-old", Refresh: "refresh-old"}
	hub := &brokerHub{
		nonce: "nonce",
		cfg:   &SuperviseConfig{Tools: tools, AccessSession: session},
	}
	first := hub.handle(wireMsg{Op: "invoke", Nonce: "nonce", Tool: "event.get", Args: `{}`})
	session.SetTokens("access-new", "refresh-new")
	second := hub.handle(wireMsg{Op: "invoke", Nonce: "nonce", Tool: "event.get", Args: `{}`})
	if !first.OK || !second.OK {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if strings.Join(tools.accesses, ",") != "access-old,access-new" {
		t.Fatalf("access generations=%v", tools.accesses)
	}
}

func TestBrokerRejectsEffectWhenProgressCannotPersist(t *testing.T) {
	work := &fakeWork{failProgress: true}
	tools := &captureTools{}
	hub := &brokerHub{
		nonce: "nonce",
		cfg:   &SuperviseConfig{WorkID: "work", Client: work, Tools: tools},
	}
	reply := hub.handle(wireMsg{Op: "invoke", Nonce: "nonce", Tool: "event.get", Args: `{}`})
	if reply.OK || !strings.Contains(reply.Error, "report progress") {
		t.Fatalf("invoke must fail closed: %+v", reply)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.calls != 0 {
		t.Fatalf("tool ran without durable progress: calls=%d", tools.calls)
	}
}

func TestBrokerAcceptsOneTerminalReceipt(t *testing.T) {
	hub := &brokerHub{nonce: "nonce", cfg: &SuperviseConfig{}}
	first := hub.handle(wireMsg{Op: "done", Nonce: "nonce", Payload: "result"})
	second := hub.handle(wireMsg{Op: "fail", Nonce: "nonce", Payload: "late"})
	if !first.OK || second.OK || !strings.Contains(second.Error, "already recorded") {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	kind, payload := hub.terminal()
	if kind != "done" || payload != "result" {
		t.Fatalf("terminal=%s payload=%s", kind, payload)
	}
}
