package health

import (
	"net/http/httptest"
	"testing"
)

func TestHealthHandlerReturnsNotReadyUntilStateReady(t *testing.T) {
	state := New("archive", "test", "dev", "local")
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("status=%d", rec.Code)
	}
	state.ReadyFlag.Store(true)
	rec = httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ready status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("liveness status=%d", rec.Code)
	}
}
