package healthz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthzResponseIncludesStableFields(t *testing.T) {
	start := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)

	rsp := Base("monitor", "monitor-local-1", "dev", "abc1234", start, true)

	if rsp.Module != "monitor" {
		t.Fatalf("Module = %q, want monitor", rsp.Module)
	}
	if rsp.InstanceID != "monitor-local-1" {
		t.Fatalf("InstanceID = %q, want monitor-local-1", rsp.InstanceID)
	}
	if !rsp.Ready {
		t.Fatal("Ready = false, want true")
	}
	if rsp.Status != "ok" {
		t.Fatalf("Status = %q, want ok", rsp.Status)
	}
	if rsp.Version != "dev" {
		t.Fatalf("Version = %q, want dev", rsp.Version)
	}
	if rsp.GitCommit != "abc1234" {
		t.Fatalf("GitCommit = %q, want abc1234", rsp.GitCommit)
	}
	if !rsp.StartTime.Equal(start) {
		t.Fatalf("StartTime = %s, want %s", rsp.StartTime, start)
	}
	if rsp.Time.IsZero() {
		t.Fatal("Time is zero")
	}

	body, err := json.Marshal(rsp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	for _, field := range []string{
		`"module"`,
		`"instance_id"`,
		`"ready"`,
		`"status"`,
		`"version"`,
		`"git_commit"`,
		`"start_time"`,
		`"time"`,
	} {
		if !jsonContains(body, field) {
			t.Fatalf("response JSON %s missing %s", body, field)
		}
	}
}

func TestHealthzHandlerReturns200WhenReady(t *testing.T) {
	handler := Handler(func(context.Context) Response {
		return Response{Module: "admin", Ready: true, Status: "ok"}
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestHealthzHandlerReturns503WhenNotReady(t *testing.T) {
	handler := Handler(func(context.Context) Response {
		return Response{Module: "storage", Ready: false}
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}

	var rsp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &rsp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rsp.Status != "degraded" {
		t.Fatalf("Status = %q, want degraded", rsp.Status)
	}
}

func TestLivenessHandlerReturns200WhenDependenciesAreNotReady(t *testing.T) {
	handler := LivenessHandler(func(context.Context) Response {
		return Response{Module: "storage", Ready: false, Status: "degraded"}
	})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var rsp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &rsp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("liveness response = %+v", rsp)
	}
}

func TestStandardMuxSeparatesLivenessAndReadiness(t *testing.T) {
	state := func(context.Context) Response { return Response{Module: "test", Ready: false} }
	handler := StandardMux(state, nil)
	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/healthz", want: http.StatusOK},
		{path: "/readyz", want: http.StatusServiceUnavailable},
	} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.path, rr.Code, tc.want)
		}
	}
}

func TestStandardMuxRejectsNonGETHealthRequests(t *testing.T) {
	handler := StandardMux(func(context.Context) Response { return Response{Ready: true} }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
		if rr.Code != http.StatusMethodNotAllowed || rr.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("%s response = %d Allow=%q", path, rr.Code, rr.Header().Get("Allow"))
		}
	}
}

func TestRegisterNoProtocolServiceMuxRejectsMissingInputs(t *testing.T) {
	if err := RegisterNoProtocolServiceMux(nil, NewMux()); err == nil {
		t.Fatal("nil service should be rejected")
	}
	if err := RegisterNoProtocolServiceMux(nil, nil); err == nil {
		t.Fatal("nil service and handler should be rejected")
	}
}

func TestStateSnapshotGatesReadiness(t *testing.T) {
	state := NewState("test", "instance", "", "")
	state.SnapshotFunc = func(context.Context) Response {
		return Base("test", "instance", "", "", state.StartedAt, true)
	}
	rsp := state.Snapshot(context.Background())
	if rsp.Ready || rsp.Status != "degraded" {
		t.Fatalf("snapshot = %+v, want degraded and not ready", rsp)
	}
}

func TestMuxDispatchesExactAndPrefixRoutes(t *testing.T) {
	mux := NewMux()
	mux.HandleFunc("/exact", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("exact")) })
	mux.HandlePrefix("/debug/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("prefix")) }))
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/exact", want: "exact"},
		{path: "/debug/pprof/heap", want: "prefix"},
	} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != http.StatusOK || rr.Body.String() != tc.want {
			t.Fatalf("%s response = %d/%q, want 200/%q", tc.path, rr.Code, rr.Body.String(), tc.want)
		}
	}
}

func TestHealthzHandlerPreservesDetails(t *testing.T) {
	handler := Handler(func(context.Context) Response {
		return Response{
			Module: "monitor",
			Ready:  true,
			Status: "ok",
			Details: map[string]any{
				"db_ok":        true,
				"scheduler_ok": true,
			},
		}
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var rsp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &rsp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rsp.Details["db_ok"] != true {
		t.Fatalf("db_ok = %#v, want true", rsp.Details["db_ok"])
	}
	if rsp.Details["scheduler_ok"] != true {
		t.Fatalf("scheduler_ok = %#v, want true", rsp.Details["scheduler_ok"])
	}
}

func TestBaseIncludesOptionalRuntimeIdentityFields(t *testing.T) {
	t.Setenv("MOOX_BOOT_ID", "boot-a")
	t.Setenv("MOOX_BUILD_TIME", "2026-07-19T00:00:00Z")
	t.Setenv("MOOX_CONFIG_HASH", "sha256:config")
	t.Setenv("MOOX_PIPELINE_CONFIG_HASH", "sha256:pipelines")

	rsp := Base("test", "test@node-a", "v1", "commit", time.Now(), true)
	if rsp.BootID != "boot-a" || rsp.BuildTime == "" || rsp.ConfigHash != "sha256:config" || rsp.PipelineConfigHash != "sha256:pipelines" {
		t.Fatalf("runtime identity fields = %+v", rsp)
	}
}

func jsonContains(body []byte, field string) bool {
	for i := 0; i+len(field) <= len(body); i++ {
		if string(body[i:i+len(field)]) == field {
			return true
		}
	}
	return false
}
