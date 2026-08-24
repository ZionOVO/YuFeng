package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yufeng/lib/kernel"
)

func TestAuthorizationFixtureSemantics(t *testing.T) {
	handler := authorizationHandler()
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		partial    bool
		wantStatus int
	}{
		{name: "allow", method: http.MethodGet, target: "/allow", wantStatus: http.StatusOK},
		{name: "deny", method: http.MethodGet, target: "/deny", wantStatus: http.StatusForbidden},
		{name: "missing body", method: http.MethodPost, target: "/body-required", wantStatus: http.StatusOK},
		{name: "body policy", method: http.MethodPost, target: "/body-required", body: "deny", wantStatus: http.StatusForbidden},
		{name: "partial body", method: http.MethodPost, target: "/body-required", body: strings.Repeat("x", kernel.EngineBodyLimitBytes), partial: true, wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if tt.partial {
				req.Header.Set("X-Envoy-Auth-Partial-Body", "true")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d want=%d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestAuthorizationFixtureRequiresGatewayInput(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/required-headers?first=1&first=2", strings.NewReader("payload"))
	req.Host = "protected.example"
	for name, value := range map[string]string{
		"Content-Type":      "text/plain",
		"X-Forwarded-For":   "192.0.2.10",
		"X-Forwarded-Proto": "https",
		"X-Request-Id":      "gateway-generated",
	} {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	authorizationHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=200", rec.Code)
	}
}

func TestApplicationFixtureMarksReachedRequests(t *testing.T) {
	rec := httptest.NewRecorder()
	applicationHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/allow", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("X-Yufeng-Upstream") != "reached" {
		t.Fatalf("status=%d header=%q", rec.Code, rec.Header().Get("X-Yufeng-Upstream"))
	}
}
