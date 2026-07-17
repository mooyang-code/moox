package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"
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
	state.RouteValidationFailed()
	state.AuthFailed()
	state.ReplayFailed()
	state.UpstreamFailed("connection")
	state.UpstreamFailed("timeout")
	state.ObserveRequest("monitor", "GetSnapshot", http.StatusOK, 250*time.Millisecond)
	state.RouteSyncSucceeded(time.Unix(123, 0))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics := recorder.Body.String()
	for _, want := range []string{
		"gateway_route_sync_errors_total 1",
		"gateway_route_validation_failures_total 1",
		"gateway_auth_failures_total 1",
		"gateway_replay_failures_total 1",
		`gateway_upstream_failures_total{type="connection"} 1`,
		`gateway_upstream_failures_total{type="timeout"} 1`,
		`gateway_requests_total{service="monitor",method="GetSnapshot",status="200"} 1`,
		`gateway_request_duration_seconds_sum{service="monitor",method="GetSnapshot"} 0.25`,
		"gateway_routes_current 2",
		`gateway_route_info{route_hash="hash"} 1`,
		"gateway_route_last_sync_timestamp_seconds 123",
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics missing %q:\n%s", want, metrics)
		}
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics = %d %q", recorder.Code, recorder.Body.String())
	}

	for _, path := range []string{"/", "/api/service/x/y", "/metrics/extra"} {
		assertStatus(t, mux, path, http.StatusNotFound)
	}
}

func TestMetricsExposeSharedTimerJobs(t *testing.T) {
	job, err := timerjob.New("gateway_health_test", time.Second, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Handle(context.Background()); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	NewState().Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics := recorder.Body.String()
	for _, want := range []string{
		`moox_timer_job_runs_total{job="gateway_health_test",result="success"} `,
		`moox_timer_job_duration_seconds_count{job="gateway_health_test"} `,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("metrics missing %q:\n%s", want, metrics)
		}
	}
}

func TestLivenessFailsWhenPersistentStorageIsUnavailable(t *testing.T) {
	state := NewState()
	state.SetStorageCheck(func() error { return errors.New("disk unavailable") })
	assertStatus(t, state.Handler(), "/healthz", http.StatusServiceUnavailable)
}

func assertStatus(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != want {
		t.Fatalf("%s status = %d, want %d", path, recorder.Code, want)
	}
}
