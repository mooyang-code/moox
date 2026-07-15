package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStateHealthReadinessAndMetrics(t *testing.T) {
	state := NewState()
	mux := state.Handler()
	assertStatus(t, mux, "/healthz", http.StatusOK)
	assertStatus(t, mux, "/readyz", http.StatusServiceUnavailable)

	state.ApplyRoutes("hash", 2, true)
	assertStatus(t, mux, "/readyz", http.StatusOK)
	if !state.Disabled() {
		t.Fatal("Disabled() = false")
	}
	state.RouteSyncFailed()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "gateway_route_sync_errors_total 1") {
		t.Fatalf("metrics = %d %q", recorder.Code, recorder.Body.String())
	}

	for _, path := range []string{"/", "/api/service/x/y", "/metrics/extra"} {
		assertStatus(t, mux, path, http.StatusNotFound)
	}
}

func assertStatus(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != want {
		t.Fatalf("%s status = %d, want %d", path, recorder.Code, want)
	}
}
