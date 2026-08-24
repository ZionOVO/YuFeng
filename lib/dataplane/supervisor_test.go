package dataplane

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSupervisorOnlyProjectsManuallyStartedEdgeReadiness(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true,"unit_id":"edge-1","generation_id":"gen-1","generation_seq":1,"listen_plan_version":1}`))
	}))
	defer edge.Close()

	supervisor := &Supervisor{ProbeURL: edge.URL + "/ready"}
	recorder := httptest.NewRecorder()
	supervisor.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSupervisorExposesNoLifecycleMutationEndpoint(t *testing.T) {
	supervisor := &Supervisor{}
	for _, path := range []string{"/v1/ensure-local", "/v1/start", "/v1/rebuild", "/v1/upgrade"} {
		recorder := httptest.NewRecorder()
		supervisor.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d want 404", path, recorder.Code)
		}
	}
}
