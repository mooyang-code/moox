package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadinessAndMetricsEndpoints(t *testing.T) {
	s := &Server{}
	s.ready.Store(false)
	r := httptest.NewRecorder()
	s.readyz(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d", r.Code)
	}
	s.ready.Store(true)
	r = httptest.NewRecorder()
	s.readyz(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil broker status = %d", r.Code)
	}
	r = httptest.NewRecorder()
	s.healthz(r, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if r.Code != http.StatusOK {
		t.Fatalf("liveness status = %d", r.Code)
	}
	r = httptest.NewRecorder()
	s.metrics(r, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(r.Body.String(), "moox_eventbus_connections") {
		t.Fatalf("metrics missing connection gauge: %s", r.Body.String())
	}
}
