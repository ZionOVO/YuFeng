package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecuteSagaCompensate(t *testing.T) {
	var rec RunRecord
	err := Execute(context.Background(), []Step{
		{Name: "ok", Run: func(context.Context) error { return nil }, Compensate: func(context.Context) error { return nil }},
		{Name: "boom", Fail: true},
		{Name: "never"},
	}, false, &rec)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want boom, got %v", err)
	}
	got := strings.Join(rec.Events, ",")
	if !strings.Contains(got, "ok:ok") || !strings.Contains(got, "fail:boom") || !strings.Contains(got, "compensate:ok") {
		t.Fatalf("events=%s", got)
	}
}

func TestExecuteConsumesBudget(t *testing.T) {
	b := &CallBudget{Remaining: 1}
	var rec RunRecord
	err := Execute(context.Background(), []Step{
		{Name: "one", Budget: b},
		{Name: "two", Budget: b},
	}, false, &rec)
	if err == nil || !strings.Contains(err.Error(), "resource_exhausted") {
		t.Fatalf("got %v", err)
	}
}

func TestExecuteTTLCancels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	time.Sleep(10 * time.Millisecond)
	var rec RunRecord
	err := Execute(ctx, []Step{
		{Name: "ok", Run: func(context.Context) error { return nil }},
		{Name: "late"},
	}, false, &rec)
	if err == nil {
		t.Fatal("expired ttl must fail")
	}
}

func TestExecuteRejectsDangerousWithoutSandbox(t *testing.T) {
	var rec RunRecord
	err := Execute(context.Background(), []Step{{Name: "exec", Dangerous: true}}, false, &rec)
	if err == nil || !strings.Contains(err.Error(), "failed_precondition") {
		t.Fatalf("got %v", err)
	}
}

func TestExecuteCancelRunCompensates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var rec RunRecord
	errCh := make(chan error, 1)
	go func() {
		errCh <- Execute(ctx, []Step{
			{Name: "ok", Run: func(context.Context) error {
				close(started)
				return nil
			}, Compensate: func(context.Context) error { return nil }},
			{Name: "block", Run: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}},
		}, false, &rec)
	}()
	<-started
	cancel()
	err := <-errCh
	if err == nil {
		t.Fatal("cancel must fail the run")
	}
	got := strings.Join(rec.Events, ",")
	if !strings.Contains(got, "ok:ok") || !strings.Contains(got, "compensate:ok") {
		t.Fatalf("events=%s", got)
	}
}
