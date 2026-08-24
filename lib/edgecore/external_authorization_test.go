package edgecore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestExtAuthzTimeoutFailOpenThenTrip(t *testing.T) {
	e := NewExtAuthz("asset-1", func(CanonicalView, Request) Action {
		time.Sleep(80 * time.Millisecond)
		return ActionBlock
	})
	e.timeout = 5 * time.Millisecond
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first timeout want 200, got %d", rec.Code)
	}
	if e.Timeouts < 1 {
		t.Fatal("timeout metric must increase")
	}
	for i := 0; i < 25; i++ {
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}
	saw503 := false
	for i := 0; i < 5; i++ {
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/y", nil))
		if rec.Code == http.StatusServiceUnavailable {
			saw503 = true
		}
	}
	if !saw503 {
		t.Fatalf("after trip want at least one 503, timeouts=%d tripped checks=%d", e.Timeouts, e.Checks)
	}
}

func TestExtAuthzHalfOpenRecordsAndRecovers(t *testing.T) {
	calls := 0
	e := NewExtAuthz("asset-1", func(CanonicalView, Request) Action {
		calls++
		return ActionAllow
	})
	e.timeout = time.Second
	now := time.Now()
	e.now = func() time.Time { return now }
	e.mu.Lock()
	e.tripped = true
	e.mu.Unlock()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("half-open admit want 200 got %d", rec.Code)
	}
	if calls != 1 {
		t.Fatalf("half-open must call gate, calls=%d", calls)
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/deny", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("same-second extra probe want 503 got %d", rec.Code)
	}
	now = now.Add(2 * time.Second)
	e.mu.Lock()
	e.events = []timeoutSample{{at: now.Add(-kernel.ExtAuthzTimeoutRateWindow - time.Second), timeout: true}}
	e.mu.Unlock()
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/recover-start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery window first probe want 200 got %d", rec.Code)
	}
	now = now.Add(kernel.ExtAuthzTimeoutRateRecoverHold + time.Second)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/recover-finish", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery window final probe want 200 got %d", rec.Code)
	}
	e.mu.Lock()
	tripped := e.tripped
	e.mu.Unlock()
	if tripped {
		t.Fatal("sustained healthy half-open probes must close circuit")
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/closed", nil))
	if rec.Code != http.StatusOK || calls != 4 {
		t.Fatalf("closed circuit must admit immediately: status=%d calls=%d", rec.Code, calls)
	}
}

func TestExtAuthzMissingBodySkipsBodyPolicy(t *testing.T) {
	var saw CanonicalView
	e := NewExtAuthz("asset-1", func(view CanonicalView, _ Request) Action {
		saw = view
		if ShouldSkipBodyPolicy(view) {
			return ActionAllow
		}
		return ActionBlock
	})
	req := httptest.NewRequest(http.MethodPost, "/api", nil)
	req.ContentLength = 100
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("body absent must not 403, got %d", rec.Code)
	}
	if CoverageOf(saw.Coverage, commonv1.InspectionSurface_INSPECTION_SURFACE_BODY) != commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT {
		t.Fatalf("body coverage=%v", saw.Coverage)
	}
	if e.LastSkipReason != "coverage" {
		t.Fatalf("skip reason=%q", e.LastSkipReason)
	}
}

func TestExtAuthzRejectsEnvoyPartialBody(t *testing.T) {
	called := false
	e := NewExtAuthz("asset-1", func(CanonicalView, Request) Action {
		called = true
		return ActionAllow
	})
	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(strings.Repeat("x", kernel.EngineBodyLimitBytes)))
	req.Header.Set(envoyAuthPartialBodyHeader, "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("partial body want 403 got %d", rec.Code)
	}
	if called {
		t.Fatal("partial body must not reach gate")
	}
}

func TestExtAuthzDetectorPanicDoesNotCrash(t *testing.T) {
	e := NewExtAuthz("asset-1", func(CanonicalView, Request) Action {
		panic("detector boom")
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("panic must fail open 200, got %d", rec.Code)
	}
}

func TestExtAuthzAndProxySameCanonicalView(t *testing.T) {
	headers := map[string]string{"Content-Type": "text/plain"}
	body := []byte("hello")
	proxyView := Canonicalize("POST", "/api/items", "id=1", headers, body, DefaultInspectionProfile())
	var extView CanonicalView
	e := NewExtAuthz("a", func(view CanonicalView, _ Request) Action {
		extView = view
		return ActionAllow
	})
	req := httptest.NewRequest(http.MethodPost, "/api/items?id=1", strings.NewReader("hello"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if extView.Path != proxyView.Path || extView.Rejected != proxyView.Rejected {
		t.Fatalf("views differ proxy=%+v ext=%+v", proxyView, extView)
	}
}
