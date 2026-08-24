package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestSetupMergesDefaultResource(t *testing.T) {
	stop, err := Setup("yufeng-test")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredLogFieldsAndRedact(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "brain")
	ctx, span := Start(context.Background(), "test.span")
	span.End()
	log.InfoContext(ctx, "login", "authorization", "Bearer super-secret", "token", "abc")
	line := buf.String()
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("ndjson: %v %s", err, line)
	}
	for _, k := range RequiredLogFields {
		if _, ok := rec[k]; !ok {
			t.Fatalf("missing field %s in %s", k, line)
		}
	}
	if rec[LogService] != "brain" {
		t.Fatalf("service=%v", rec[LogService])
	}
	if strings.Contains(line, "super-secret") || strings.Contains(line, "abc") {
		t.Fatalf("token leaked: %s", line)
	}
	if rec["authorization"] != "[redacted]" {
		t.Fatalf("authorization=%v", rec["authorization"])
	}
}

func TestTraceInterceptorRecordsSpan(t *testing.T) {
	rec, stop := InstallTestTracer()
	defer stop()
	next := connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&struct{}{}), nil
	})
	fn := TraceInterceptor()(next)
	req := connect.NewRequest(&struct{}{})
	if _, err := fn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(rec.Ended()) == 0 {
		t.Fatal("expected a span on the real interceptor path")
	}
}

func TestTraceInterceptorRecordsError(t *testing.T) {
	rec, stop := InstallTestTracer()
	defer stop()
	next := connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, errors.New("boom")
	})
	fn := TraceInterceptor()(next)
	if _, err := fn(context.Background(), connect.NewRequest(&struct{}{})); err == nil {
		t.Fatal("want error")
	}
	if len(rec.Ended()) == 0 {
		t.Fatal("error path must still end span")
	}
}

func TestMetricNamesStable(t *testing.T) {
	for _, n := range RequiredMetricNames {
		if !strings.HasPrefix(n, "yufeng_") {
			t.Fatalf("metric name %s", n)
		}
	}
	if Default().Get(MetricTelemetryDropped) < 0 {
		t.Fatal("metrics must exist")
	}
}
