package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunnerCountsResponsesAndPercentiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	r := runner{client: &http.Client{Timeout: time.Second}, url: server.URL}
	got := r.run(100, 8, 0)
	if got.Completed != 100 || got.TransportErrors != 0 || got.StatusCounts["204"] != 100 {
		t.Fatalf("result=%+v", got)
	}
	if got.P50Micros < 0 || got.P99Micros < got.P50Micros || got.MaxMicros < got.P99Micros {
		t.Fatalf("latencies=%+v", got)
	}
}

func TestRunnerPacesTargetRate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	r := runner{client: &http.Client{Timeout: time.Second}, url: server.URL}
	started := time.Now()
	got := r.run(20, 4, 100)
	if elapsed := time.Since(started); elapsed < 170*time.Millisecond {
		t.Fatalf("target rate was not paced: elapsed=%s result=%+v", elapsed, got)
	}
	if got.TargetRate != 100 || got.Completed != 20 {
		t.Fatalf("result=%+v", got)
	}
}

func TestPercentileUsesNearestRank(t *testing.T) {
	samples := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(samples, 0.50); got != 5 {
		t.Fatalf("p50=%d", got)
	}
	if got := percentile(samples, 0.99); got != 10 {
		t.Fatalf("p99=%d", got)
	}
}

func TestFrozenBudgetsExposeModelBypassLatency(t *testing.T) {
	budgets := frozenBudgets()
	if budgets.ModelBypassP99Micros != time.Millisecond.Microseconds() {
		t.Fatalf("model bypass p99 budget=%d", budgets.ModelBypassP99Micros)
	}
}
