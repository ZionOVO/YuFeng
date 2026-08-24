package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"yufeng/agents/modelgateway"
	agentv1 "yufeng/proto/gen/agentv1"
)

type memInstr struct {
	items      []*agentv1.AgentInstruction
	acks       []string
	cancel     context.CancelFunc
	extends    int
	extendResp *agentv1.ExtendInstructionLeaseResponse
}

func (m *memInstr) Poll(context.Context) ([]*agentv1.AgentInstruction, error) {
	out := m.items
	m.items = nil
	return out, nil
}

func (m *memInstr) Extend(context.Context, string, string, int64, string) (*agentv1.ExtendInstructionLeaseResponse, error) {
	m.extends++
	return m.extendResp, nil
}

func (m *memInstr) Ack(_ context.Context, id, _ string, _ int64, status, _ string) error {
	m.acks = append(m.acks, id+":"+status)
	if m.cancel != nil {
		m.cancel()
	}
	return nil
}

func TestRunInstructionsAcksHandle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cli := &memInstr{cancel: cancel, items: []*agentv1.AgentInstruction{{
		Kind: "SESSION_MESSAGE", PayloadRef: "ses-1", InstructionId: "i1", LeaseId: "l1", BudgetId: "b1",
		CapabilityToken: "cap-1", LeaseDeadline: timestamppb.New(time.Now().Add(time.Second)),
	}}}
	caller := &scriptCaller{fn: func(name, args string) (string, error) {
		if name != "session.reply" {
			return "", errors.New("unexpected " + name)
		}
		return `{"ok":true}`, nil
	}}
	_ = RunInstructions(ctx, staticProvider{content: `{"tool":"session.reply","args":{"session_id":"ses-1","content":"ok"}}`}, caller, cli, &AccessSession{Access: "a"})
	if len(cli.acks) != 1 || cli.acks[0] != "i1:acked" {
		t.Fatalf("acks=%v", cli.acks)
	}
}

type delayedReplyProvider struct {
	delay time.Duration
}

func (p delayedReplyProvider) Complete(ctx context.Context, _ modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	select {
	case <-ctx.Done():
		return modelgateway.ChatResponse{}, ctx.Err()
	case <-time.After(p.delay):
		return modelgateway.ChatResponse{Content: `{"tool":"session.reply","args":{"session_id":"ses-1","content":"ok"}}`}, nil
	}
}

type capabilityCaller struct {
	mu         sync.Mutex
	capability string
}

func (c *capabilityCaller) Invoke(_ context.Context, _, capabilityToken, _, _ string) (string, error) {
	c.mu.Lock()
	c.capability = capabilityToken
	c.mu.Unlock()
	return `{"ok":true}`, nil
}

func TestRunInstructionsRenewsLeaseAndUsesRotatedCapability(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	item := &agentv1.AgentInstruction{
		Kind: "SESSION_MESSAGE", PayloadRef: "ses-1", InstructionId: "i1", LeaseId: "l1", LeaseEpoch: 3,
		BudgetId: "b1", CapabilityToken: "cap-old", LeaseDeadline: timestamppb.New(time.Now().Add(40 * time.Millisecond)),
	}
	cli := &memInstr{
		items:  []*agentv1.AgentInstruction{item},
		cancel: cancel,
		extendResp: &agentv1.ExtendInstructionLeaseResponse{
			LeaseId: "l1", LeaseEpoch: 3, BudgetId: "b1", CapabilityToken: "cap-new",
			LeaseDeadline: timestamppb.New(time.Now().Add(time.Second)),
		},
	}
	caller := &capabilityCaller{}
	_ = RunInstructions(ctx, delayedReplyProvider{delay: 60 * time.Millisecond}, caller, cli, &AccessSession{Access: "a"})
	if cli.extends < 1 {
		t.Fatal("long instruction must extend its lease")
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if caller.capability != "cap-new" {
		t.Fatalf("tool used capability %q", caller.capability)
	}
}

func TestAccessSessionRefreshOnUnauth(t *testing.T) {
	s := &AccessSession{Refresh: "old", Renew: func(context.Context, string) (string, string, error) {
		return "new-access", "new-refresh", nil
	}}
	if !s.RefreshIfUnauth(context.Background(), errors.New("unauthenticated: expired")) {
		t.Fatal("should refresh")
	}
	if s.Access != "new-access" || s.Refresh != "new-refresh" {
		t.Fatalf("%+v", s)
	}
}

func TestAccessSessionSerializesOneRefreshForRejectedGeneration(t *testing.T) {
	var renews atomic.Int32
	release := make(chan struct{})
	s := &AccessSession{Access: "access-old", Refresh: "refresh-old"}
	s.Renew = func(_ context.Context, refresh string) (string, string, error) {
		if refresh != "refresh-old" {
			return "", "", fmt.Errorf("refresh=%q", refresh)
		}
		renews.Add(1)
		<-release
		return "access-new", "refresh-new", nil
	}
	var calls atomic.Int32
	var oldCalls atomic.Int32
	bothRejected := make(chan struct{})
	call := func(access string) error {
		calls.Add(1)
		if access == "access-old" {
			if oldCalls.Add(1) == 2 {
				close(bothRejected)
			}
			<-bothRejected
			return errors.New("unauthenticated: expired")
		}
		if access != "access-new" {
			return fmt.Errorf("access=%q", access)
		}
		return nil
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errs <- s.Call(context.Background(), "", call)
		}()
	}
	close(start)
	deadline := time.Now().Add(time.Second)
	for renews.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if renews.Load() != 1 {
		t.Fatalf("refresh calls=%d", renews.Load())
	}
	if calls.Load() != 4 {
		t.Fatalf("remote calls=%d", calls.Load())
	}
}

func TestAccessSessionDoesNotRetryNonAuthenticationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "unavailable", err: errors.New("unavailable")},
		{name: "expired business object", err: connect.NewError(connect.CodeFailedPrecondition, errors.New("evidence expired"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var renews, calls int
			s := &AccessSession{Access: "access", Refresh: "refresh", Renew: func(context.Context, string) (string, string, error) {
				renews++
				return "new-access", "new-refresh", nil
			}}
			err := s.Call(context.Background(), "", func(string) error {
				calls++
				return test.err
			})
			if !errors.Is(err, test.err) || calls != 1 || renews != 0 {
				t.Fatalf("err=%v calls=%d renews=%d", err, calls, renews)
			}
		})
	}
}

func TestAccessSessionRetriesRejectedCallOnlyOnce(t *testing.T) {
	var renews, calls int
	s := &AccessSession{Access: "access-old", Refresh: "refresh-old", Renew: func(context.Context, string) (string, string, error) {
		renews++
		return "access-new", "refresh-new", nil
	}}
	err := s.Call(context.Background(), "", func(string) error {
		calls++
		return connect.NewError(connect.CodeUnauthenticated, errors.New("still rejected"))
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated || calls != 2 || renews != 1 {
		t.Fatalf("err=%v calls=%d renews=%d", err, calls, renews)
	}
}

func TestAccessSessionPersistenceFailureClosesWithoutUsingOldRefreshAgain(t *testing.T) {
	persistErr := errors.New("disk full")
	var renews, calls int
	s := &AccessSession{Access: "access-old", Refresh: "refresh-old", Renew: func(context.Context, string) (string, string, error) {
		renews++
		return "", "", fmt.Errorf("%w: %w", ErrAccessRefreshPersistence, persistErr)
	}}
	call := func(string) error {
		calls++
		return errors.New("unauthenticated")
	}
	first := s.Call(context.Background(), "", call)
	second := s.Call(context.Background(), "", call)
	if !errors.Is(first, ErrAccessSessionFailed) || !errors.Is(first, persistErr) {
		t.Fatalf("first=%v", first)
	}
	if !errors.Is(second, ErrAccessSessionFailed) || renews != 1 || calls != 1 {
		t.Fatalf("second=%v renews=%d calls=%d", second, renews, calls)
	}
	access, refresh := s.Tokens()
	if access != "access-old" || refresh != "refresh-old" {
		t.Fatalf("tokens changed after failed persistence: %q %q", access, refresh)
	}
}
