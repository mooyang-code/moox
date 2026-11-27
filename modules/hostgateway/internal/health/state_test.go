package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/timerjob"
)

func TestStateHealthReadinessAndMetrics(t *testing.T) {
	state := NewState()
	mux := state.Handler()
	assertStatus(t, mux, "/healthz", http.StatusOK)
	assertStatus(t, mux, "/readyz", http.StatusServiceUnavailable)

	state.ApplyRoutes("hash", 2, true)
	state.RouteSyncSucceeded(time.Now())
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
	state.UpstreamFailed("ignored")
	state.ObserveRequest("monitor", "GetSnapshot", http.StatusOK, 250*time.Millisecond)
	state.RouteSyncSucceeded(time.Unix(123, 0))

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics = %d %q", recorder.Code, recorder.Body.String())
	}
	metrics := recorder.Body.String()
	for _, want := range []string{
		"host_gateway_route_sync_errors_total 1",
		"host_gateway_route_validation_failures_total 1",
		"host_gateway_auth_failures_total 1",
		"host_gateway_replay_failures_total 1",
		`host_gateway_upstream_failures_total{type="connection"} 1`,
		`host_gateway_upstream_failures_total{type="timeout"} 1`,
		`host_gateway_requests_total{method="GetSnapshot",service="monitor",status="200"} 1`,
		`host_gateway_request_duration_seconds_sum{method="GetSnapshot",service="monitor"} 0.25`,
		`host_gateway_request_duration_seconds_count{method="GetSnapshot",service="monitor"} 1`,
		"host_gateway_routes_current 2",
		`host_gateway_route_info{route_hash="hash"} 1`,
		"host_gateway_route_last_sync_timestamp_seconds 123",
		"host_gateway_route_sync_stale 1",
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics missing %q:\n%s", want, metrics)
		}
	}

	// 路由哈希变更时，只暴露当前快照的 route_info，不残留旧哈希序列。
	state.ApplyRoutes("hash-v2", 3, false)
	state.RouteSyncSucceeded(time.Now())
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics = recorder.Body.String()
	if strings.Contains(metrics, `route_hash="hash"`) || !strings.Contains(metrics, `host_gateway_route_info{route_hash="hash-v2"} 1`) {
		t.Fatalf("route_info did not replace previous route hash:\n%s", metrics)
	}
	if !strings.Contains(metrics, "host_gateway_route_sync_stale 0") {
		t.Fatalf("expected fresh route_sync_stale 0:\n%s", metrics)
	}

	for _, path := range []string{"/", "/api/service/x/y", "/metrics/extra"} {
		assertStatus(t, mux, path, http.StatusNotFound)
	}
	// StandardMux 拒绝非 GET 请求。
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want %d", path, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestReadinessRequiresFreshSyncAndHeartbeat(t *testing.T) {
	now := time.Now()
	state := NewState()
	state.SetClock(func() time.Time { return now })
	state.SetRouteSyncStaleAfter(time.Second)

	ready, reason := state.Readiness(context.Background())
	if ready || reason == "" {
		t.Fatalf("initial Readiness = (%v, %q), want not ready with reason", ready, reason)
	}
	state.ApplyRoutes("hash", 1, false)
	assertStatus(t, state.Handler(), "/readyz", http.StatusServiceUnavailable)
	ready, reason = state.Readiness(context.Background())
	if ready || reason == "" {
		t.Fatalf("unsynced Readiness = (%v, %q), want not ready with reason", ready, reason)
	}

	state.RouteSyncSucceeded(now)
	assertStatus(t, state.Handler(), "/readyz", http.StatusOK)

	now = now.Add(2 * time.Second)
	assertStatus(t, state.Handler(), "/readyz", http.StatusServiceUnavailable)
	state.RouteSyncSucceeded(now)
	assertStatus(t, state.Handler(), "/readyz", http.StatusOK)
	now = now.Add(2 * time.Second)
	assertStatus(t, state.Handler(), "/readyz", http.StatusServiceUnavailable)
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

func TestHealthJSONBindsRuntimeProcessAndSnapshotIdentity(t *testing.T) {
	t.Setenv("MOOX_NODE_ID", "control")
	t.Setenv("MOOX_INSTANCE_ID", "host-gateway@control")
	t.Setenv("MOOX_BOOT_ID", strings.Repeat("b", 64))
	state := NewState()
	for _, path := range []string{"/healthz", "/readyz"} {
		if path == "/readyz" {
			state.ApplyRoutes("snapshot-hash", 2, false)
			state.RouteSyncSucceeded(time.Now())
		}
		recorder := httptest.NewRecorder()
		state.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		var payload healthz.Response
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || !payload.Ready || len(payload.BinarySHA256) != 64 || payload.BootID != strings.Repeat("b", 64) || payload.InstanceID != "host-gateway@control" {
			t.Fatalf("health identity mismatch: %+v", payload)
		}
		if path == "/readyz" && payload.Details["route_hash"] != "snapshot-hash" {
			t.Fatal("readiness must identify the applied route snapshot")
		}
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
