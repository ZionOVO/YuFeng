package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandlerPreservesRequestAndStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/status/418?a=1&a=2", strings.NewReader("payload"))
	req.Header.Add("X-Test", "one")
	rec := httptest.NewRecorder()
	handler("app-a").ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || rec.Header().Get("X-Upstream-Name") != "app-a" {
		t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
	}
	var got echoResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.Path != "/status/418" || got.RawQuery != "a=1&a=2" || got.Body != "payload" {
		t.Fatalf("response=%+v", got)
	}
}

func TestHandlerAppliesBoundedResponseDelay(t *testing.T) {
	start := time.Now()
	rec := httptest.NewRecorder()
	handler("slow-app").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/delay/20", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("elapsed=%s", elapsed)
	}
	if delay, ok := responseDelay("/delay/10001"); ok || delay != 0 {
		t.Fatalf("unbounded delay=%s ok=%t", delay, ok)
	}
}
