package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminHandlerProbes(t *testing.T) {
	h := Handler(nil, "dev", "v1")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	for _, path := range []string{"/livez", "/readyz", "/metrics", "/version"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
}

func TestAdminMetricsIncludeRequiredNames(t *testing.T) {
	h := Handler(nil, "dev", "v1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, n := range RequiredMetricNames {
		if !containsName(body, n) {
			t.Fatalf("missing %s in %s", n, body)
		}
	}
}

func containsName(body, n string) bool {
	return len(body) > 0 && (len(n) == 0 || (len(body) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(body); i++ {
			if body[i:i+len(n)] == n {
				return true
			}
		}
		return false
	})()))
}

func TestAdminReadyzFailsClosed(t *testing.T) {
	h := Handler(func(context.Context) error { return errors.New("down") }, "dev", "v1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", rec.Code)
	}
}
